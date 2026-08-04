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
	Capability string
	State      string // connecting | live
	PC         *webrtc.PeerConnection
	Remote     *webrtc.TrackRemote
	Local      *webrtc.TrackLocalStaticRTP
	Mime       string
}

type Hub struct {
	api        *webrtc.API
	v4l2Device string
	pairCode   string

	mu          sync.Mutex
	devices     map[string]*device
	activeID    string
	vcam        *vcam
	controlPC   *webrtc.PeerConnection
	controlSend func(signalMsg)
	controlGen  int
}

func newHub(v4l2Device, pairCode string) (*Hub, error) {
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

	return &Hub{
		api:        api,
		v4l2Device: v4l2Device,
		pairCode:   pairCode,
		devices:    make(map[string]*device),
	}, nil
}

func (h *Hub) handleWS(w http.ResponseWriter, r *http.Request) {
	rRole := role(r.URL.Query().Get("role"))
	if rRole != rolePhone && rRole != roleControl && rRole != roleViewer {
		http.Error(w, "role must be phone or control", http.StatusBadRequest)
		return
	}
	if rRole == roleViewer {
		rRole = roleControl
	}

	if rRole == rolePhone && !h.validPairToken(r) {
		http.Error(w, "pairing code required", http.StatusForbidden)
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
		if err := h.serveControl(ctx, r, conn, send); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("control session: %v", err)
			send(signalMsg{Type: "error", Message: err.Error()})
		}
	}
}

func (h *Hub) validPairToken(r *http.Request) bool {
	t := r.URL.Query().Get("t")
	if t == "" {
		t = r.URL.Query().Get("token")
	}
	return normalizePairCode(t) == normalizePairCode(h.pairCode)
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
		ID:         id,
		Name:       name,
		Capability: "video",
		State:      "connecting",
		PC:         pc,
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
		if remote.Kind() != webrtc.RTPCodecTypeVideo {
			return
		}
		mime := remote.Codec().MimeType
		log.Printf("phone %s track: %s", id, mime)

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
		d.Remote = remote
		d.Local = local
		d.Mime = mime
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

func (h *Hub) serveControl(ctx context.Context, r *http.Request, conn *websocket.Conn, send func(signalMsg)) error {
	h.mu.Lock()
	h.controlGen++
	gen := h.controlGen
	h.controlSend = send
	hasActive := h.activeLocalLocked() != nil
	code := h.pairCode
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

	send(signalMsg{
		Type: "pair",
		Code: code,
		URL:  phonePairURL(r.Host, code),
	})
	send(signalMsg{Type: "devices", Devices: h.snapshotDevices()})
	if hasActive {
		send(signalMsg{Type: "status", Message: "track-ready"})
	} else {
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
	if !ok || d.State != "live" || d.Local == nil {
		h.mu.Unlock()
		return fmt.Errorf("device %s not available", id)
	}
	if h.activeID == id {
		h.mu.Unlock()
		h.broadcastDevices()
		return nil
	}
	h.activeID = id
	h.mu.Unlock()

	log.Printf("active → %s (%s)", id, d.Name)
	h.restartVCamForActive()
	h.broadcastDevices()
	h.notifyControl(signalMsg{Type: "status", Message: "track-ready"})

	h.mu.Lock()
	remote := d.Remote
	pc := d.PC
	h.mu.Unlock()
	if remote != nil && pc != nil {
		_ = pc.WriteRTCP([]rtcp.Packet{
			&rtcp.PictureLossIndication{MediaSSRC: uint32(remote.SSRC())},
		})
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
	if wasActive {
		h.activeID = ""
		h.stopVCamLocked()
		for _, other := range h.devices {
			if other.State == "live" && other.Local != nil {
				next = other.ID
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
			h.notifyControl(signalMsg{Type: "status", Message: "track-ready"})
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
		if d := h.devices[id]; d != nil {
			mime = d.Mime
		}
	}
	h.mu.Unlock()

	if id == "" || devPath == "" {
		return
	}
	if !strings.Contains(strings.ToLower(mime), "h264") {
		log.Printf("vcam: skip — active %s codec %s (need H264)", id, mime)
		return
	}

	cam, err := startVCam(devPath)
	if err != nil {
		log.Printf("vcam: %v", err)
		return
	}

	h.mu.Lock()
	if h.activeID != id {
		h.mu.Unlock()
		cam.Close()
		return
	}
	h.vcam = cam
	h.mu.Unlock()
}

func (h *Hub) stopVCamLocked() {
	if h.vcam != nil {
		h.vcam.Close()
		h.vcam = nil
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

func (h *Hub) activeLocalLocked() *webrtc.TrackLocalStaticRTP {
	if h.activeID == "" {
		return nil
	}
	d := h.devices[h.activeID]
	if d == nil {
		return nil
	}
	return d.Local
}

func (h *Hub) snapshotDevices() []deviceInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]deviceInfo, 0, len(h.devices))
	for _, d := range h.devices {
		out = append(out, deviceInfo{
			ID:         d.ID,
			Name:       d.Name,
			Capability: d.Capability,
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
