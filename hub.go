package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"nhooyr.io/websocket"
)

type role string

const (
	rolePhone   role = "phone"
	roleControl role = "control"
	roleViewer  role = "viewer" // alias for control
	roleView    role = "view"   // bare OBS page for one device
)

type signalMsg struct {
	Type       string       `json:"type"`
	SDP        string       `json:"sdp,omitempty"`
	Candidate  string       `json:"candidate,omitempty"`
	SDPMid     string       `json:"sdpMid,omitempty"`
	SDPMLineIx *int         `json:"sdpMLineIndex,omitempty"`
	Message    string       `json:"message,omitempty"`
	ID         string       `json:"id,omitempty"`
	Resume     string       `json:"resume,omitempty"`
	Devices    []deviceInfo `json:"devices,omitempty"`
	Code       string       `json:"code,omitempty"`
	URL        string       `json:"url,omitempty"`
	ExpiresAt  int64        `json:"expiresAt,omitempty"`
	Codec      string       `json:"codec,omitempty"`
}

const phoneResumeGrace = 90 * time.Second

type deviceInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Capability string `json:"capability"`
	State      string `json:"state"`
	Active     bool   `json:"active"`
}

type device struct {
	ID          string
	Name        string
	State       string // connecting | live | reconnecting
	Gen         int
	ResumeToken string
	ResumeUntil time.Time
	PC          *webrtc.PeerConnection
	HasVideo    bool
	HasAudio    bool
	VideoRemote *webrtc.TrackRemote
	VideoLocal  *webrtc.TrackLocalStaticRTP
	VideoMime   string
	AudioRemote *webrtc.TrackRemote
	AudioLocal  *webrtc.TrackLocalStaticRTP
}

func (d *device) capability() string {
	switch {
	case d.HasVideo && d.HasAudio:
		return "av"
	case d.HasAudio:
		return "audio"
	default:
		return "video"
	}
}

type viewSession struct {
	gen      int
	deviceID string
	send     func(signalMsg)
	pc       *webrtc.PeerConnection
}

type Hub struct {
	api          *webrtc.API
	phoneBaseURL string
	videoCodec   string // h264 | vp8

	mu          sync.Mutex
	devices     map[string]*device
	activeID    string
	pairCode    string
	pairExpires time.Time
	controlPC   *webrtc.PeerConnection
	controlSend func(signalMsg)
	controlGen  int
	views       map[int]*viewSession
	viewGen     int
}

func newHub() (*Hub, error) {
	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterDefaultCodecs(); err != nil {
		return nil, err
	}

	registry := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(mediaEngine, registry); err != nil {
		return nil, err
	}

	se := webrtc.SettingEngine{}
	se.SetInterfaceFilter(func(name string) bool {
		n := strings.ToLower(name)
		switch {
		case n == "docker0", n == "lo":
			return n == "lo"
		case strings.HasPrefix(n, "br-"),
			strings.HasPrefix(n, "veth"),
			strings.HasPrefix(n, "virbr"),
			strings.HasPrefix(n, "vmnet"),
			strings.HasPrefix(n, "vbox"),
			strings.HasPrefix(n, "zt"),
			strings.HasPrefix(n, "tailscale"):
			return false
		default:
			return true
		}
	})

	api := webrtc.NewAPI(
		webrtc.WithMediaEngine(mediaEngine),
		webrtc.WithInterceptorRegistry(registry),
		webrtc.WithSettingEngine(se),
	)

	h := &Hub{
		api:        api,
		devices:    make(map[string]*device),
		views:      make(map[int]*viewSession),
		videoCodec: "h264",
	}
	h.mu.Lock()
	err := h.rotatePairLocked("startup")
	h.mu.Unlock()
	if err != nil {
		return nil, err
	}
	go h.pairExpiryLoop()
	return h, nil
}

func (h *Hub) setPhoneBaseURL(base string) {
	h.mu.Lock()
	h.phoneBaseURL = strings.TrimRight(base, "/")
	h.mu.Unlock()
}

func (h *Hub) pairSnapshot() (code, url string, expires time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pairCode, phonePairURL(h.phoneBaseURL, h.pairCode), h.pairExpires
}

func (h *Hub) rotatePairLocked(reason string) error {
	code, err := generatePairCode()
	if err != nil {
		return err
	}
	h.pairCode = code
	h.pairExpires = time.Now().Add(pairCodeTTL)
	log.Printf("pairing code rotated (%s): %s (valid %s)", reason, code, pairCodeTTL)
	return nil
}

func (h *Hub) pairStatusMsgLocked() signalMsg {
	return signalMsg{
		Type:      "pair",
		Code:      h.pairCode,
		URL:       phonePairURL(h.phoneBaseURL, h.pairCode),
		ExpiresAt: h.pairExpires.Unix(),
		Message:   fmt.Sprintf("valid for %s or until first phone connects", pairCodeTTL),
	}
}

func (h *Hub) pairExpiryLoop() {
	t := time.NewTicker(pairExpiryTick)
	defer t.Stop()
	for range t.C {
		h.mu.Lock()
		expired := !h.pairExpires.IsZero() && time.Now().After(h.pairExpires)
		var msg signalMsg
		if expired {
			if err := h.rotatePairLocked("expired"); err != nil {
				h.mu.Unlock()
				log.Printf("pairing expiry rotate: %v", err)
				continue
			}
			msg = h.pairStatusMsgLocked()
		}
		h.mu.Unlock()
		if expired {
			h.notifyControl(msg)
		}
	}
}

func (h *Hub) tryConsumePairToken(r *http.Request) bool {
	t := r.URL.Query().Get("t")
	if t == "" {
		t = r.URL.Query().Get("token")
	}
	got := normalizePairCode(t)

	h.mu.Lock()
	if got == "" || got != normalizePairCode(h.pairCode) {
		h.mu.Unlock()
		return false
	}
	if time.Now().After(h.pairExpires) {
		_ = h.rotatePairLocked("expired-on-use")
		msg := h.pairStatusMsgLocked()
		h.mu.Unlock()
		h.notifyControl(msg)
		return false
	}
	if err := h.rotatePairLocked("used"); err != nil {
		h.mu.Unlock()
		log.Printf("pairing consume rotate: %v", err)
		return false
	}
	msg := h.pairStatusMsgLocked()
	h.mu.Unlock()
	h.notifyControl(msg)
	return true
}

func (h *Hub) authorizePhone(r *http.Request) (resumeID string, ok bool) {
	if tok := strings.TrimSpace(r.URL.Query().Get("resume")); tok != "" {
		h.mu.Lock()
		defer h.mu.Unlock()
		now := time.Now()
		for _, d := range h.devices {
			if d.ResumeToken == tok && d.State == "reconnecting" && now.Before(d.ResumeUntil) {
				log.Printf("phone resume authorized for %s (%s)", d.ID, d.Name)
				return d.ID, true
			}
		}
		return "", false
	}
	if !h.tryConsumePairToken(r) {
		return "", false
	}
	return "", true
}

func (h *Hub) handlePhoneWS(w http.ResponseWriter, r *http.Request) {
	h.handleWS(w, r, rolePhone)
}

func (h *Hub) handleControlListenerWS(w http.ResponseWriter, r *http.Request) {
	rRole := role(r.URL.Query().Get("role"))
	if rRole == roleViewer {
		rRole = roleControl
	}
	switch rRole {
	case roleControl, roleView:
		h.handleWS(w, r, rRole)
	default:
		http.Error(w, "role must be control or view", http.StatusBadRequest)
	}
}

func (h *Hub) handleWS(w http.ResponseWriter, r *http.Request, allowed role) {
	rRole := role(r.URL.Query().Get("role"))
	if rRole == roleViewer {
		rRole = roleControl
	}
	if rRole != allowed {
		http.Error(w, "role not allowed on this listener", http.StatusForbidden)
		return
	}

	resumeID := ""
	deviceID := ""
	if rRole == rolePhone {
		var ok bool
		resumeID, ok = h.authorizePhone(r)
		if !ok {
			http.Error(w, "pairing code required or expired", http.StatusForbidden)
			return
		}
	}
	if rRole == roleView {
		deviceID = strings.TrimSpace(r.URL.Query().Get("id"))
		if deviceID == "" {
			http.Error(w, "view requires id=", http.StatusBadRequest)
			return
		}
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		log.Printf("websocket accept: %v", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := r.Context()
	var sendMu sync.Mutex
	send := func(msg signalMsg) {
		b, err := json.Marshal(msg)
		if err != nil {
			return
		}
		sendMu.Lock()
		defer sendMu.Unlock()
		writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := conn.Write(writeCtx, websocket.MessageText, b); err != nil {
			log.Printf("ws write (%s): %v", rRole, err)
		}
	}

	switch rRole {
	case rolePhone:
		if err := h.servePhone(ctx, r, conn, send, resumeID); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("phone session: %v", err)
			send(signalMsg{Type: "error", Message: err.Error()})
		}
	case roleControl:
		if err := h.serveControl(ctx, conn, send); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("control session: %v", err)
			send(signalMsg{Type: "error", Message: err.Error()})
		}
	case roleView:
		if err := h.serveView(ctx, conn, send, deviceID); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("view session %s: %v", deviceID, err)
			send(signalMsg{Type: "error", Message: err.Error()})
		}
	}
}

func (h *Hub) servePhone(ctx context.Context, r *http.Request, conn *websocket.Conn, send func(signalMsg), resumeID string) error {
	pc, err := h.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return err
	}

	resumeToken, err := newResumeToken()
	if err != nil {
		_ = pc.Close()
		return err
	}

	var id, name string
	var gen int

	h.mu.Lock()
	if resumeID != "" {
		d := h.devices[resumeID]
		if d == nil || d.State != "reconnecting" {
			h.mu.Unlock()
			_ = pc.Close()
			return fmt.Errorf("resume session not available")
		}
		d.Gen++
		gen = d.Gen
		id = d.ID
		name = d.Name
		d.ResumeToken = resumeToken
		d.ResumeUntil = time.Time{}
		d.State = "connecting"
		d.PC = pc
		d.HasVideo = false
		d.HasAudio = false
		d.VideoRemote = nil
		d.VideoLocal = nil
		d.VideoMime = ""
		d.AudioRemote = nil
		d.AudioLocal = nil
		log.Printf("device resume %s (%s) gen=%d", id, name, gen)
	} else {
		id, err = newDeviceID()
		if err != nil {
			h.mu.Unlock()
			_ = pc.Close()
			return err
		}
		name = phoneName(r)
		dev := &device{
			ID:          id,
			Name:        name,
			State:       "connecting",
			Gen:         1,
			ResumeToken: resumeToken,
			PC:          pc,
		}
		gen = 1
		h.devices[id] = dev
		log.Printf("device + %s (%s)", id, name)
	}
	h.mu.Unlock()
	h.broadcastDevices()

	defer func() {
		_ = pc.Close()
		h.onPhoneSessionEnd(id, gen)
	}()

	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{
		Direction: webrtc.RTPTransceiverDirectionRecvonly,
	}); err != nil {
		return err
	}
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio, webrtc.RTPTransceiverInit{
		Direction: webrtc.RTPTransceiverDirectionRecvonly,
	}); err != nil {
		return err
	}

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		init := c.ToJSON()
		msg := signalMsg{Type: "candidate", Candidate: init.Candidate}
		if init.SDPMid != nil {
			msg.SDPMid = *init.SDPMid
		}
		if init.SDPMLineIndex != nil {
			idx := int(*init.SDPMLineIndex)
			msg.SDPMLineIx = &idx
		}
		send(msg)
	})

	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		log.Printf("phone %s peer state: %s", id, s)
		switch s {
		case webrtc.PeerConnectionStateDisconnected, webrtc.PeerConnectionStateFailed:
			h.markPhoneLinkDown(id, gen)
		case webrtc.PeerConnectionStateConnected:
			h.markPhoneLinkUp(id, gen)
		}
	})

	pc.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		switch remote.Kind() {
		case webrtc.RTPCodecTypeVideo:
			h.handleVideoTrack(ctx, id, pc, remote)
		case webrtc.RTPCodecTypeAudio:
			h.handleAudioTrack(ctx, id, remote)
		}
	})

	send(signalMsg{Type: "status", Message: "ready", ID: id, Resume: resumeToken, Codec: h.preferredCodec()})

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var msg signalMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if err := h.handlePhoneSignal(pc, msg, send); err != nil {
			return err
		}
	}
}

func (h *Hub) handleVideoTrack(ctx context.Context, id string, pc *webrtc.PeerConnection, remote *webrtc.TrackRemote) {
	mime := remote.Codec().MimeType
	log.Printf("phone %s video: %s", id, mime)

	local, err := webrtc.NewTrackLocalStaticRTP(
		remote.Codec().RTPCodecCapability,
		"video",
		id,
	)
	if err != nil {
		log.Printf("local track: %v", err)
		return
	}

	h.mu.Lock()
	d := h.devices[id]
	if d == nil {
		h.mu.Unlock()
		return
	}
	d.VideoRemote = remote
	d.VideoLocal = local
	d.VideoMime = mime
	d.HasVideo = true
	d.State = "live"
	becameActive := false
	if h.activeID == "" {
		h.activeID = id
		becameActive = true
	}
	isActive := h.activeID == id
	h.mu.Unlock()

	h.broadcastDevices()
	if isActive || becameActive {
		h.notifyControl(signalMsg{Type: "status", Message: "track-ready"})
	}
	h.notifyViews(id, signalMsg{Type: "status", Message: "track-ready", ID: id})

	_ = pc.WriteRTCP([]rtcp.Packet{
		&rtcp.PictureLossIndication{MediaSSRC: uint32(remote.SSRC())},
	})
	go pliLoop(ctx, pc, remote)

	for {
		pkt, _, readErr := remote.ReadRTP()
		if readErr != nil {
			return
		}
		raw, mErr := pkt.Marshal()
		if mErr != nil {
			continue
		}
		if _, err := local.Write(raw); err != nil && !errors.Is(err, io.ErrClosedPipe) {
			return
		}
	}
}

func (h *Hub) handleAudioTrack(ctx context.Context, id string, remote *webrtc.TrackRemote) {
	mime := remote.Codec().MimeType
	log.Printf("phone %s audio: %s", id, mime)

	local, err := webrtc.NewTrackLocalStaticRTP(
		remote.Codec().RTPCodecCapability,
		"audio",
		id,
	)
	if err != nil {
		log.Printf("local audio track: %v", err)
		return
	}

	h.mu.Lock()
	d := h.devices[id]
	if d == nil {
		h.mu.Unlock()
		return
	}
	d.AudioRemote = remote
	d.AudioLocal = local
	d.HasAudio = true
	d.State = "live"
	becameActive := false
	if h.activeID == "" {
		h.activeID = id
		becameActive = true
	}
	isActive := h.activeID == id
	hasVideo := d.HasVideo
	h.mu.Unlock()

	h.broadcastDevices()
	if becameActive || isActive {
		if hasVideo {
			h.notifyControl(signalMsg{Type: "status", Message: "track-ready"})
		} else {
			h.notifyControl(signalMsg{Type: "status", Message: "audio-only"})
		}
	}
	h.notifyViews(id, signalMsg{Type: "status", Message: "track-ready", ID: id})

	_ = ctx
	for {
		pkt, _, readErr := remote.ReadRTP()
		if readErr != nil {
			return
		}
		raw, mErr := pkt.Marshal()
		if mErr != nil {
			continue
		}
		if _, err := local.Write(raw); err != nil && !errors.Is(err, io.ErrClosedPipe) {
			return
		}
	}
}

func (h *Hub) handlePhoneSignal(pc *webrtc.PeerConnection, msg signalMsg, send func(signalMsg)) error {
	switch msg.Type {
	case "offer":
		if err := pc.SetRemoteDescription(webrtc.SessionDescription{
			Type: webrtc.SDPTypeOffer,
			SDP:  msg.SDP,
		}); err != nil {
			return fmt.Errorf("set remote offer: %w", err)
		}
		answer, err := pc.CreateAnswer(nil)
		if err != nil {
			return fmt.Errorf("create answer: %w", err)
		}
		if err := pc.SetLocalDescription(answer); err != nil {
			return fmt.Errorf("set local answer: %w", err)
		}
		send(signalMsg{Type: "answer", SDP: answer.SDP})
	case "candidate":
		if msg.Candidate == "" {
			return nil
		}
		cand := webrtc.ICECandidateInit{Candidate: msg.Candidate}
		if msg.SDPMid != "" {
			cand.SDPMid = &msg.SDPMid
		}
		if msg.SDPMLineIx != nil {
			idx := uint16(*msg.SDPMLineIx)
			cand.SDPMLineIndex = &idx
		}
		if err := pc.AddICECandidate(cand); err != nil {
			log.Printf("phone add candidate: %v", err)
		}
	}
	return nil
}

func (h *Hub) serveControl(ctx context.Context, conn *websocket.Conn, send func(signalMsg)) error {
	h.mu.Lock()
	h.controlGen++
	gen := h.controlGen
	h.controlSend = send
	hasActive := h.activeLocalLocked() != nil
	hasAudioOnly := false
	phoneDisconnected := false
	if h.activeID != "" {
		if d := h.devices[h.activeID]; d != nil {
			switch {
			case d.State == "reconnecting":
				phoneDisconnected = true
			case d.HasAudio && !d.HasVideo:
				hasAudioOnly = true
			}
		}
	}
	pairMsg := h.pairStatusMsgLocked()
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		if h.controlGen == gen {
			h.controlSend = nil
			if h.controlPC != nil {
				_ = h.controlPC.Close()
				h.controlPC = nil
			}
		}
		h.mu.Unlock()
	}()

	send(pairMsg)
	send(signalMsg{Type: "codec", Codec: h.preferredCodec()})
	send(signalMsg{Type: "devices", Devices: h.snapshotDevices()})
	switch {
	case phoneDisconnected:
		send(signalMsg{Type: "status", Message: "phone-disconnected"})
	case hasActive:
		send(signalMsg{Type: "status", Message: "track-ready"})
	case hasAudioOnly:
		send(signalMsg{Type: "status", Message: "audio-only"})
	default:
		send(signalMsg{Type: "status", Message: "waiting-for-phone"})
	}

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var msg signalMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if err := h.handleControlSignal(msg, send); err != nil {
			return err
		}
	}
}

func (h *Hub) serveView(ctx context.Context, conn *websocket.Conn, send func(signalMsg), deviceID string) error {
	h.mu.Lock()
	h.viewGen++
	gen := h.viewGen
	sess := &viewSession{gen: gen, deviceID: deviceID, send: send}
	h.views[gen] = sess
	d := h.devices[deviceID]
	ready := d != nil && d.State == "live" && (d.VideoLocal != nil || d.AudioLocal != nil)
	name := ""
	if d != nil {
		name = d.Name
	}
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		if s := h.views[gen]; s != nil {
			if s.pc != nil {
				_ = s.pc.Close()
			}
			delete(h.views, gen)
		}
		h.mu.Unlock()
	}()

	log.Printf("view + gen=%d device=%s (%s)", gen, deviceID, name)
	if ready {
		send(signalMsg{Type: "status", Message: "track-ready", ID: deviceID})
	} else {
		send(signalMsg{Type: "status", Message: "waiting-for-phone", ID: deviceID})
	}

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var msg signalMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if err := h.handleViewSignal(gen, deviceID, msg, send); err != nil {
			return err
		}
	}
}

func (h *Hub) handleViewSignal(viewGen int, deviceID string, msg signalMsg, send func(signalMsg)) error {
	switch msg.Type {
	case "offer":
		h.mu.Lock()
		d := h.devices[deviceID]
		if d == nil || d.State != "live" || (d.VideoLocal == nil && d.AudioLocal == nil) {
			h.mu.Unlock()
			send(signalMsg{Type: "status", Message: "waiting-for-phone", ID: deviceID})
			return nil
		}
		video := d.VideoLocal
		audio := d.AudioLocal
		sess := h.views[viewGen]
		h.mu.Unlock()
		if sess == nil {
			return nil
		}

		pc, err := h.api.NewPeerConnection(webrtc.Configuration{})
		if err != nil {
			return err
		}
		pc.OnICECandidate(func(c *webrtc.ICECandidate) {
			if c == nil {
				return
			}
			init := c.ToJSON()
			out := signalMsg{Type: "candidate", Candidate: init.Candidate}
			if init.SDPMid != nil {
				out.SDPMid = *init.SDPMid
			}
			if init.SDPMLineIndex != nil {
				idx := int(*init.SDPMLineIndex)
				out.SDPMLineIx = &idx
			}
			send(out)
		})
		pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
			log.Printf("view gen=%d device=%s peer state: %s", viewGen, deviceID, s)
		})

		if video != nil {
			if err := h.attachTrack(pc, video); err != nil {
				_ = pc.Close()
				return err
			}
		}
		if audio != nil {
			if err := h.attachTrack(pc, audio); err != nil {
				_ = pc.Close()
				return err
			}
		}

		h.mu.Lock()
		if s := h.views[viewGen]; s != nil {
			if s.pc != nil {
				_ = s.pc.Close()
			}
			s.pc = pc
		} else {
			h.mu.Unlock()
			_ = pc.Close()
			return nil
		}
		h.mu.Unlock()

		if err := pc.SetRemoteDescription(webrtc.SessionDescription{
			Type: webrtc.SDPTypeOffer,
			SDP:  msg.SDP,
		}); err != nil {
			_ = pc.Close()
			return fmt.Errorf("view set remote offer: %w", err)
		}
		answer, err := pc.CreateAnswer(nil)
		if err != nil {
			_ = pc.Close()
			return fmt.Errorf("view create answer: %w", err)
		}
		if err := pc.SetLocalDescription(answer); err != nil {
			_ = pc.Close()
			return fmt.Errorf("view set local answer: %w", err)
		}
		send(signalMsg{Type: "answer", SDP: answer.SDP})

	case "candidate":
		if msg.Candidate == "" {
			return nil
		}
		h.mu.Lock()
		var pc *webrtc.PeerConnection
		if s := h.views[viewGen]; s != nil {
			pc = s.pc
		}
		h.mu.Unlock()
		if pc == nil {
			return nil
		}
		cand := webrtc.ICECandidateInit{Candidate: msg.Candidate}
		if msg.SDPMid != "" {
			cand.SDPMid = &msg.SDPMid
		}
		if msg.SDPMLineIx != nil {
			idx := uint16(*msg.SDPMLineIx)
			cand.SDPMLineIndex = &idx
		}
		if err := pc.AddICECandidate(cand); err != nil {
			log.Printf("view add candidate: %v", err)
		}
	}
	return nil
}

func (h *Hub) handleControlSignal(msg signalMsg, send func(signalMsg)) error {
	switch msg.Type {
	case "select":
		if msg.ID == "" {
			return nil
		}
		if err := h.setActive(msg.ID); err != nil {
			send(signalMsg{Type: "error", Message: err.Error()})
		}
		return nil

	case "codec":
		codec := msg.Codec
		if codec == "" {
			codec = msg.Message
		}
		if err := h.setPreferredCodec(codec); err != nil {
			send(signalMsg{Type: "error", Message: err.Error()})
			return nil
		}
		send(signalMsg{Type: "codec", Codec: h.preferredCodec()})
		return nil

	case "offer":
		h.mu.Lock()
		track := h.activeLocalLocked()
		h.mu.Unlock()
		if track == nil {
			send(signalMsg{Type: "status", Message: "waiting-for-phone"})
			return nil
		}

		pc, err := h.newControlPC(send)
		if err != nil {
			return err
		}
		if err := h.attachTrack(pc, track); err != nil {
			_ = pc.Close()
			return err
		}
		if err := pc.SetRemoteDescription(webrtc.SessionDescription{
			Type: webrtc.SDPTypeOffer,
			SDP:  msg.SDP,
		}); err != nil {
			_ = pc.Close()
			return fmt.Errorf("control set remote offer: %w", err)
		}
		answer, err := pc.CreateAnswer(nil)
		if err != nil {
			_ = pc.Close()
			return fmt.Errorf("control create answer: %w", err)
		}
		if err := pc.SetLocalDescription(answer); err != nil {
			_ = pc.Close()
			return fmt.Errorf("control set local answer: %w", err)
		}
		send(signalMsg{Type: "answer", SDP: answer.SDP})

	case "candidate":
		if msg.Candidate == "" {
			return nil
		}
		h.mu.Lock()
		pc := h.controlPC
		h.mu.Unlock()
		if pc == nil {
			return nil
		}
		cand := webrtc.ICECandidateInit{Candidate: msg.Candidate}
		if msg.SDPMid != "" {
			cand.SDPMid = &msg.SDPMid
		}
		if msg.SDPMLineIx != nil {
			idx := uint16(*msg.SDPMLineIx)
			cand.SDPMLineIndex = &idx
		}
		if err := pc.AddICECandidate(cand); err != nil {
			log.Printf("control add candidate: %v", err)
		}
	}
	return nil
}

func (h *Hub) newControlPC(send func(signalMsg)) (*webrtc.PeerConnection, error) {
	pc, err := h.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, err
	}

	h.mu.Lock()
	old := h.controlPC
	h.controlPC = pc
	h.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		init := c.ToJSON()
		msg := signalMsg{Type: "candidate", Candidate: init.Candidate}
		if init.SDPMid != nil {
			msg.SDPMid = *init.SDPMid
		}
		if init.SDPMLineIndex != nil {
			idx := int(*init.SDPMLineIndex)
			msg.SDPMLineIx = &idx
		}
		send(msg)
	})
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		log.Printf("control peer state: %s", s)
	})
	return pc, nil
}

func (h *Hub) attachTrack(pc *webrtc.PeerConnection, track *webrtc.TrackLocalStaticRTP) error {
	sender, err := pc.AddTrack(track)
	if err != nil {
		return err
	}
	go func() {
		rtcpBuf := make([]byte, 1500)
		for {
			if _, _, rtcpErr := sender.Read(rtcpBuf); rtcpErr != nil {
				return
			}
		}
	}()
	return nil
}

func (h *Hub) setActive(id string) error {
	h.mu.Lock()
	d, ok := h.devices[id]
	if !ok || d.State != "live" || (!d.HasVideo && !d.HasAudio) {
		h.mu.Unlock()
		return fmt.Errorf("device %s not available", id)
	}
	if h.activeID == id {
		h.mu.Unlock()
		h.broadcastDevices()
		return nil
	}
	prev := h.activeID
	h.activeID = id
	hasVideo := d.HasVideo
	videoRemote := d.VideoRemote
	pc := d.PC
	name := d.Name
	h.mu.Unlock()

	log.Printf("active → %s (%s) (was %s)", id, name, prev)
	h.broadcastDevices()
	if hasVideo {
		h.notifyControl(signalMsg{Type: "status", Message: "track-ready"})
		if videoRemote != nil && pc != nil {
			_ = pc.WriteRTCP([]rtcp.Packet{
				&rtcp.PictureLossIndication{MediaSSRC: uint32(videoRemote.SSRC())},
			})
		}
	} else {
		h.notifyControl(signalMsg{Type: "status", Message: "audio-only"})
	}
	return nil
}

func (h *Hub) onPhoneSessionEnd(id string, gen int) {
	h.mu.Lock()
	d := h.devices[id]
	if d == nil || d.Gen != gen {
		h.mu.Unlock()
		return
	}

	wasActive := h.activeID == id
	wasLive := d.State == "live" || d.State == "reconnecting"
	name := d.Name
	token := d.ResumeToken

	if !wasLive {
		delete(h.devices, id)
		if wasActive {
			h.activeID = ""
		}
		h.mu.Unlock()
		log.Printf("device - %s (%s) (never live)", id, name)
		if wasActive {
			h.notifyControl(signalMsg{Type: "status", Message: "waiting-for-phone"})
		}
		h.notifyViews(id, signalMsg{Type: "status", Message: "waiting-for-phone", ID: id})
		h.broadcastDevices()
		return
	}

	until := time.Now().Add(phoneResumeGrace)
	d.State = "reconnecting"
	d.PC = nil
	d.HasVideo = false
	d.HasAudio = false
	d.VideoRemote = nil
	d.VideoLocal = nil
	d.VideoMime = ""
	d.AudioRemote = nil
	d.AudioLocal = nil
	d.ResumeUntil = until
	h.mu.Unlock()

	log.Printf("device reconnecting %s (%s) — resume grace %s", id, name, phoneResumeGrace)
	if wasActive {
		h.notifyControl(signalMsg{Type: "status", Message: "phone-disconnected"})
	}
	h.notifyViews(id, signalMsg{Type: "status", Message: "phone-disconnected", ID: id})
	h.broadcastDevices()

	go h.expirePhoneResume(id, gen, token, until)
}

func (h *Hub) expirePhoneResume(id string, gen int, token string, until time.Time) {
	timer := time.NewTimer(time.Until(until))
	defer timer.Stop()
	<-timer.C

	h.mu.Lock()
	d := h.devices[id]
	if d == nil || d.Gen != gen || d.ResumeToken != token || d.State != "reconnecting" {
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()
	log.Printf("device resume expired %s — removing", id)
	h.removeDevice(id)
}

func (h *Hub) markPhoneLinkDown(id string, gen int) {
	h.mu.Lock()
	d := h.devices[id]
	if d == nil || d.Gen != gen || d.State != "live" {
		h.mu.Unlock()
		return
	}
	wasActive := h.activeID == id
	h.mu.Unlock()
	if wasActive {
		log.Printf("phone %s link down — notifying control", id)
		h.notifyControl(signalMsg{Type: "status", Message: "phone-disconnected"})
	}
	h.notifyViews(id, signalMsg{Type: "status", Message: "phone-disconnected", ID: id})
}

func (h *Hub) markPhoneLinkUp(id string, gen int) {
	h.mu.Lock()
	d := h.devices[id]
	if d == nil || d.Gen != gen || d.State != "live" {
		h.mu.Unlock()
		return
	}
	wasActive := h.activeID == id
	hasVideo := d.HasVideo
	hasAudio := d.HasAudio
	h.mu.Unlock()
	if wasActive {
		log.Printf("phone %s link up", id)
		if hasVideo {
			h.notifyControl(signalMsg{Type: "status", Message: "track-ready"})
		} else if hasAudio {
			h.notifyControl(signalMsg{Type: "status", Message: "audio-only"})
		}
	}
	if hasVideo || hasAudio {
		h.notifyViews(id, signalMsg{Type: "status", Message: "track-ready", ID: id})
	}
}

func (h *Hub) removeDevice(id string) {
	h.mu.Lock()
	d, ok := h.devices[id]
	if !ok {
		h.mu.Unlock()
		return
	}
	delete(h.devices, id)
	wasActive := h.activeID == id
	var next string
	var nextHasVideo bool
	if wasActive {
		h.activeID = ""
		for _, other := range h.devices {
			if other.State == "live" && (other.HasVideo || other.HasAudio) {
				next = other.ID
				nextHasVideo = other.HasVideo
				break
			}
		}
		h.activeID = next
	}
	name := d.Name
	h.mu.Unlock()

	log.Printf("device - %s (%s)", id, name)
	h.notifyViews(id, signalMsg{Type: "status", Message: "waiting-for-phone", ID: id})
	if wasActive {
		if next != "" {
			log.Printf("active failover → %s", next)
			if nextHasVideo {
				h.notifyControl(signalMsg{Type: "status", Message: "track-ready"})
			} else {
				h.notifyControl(signalMsg{Type: "status", Message: "audio-only"})
			}
		} else {
			h.notifyControl(signalMsg{Type: "status", Message: "waiting-for-phone"})
		}
	}
	h.broadcastDevices()
}

func (h *Hub) activeLocalLocked() *webrtc.TrackLocalStaticRTP {
	if h.activeID == "" {
		return nil
	}
	d := h.devices[h.activeID]
	if d == nil {
		return nil
	}
	return d.VideoLocal
}

func (h *Hub) snapshotDevices() []deviceInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]deviceInfo, 0, len(h.devices))
	for _, d := range h.devices {
		out = append(out, deviceInfo{
			ID:         d.ID,
			Name:       d.Name,
			Capability: d.capability(),
			State:      d.State,
			Active:     d.ID == h.activeID,
		})
	}
	return out
}

func (h *Hub) broadcastDevices() {
	msg := signalMsg{Type: "devices", Devices: h.snapshotDevices()}
	h.notifyControl(msg)
}

func (h *Hub) notifyControl(msg signalMsg) {
	h.mu.Lock()
	send := h.controlSend
	h.mu.Unlock()
	if send != nil {
		send(msg)
	}
}

func (h *Hub) notifyViews(deviceID string, msg signalMsg) {
	h.mu.Lock()
	sends := make([]func(signalMsg), 0)
	for _, s := range h.views {
		if s.deviceID == deviceID {
			sends = append(sends, s.send)
		}
	}
	h.mu.Unlock()
	for _, send := range sends {
		send(msg)
	}
}

func pliLoop(ctx context.Context, pc *webrtc.PeerConnection, track *webrtc.TrackRemote) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := pc.WriteRTCP([]rtcp.Packet{
				&rtcp.PictureLossIndication{MediaSSRC: uint32(track.SSRC())},
			}); err != nil {
				return
			}
		}
	}
}

func newDeviceID() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func newResumeToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func phoneName(r *http.Request) string {
	if n := strings.TrimSpace(r.URL.Query().Get("name")); n != "" {
		return n
	}
	ua := r.UserAgent()
	switch {
	case strings.Contains(ua, "iPhone"):
		return "iPhone"
	case strings.Contains(ua, "iPad"):
		return "iPad"
	case strings.Contains(ua, "Android"):
		return "Android"
	default:
		return "Phone"
	}
}
