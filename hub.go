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
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"nhooyr.io/websocket"
)

type role string

const (
	rolePhone   role = "phone"
	roleControl role = "control"
	roleViewer  role = "viewer" // alias for control (M1/M2 clients)
)

type signalMsg struct {
	Type       string       `json:"type"`
	SDP        string       `json:"sdp,omitempty"`
	Candidate  string       `json:"candidate,omitempty"`
	SDPMid     string       `json:"sdpMid,omitempty"`
	SDPMLineIx *int         `json:"sdpMLineIndex,omitempty"`
	Message    string       `json:"message,omitempty"`
	ID         string       `json:"id,omitempty"`
	Devices    []deviceInfo `json:"devices,omitempty"`
	Code       string       `json:"code,omitempty"`
	URL        string       `json:"url,omitempty"`
	Available  *bool        `json:"available,omitempty"`
	Command    string       `json:"command,omitempty"`
	Device     string       `json:"device,omitempty"`
	ExpiresAt  int64        `json:"expiresAt,omitempty"`
}

type deviceInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Capability string `json:"capability"`
	State      string `json:"state"`
	Active     bool   `json:"active"`
}

type device struct {
	ID         string
	Name       string
	State      string // connecting | live
	PC         *webrtc.PeerConnection
	HasVideo   bool
	HasAudio   bool
	VideoRemote *webrtc.TrackRemote
	VideoLocal  *webrtc.TrackLocalStaticRTP
	VideoMime   string
	AudioRemote *webrtc.TrackRemote
	AudioRate   uint32
	AudioChans  uint16
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

type Hub struct {
	api          *webrtc.API
	v4l2Device   string
	audioDest    string
	phoneBaseURL string

	mu          sync.Mutex
	devices     map[string]*device
	activeID    string
	vcam        *vcam
	asink       *asink
	v4l2OK      bool
	v4l2Msg     string
	pairCode    string
	pairExpires time.Time
	controlPC   *webrtc.PeerConnection
	controlSend func(signalMsg)
	controlGen  int
}

func newHub(v4l2Device, audioDest string) (*Hub, error) {
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
		v4l2Device: v4l2Device,
		audioDest:  audioDest,
		devices:    make(map[string]*device),
	}
	h.refreshV4L2Status()
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

// rotatePairLocked issues a new code. Caller must hold h.mu.
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

func (h *Hub) rotatePair(reason string) {
	h.mu.Lock()
	err := h.rotatePairLocked(reason)
	msg := h.pairStatusMsgLocked()
	h.mu.Unlock()
	if err != nil {
		log.Printf("pairing rotate: %v", err)
		return
	}
	h.notifyControl(msg)
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

// tryConsumePairToken validates the phone token and invalidates it on success
// (single-use). A fresh code is issued for the next pair.
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

func (h *Hub) handlePhoneWS(w http.ResponseWriter, r *http.Request) {
	h.handleWS(w, r, rolePhone)
}

func (h *Hub) handleControlWS(w http.ResponseWriter, r *http.Request) {
	h.handleWS(w, r, roleControl)
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
	if rRole != rolePhone && rRole != roleControl {
		http.Error(w, "role must be phone or control", http.StatusBadRequest)
		return
	}

	if rRole == rolePhone && !h.tryConsumePairToken(r) {
		http.Error(w, "pairing code required or expired", http.StatusForbidden)
		return
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
		if err := h.servePhone(ctx, r, conn, send); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("phone session: %v", err)
			send(signalMsg{Type: "error", Message: err.Error()})
		}
	case roleControl:
		if err := h.serveControl(ctx, conn, send); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("control session: %v", err)
			send(signalMsg{Type: "error", Message: err.Error()})
		}
	}
}

func (h *Hub) servePhone(ctx context.Context, r *http.Request, conn *websocket.Conn, send func(signalMsg)) error {
	id, err := newDeviceID()
	if err != nil {
		return err
	}
	name := phoneName(r)

	pc, err := h.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return err
	}

	dev := &device{
		ID:    id,
		Name:  name,
		State: "connecting",
		PC:    pc,
	}

	h.mu.Lock()
	h.devices[id] = dev
	h.mu.Unlock()
	log.Printf("device + %s (%s)", id, name)
	h.broadcastDevices()

	defer func() {
		h.removeDevice(id)
		_ = pc.Close()
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
	})

	pc.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		switch remote.Kind() {
		case webrtc.RTPCodecTypeVideo:
			h.handleVideoTrack(ctx, id, pc, remote)
		case webrtc.RTPCodecTypeAudio:
			h.handleAudioTrack(ctx, id, remote)
		}
	})

	send(signalMsg{Type: "status", Message: "ready", ID: id})

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
	if becameActive {
		h.restartVCamForActive()
		h.restartAudioForActive()
	}
	if isActive || becameActive {
		h.notifyControl(signalMsg{Type: "status", Message: "track-ready"})
	}

	_ = pc.WriteRTCP([]rtcp.Packet{
		&rtcp.PictureLossIndication{MediaSSRC: uint32(remote.SSRC())},
	})
	go pliLoop(ctx, pc, remote)

	h264 := &codecs.H264Packet{}
	for {
		pkt, _, readErr := remote.ReadRTP()
		if readErr != nil {
			return
		}

		raw, mErr := pkt.Marshal()
		if mErr == nil {
			if _, err := local.Write(raw); err != nil && !errors.Is(err, io.ErrClosedPipe) {
				return
			}
		}

		if !strings.Contains(strings.ToLower(mime), "h264") {
			continue
		}
		nal, uErr := h264.Unmarshal(pkt.Payload)
		if uErr != nil || len(nal) == 0 {
			continue
		}
		h.writeVCam(id, nal)
	}
}

func (h *Hub) handleAudioTrack(_ context.Context, id string, remote *webrtc.TrackRemote) {
	mime := remote.Codec().MimeType
	rate := remote.Codec().ClockRate
	if rate == 0 {
		rate = 48000
	}
	chans := remote.Codec().Channels
	if chans == 0 {
		chans = 2
	}
	log.Printf("phone %s audio: %s (%d Hz, %d ch)", id, mime, rate, chans)

	h.mu.Lock()
	d := h.devices[id]
	if d == nil {
		h.mu.Unlock()
		return
	}
	d.AudioRemote = remote
	d.AudioRate = rate
	d.AudioChans = chans
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
	if becameActive {
		h.restartVCamForActive()
		h.restartAudioForActive()
		if hasVideo {
			h.notifyControl(signalMsg{Type: "status", Message: "track-ready"})
		} else {
			h.notifyControl(signalMsg{Type: "status", Message: "audio-only"})
		}
	} else if isActive {
		h.restartAudioForActive()
	}

	for {
		pkt, _, readErr := remote.ReadRTP()
		if readErr != nil {
			return
		}
		h.writeAudio(id, pkt)
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
	if h.activeID != "" {
		if d := h.devices[h.activeID]; d != nil && d.HasAudio && !d.HasVideo {
			hasAudioOnly = true
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
	send(h.v4l2StatusMsg())
	send(signalMsg{Type: "devices", Devices: h.snapshotDevices()})
	switch {
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
	h.activeID = id
	hasVideo := d.HasVideo
	videoRemote := d.VideoRemote
	pc := d.PC
	name := d.Name
	h.mu.Unlock()

	log.Printf("active → %s (%s)", id, name)
	h.restartVCamForActive()
	h.restartAudioForActive()
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
		h.stopVCamLocked()
		h.stopAudioLocked()
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
	if wasActive {
		if next != "" {
			log.Printf("active failover → %s", next)
			h.restartVCamForActive()
			h.restartAudioForActive()
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

func (h *Hub) restartVCamForActive() {
	h.mu.Lock()
	h.stopVCamLocked()
	id := h.activeID
	devPath := h.v4l2Device
	var mime string
	if id != "" {
		if d := h.devices[id]; d != nil && d.HasVideo {
			mime = d.VideoMime
		}
	}
	h.mu.Unlock()

	h.refreshV4L2Status()
	h.notifyControl(h.v4l2StatusMsg())

	if id == "" || devPath == "" || mime == "" {
		return
	}
	if !h.v4l2DeviceAvailable() {
		return
	}
	if !strings.Contains(strings.ToLower(mime), "h264") {
		log.Printf("vcam: skip — active %s codec %s (need H264)", id, mime)
		return
	}

	cam, err := startVCam(devPath)
	if err != nil {
		log.Printf("vcam: %v", err)
		h.setV4L2Error(fmt.Sprintf("Virtual camera failed — %v", err))
		h.notifyControl(h.v4l2StatusMsg())
		return
	}

	h.mu.Lock()
	if h.activeID != id {
		h.mu.Unlock()
		cam.Close()
		return
	}
	h.vcam = cam
	h.v4l2OK = true
	h.v4l2Msg = ""
	h.mu.Unlock()
	h.notifyControl(h.v4l2StatusMsg())
}

func (h *Hub) refreshV4L2Status() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.v4l2Device == "" {
		h.v4l2OK = true
		h.v4l2Msg = ""
		return
	}
	if _, err := os.Stat(h.v4l2Device); err != nil {
		h.v4l2OK = false
		h.v4l2Msg = fmt.Sprintf("Virtual camera not available — %s is missing", h.v4l2Device)
		return
	}
	h.v4l2OK = true
	h.v4l2Msg = ""
}

func (h *Hub) setV4L2Error(msg string) {
	h.mu.Lock()
	h.v4l2OK = false
	h.v4l2Msg = msg
	h.mu.Unlock()
}

func (h *Hub) v4l2DeviceAvailable() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.v4l2Device == "" || h.v4l2OK
}

func (h *Hub) v4l2StatusMsg() signalMsg {
	h.mu.Lock()
	defer h.mu.Unlock()
	ok := h.v4l2OK
	msg := signalMsg{
		Type:      "v4l2",
		Available: &ok,
		Device:    h.v4l2Device,
		Message:   h.v4l2Msg,
	}
	if h.v4l2Device != "" && !h.v4l2OK {
		msg.Command = v4l2FixCommand(h.v4l2Device)
		if msg.Message == "" {
			msg.Message = fmt.Sprintf("Virtual camera not available — %s is missing", h.v4l2Device)
		}
	}
	return msg
}

func (h *Hub) restartAudioForActive() {
	h.mu.Lock()
	h.stopAudioLocked()
	id := h.activeID
	dest := h.audioDest
	var rate uint32
	var chans uint16
	hasAudio := false
	if id != "" {
		if d := h.devices[id]; d != nil && d.HasAudio {
			hasAudio = true
			rate = d.AudioRate
			chans = d.AudioChans
		}
	}
	h.mu.Unlock()

	if id == "" || dest == "" || !hasAudio {
		return
	}

	sink, err := startASink(dest, rate, chans)
	if err != nil {
		log.Printf("asink: %v", err)
		return
	}

	h.mu.Lock()
	if h.activeID != id {
		h.mu.Unlock()
		sink.Close()
		return
	}
	h.asink = sink
	h.mu.Unlock()
}

func (h *Hub) stopVCamLocked() {
	if h.vcam != nil {
		h.vcam.Close()
		h.vcam = nil
	}
}

func (h *Hub) stopAudioLocked() {
	if h.asink != nil {
		h.asink.Close()
		h.asink = nil
	}
}

func (h *Hub) writeVCam(deviceID string, nal []byte) {
	h.mu.Lock()
	if h.activeID != deviceID || h.vcam == nil {
		h.mu.Unlock()
		return
	}
	cam := h.vcam
	h.mu.Unlock()

	if err := cam.Write(nal); err != nil {
		log.Printf("vcam write: %v", err)
		h.mu.Lock()
		if h.vcam == cam {
			h.stopVCamLocked()
		}
		h.mu.Unlock()
	}
}

func (h *Hub) writeAudio(deviceID string, pkt *rtp.Packet) {
	h.mu.Lock()
	if h.activeID != deviceID || h.asink == nil {
		h.mu.Unlock()
		return
	}
	sink := h.asink
	h.mu.Unlock()

	if err := sink.WriteRTP(pkt); err != nil {
		log.Printf("asink write: %v", err)
		h.mu.Lock()
		if h.asink == sink {
			h.stopAudioLocked()
		}
		h.mu.Unlock()
	}
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
