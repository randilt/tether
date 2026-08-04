package main

import (
	"context"
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
	rolePhone  role = "phone"
	roleViewer role = "viewer"
)

type signalMsg struct {
	Type       string `json:"type"`
	SDP        string `json:"sdp,omitempty"`
	Candidate  string `json:"candidate,omitempty"`
	SDPMid     string `json:"sdpMid,omitempty"`
	SDPMLineIx *int   `json:"sdpMLineIndex,omitempty"`
	Message    string `json:"message,omitempty"`
}

type Hub struct {
	api        *webrtc.API
	v4l2Device string

	mu         sync.Mutex
	localTrack *webrtc.TrackLocalStaticRTP
	phonePC    *webrtc.PeerConnection
	viewerPC   *webrtc.PeerConnection
	viewerSend func(signalMsg) // set while viewer WS is live
}

func newHub(v4l2Device string) (*Hub, error) {
	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterDefaultCodecs(); err != nil {
		return nil, err
	}

	registry := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(mediaEngine, registry); err != nil {
		return nil, err
	}

	// Skip docker/VM bridges so ICE prefers real LAN NICs (e.g. 192.168.x).
	se := webrtc.SettingEngine{}
	se.SetInterfaceFilter(func(name string) bool {
		n := strings.ToLower(name)
		switch {
		case n == "docker0", n == "lo":
			return n == "lo" // keep loopback for same-machine /control
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

	return &Hub{api: api, v4l2Device: v4l2Device}, nil
}

func (h *Hub) handleWS(w http.ResponseWriter, r *http.Request) {
	rRole := role(r.URL.Query().Get("role"))
	if rRole != rolePhone && rRole != roleViewer {
		http.Error(w, "role must be phone or viewer", http.StatusBadRequest)
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
	send := func(msg signalMsg) {
		b, err := json.Marshal(msg)
		if err != nil {
			return
		}
		writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := conn.Write(writeCtx, websocket.MessageText, b); err != nil {
			log.Printf("ws write (%s): %v", rRole, err)
		}
	}

	switch rRole {
	case rolePhone:
		if err := h.servePhone(ctx, conn, send); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("phone session: %v", err)
			send(signalMsg{Type: "error", Message: err.Error()})
		}
	case roleViewer:
		if err := h.serveViewer(ctx, conn, send); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("viewer session: %v", err)
			send(signalMsg{Type: "error", Message: err.Error()})
		}
	}
}

func (h *Hub) servePhone(ctx context.Context, conn *websocket.Conn, send func(signalMsg)) error {
	pc, err := h.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return err
	}

	h.mu.Lock()
	if h.phonePC != nil {
		_ = h.phonePC.Close()
	}
	h.phonePC = pc
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		if h.phonePC == pc {
			h.phonePC = nil
			h.localTrack = nil
		}
		h.mu.Unlock()
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
		log.Printf("phone peer state: %s", s)
	})

	pc.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if remote.Kind() != webrtc.RTPCodecTypeVideo {
			return
		}
		mime := remote.Codec().MimeType
		log.Printf("phone track: %s %s", mime, remote.ID())

		local, err := webrtc.NewTrackLocalStaticRTP(
			remote.Codec().RTPCodecCapability,
			"video",
			"tether",
		)
		if err != nil {
			log.Printf("local track: %v", err)
			return
		}

		h.mu.Lock()
		h.localTrack = local
		viewerSend := h.viewerSend
		device := h.v4l2Device
		h.mu.Unlock()

		if viewerSend != nil {
			viewerSend(signalMsg{Type: "status", Message: "track-ready"})
		}

		// Keyframes ASAP for ffmpeg (needs SPS/PPS) and late viewers.
		_ = pc.WriteRTCP([]rtcp.Packet{
			&rtcp.PictureLossIndication{MediaSSRC: uint32(remote.SSRC())},
		})
		go pliLoop(ctx, pc, remote)

		var cam *vcam
		if device != "" && strings.Contains(strings.ToLower(mime), "h264") {
			cam, err = startVCam(device)
			if err != nil {
				log.Printf("vcam: %v", err)
			} else {
				defer cam.Close()
			}
		} else if device != "" {
			log.Printf("vcam: skip — need H264 for pipe, got %s", mime)
		}

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

			if cam == nil {
				continue
			}
			nal, uErr := h264.Unmarshal(pkt.Payload)
			if uErr != nil || len(nal) == 0 {
				continue
			}
			if err := cam.Write(nal); err != nil {
				log.Printf("vcam write: %v", err)
				cam.Close()
				cam = nil
			}
		}
	})

	send(signalMsg{Type: "status", Message: "ready"})

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

func (h *Hub) serveViewer(ctx context.Context, conn *websocket.Conn, send func(signalMsg)) error {
	pc, err := h.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return err
	}

	h.mu.Lock()
	if h.viewerPC != nil {
		_ = h.viewerPC.Close()
	}
	h.viewerPC = pc
	h.viewerSend = send
	track := h.localTrack
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		if h.viewerPC == pc {
			h.viewerPC = nil
			h.viewerSend = nil
		}
		h.mu.Unlock()
		_ = pc.Close()
	}()

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
		log.Printf("viewer peer state: %s", s)
	})

	if track != nil {
		if err := h.attachTrack(pc, track); err != nil {
			return err
		}
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
		if err := h.handleViewerSignal(pc, msg, send); err != nil {
			return err
		}
	}
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

func (h *Hub) handleViewerSignal(pc *webrtc.PeerConnection, msg signalMsg, send func(signalMsg)) error {
	switch msg.Type {
	case "offer":
		h.mu.Lock()
		track := h.localTrack
		h.mu.Unlock()

		if track == nil {
			send(signalMsg{Type: "status", Message: "waiting-for-phone"})
			return nil
		}

		// Attach track if this PC does not have a sender yet.
		if len(pc.GetSenders()) == 0 {
			if err := h.attachTrack(pc, track); err != nil {
				return err
			}
		}

		if err := pc.SetRemoteDescription(webrtc.SessionDescription{
			Type: webrtc.SDPTypeOffer,
			SDP:  msg.SDP,
		}); err != nil {
			return fmt.Errorf("viewer set remote offer: %w", err)
		}
		answer, err := pc.CreateAnswer(nil)
		if err != nil {
			return fmt.Errorf("viewer create answer: %w", err)
		}
		if err := pc.SetLocalDescription(answer); err != nil {
			return fmt.Errorf("viewer set local answer: %w", err)
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
			log.Printf("viewer add candidate: %v", err)
		}
	}
	return nil
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
