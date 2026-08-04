package main

import (
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/randilt/tether/internal/ndi"
)

// NDIConfig controls optional NDI multi-source publishing.
type NDIConfig struct {
	Enabled bool
	Prefix  string
	Groups  string
	Width   int
	Height  int
}

func parseNDISize(s string) (w, h int, err error) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(s)), "x")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("ndi-size must be WxH (got %q)", s)
	}
	w, err = strconv.Atoi(parts[0])
	if err != nil || w <= 0 {
		return 0, 0, fmt.Errorf("ndi-size width: %q", parts[0])
	}
	h, err = strconv.Atoi(parts[1])
	if err != nil || h <= 0 {
		return 0, 0, fmt.Errorf("ndi-size height: %q", parts[1])
	}
	return w, h, nil
}

func (h *Hub) applyNDIConfig(cfg NDIConfig) {
	h.ndiEnabled = cfg.Enabled
	h.ndiPrefix = cfg.Prefix
	if h.ndiPrefix == "" {
		h.ndiPrefix = "TETHER"
	}
	h.ndiGroups = cfg.Groups
	h.ndiWidth = cfg.Width
	h.ndiHeight = cfg.Height
	if h.ndiWidth <= 0 {
		h.ndiWidth = 1280
	}
	if h.ndiHeight <= 0 {
		h.ndiHeight = 720
	}
	if !cfg.Enabled {
		h.ndiOK = true
		h.ndiMsg = ""
		return
	}
	if err := ndi.Init(""); err != nil {
		h.ndiOK = false
		h.ndiMsg = err.Error()
		return
	}
	h.ndiOK = true
	h.ndiMsg = ""
}

func (h *Hub) setNDIError(msg string) {
	h.mu.Lock()
	h.ndiOK = false
	h.ndiMsg = msg
	h.mu.Unlock()
}

func (h *Hub) ndiStatusMsg() signalMsg {
	h.mu.Lock()
	defer h.mu.Unlock()
	enabled := h.ndiEnabled
	ok := h.ndiOK
	msg := signalMsg{
		Type:    "ndi",
		Enabled: &enabled,
		Message: h.ndiMsg,
	}
	if enabled {
		avail := ok
		msg.Available = &avail
		if !ok && msg.Message == "" {
			msg.Message = "NDI runtime missing — install from " + ndi.RedistURL
		}
		msg.URL = "https://ndi.video/"
	}
	return msg
}

func (h *Hub) preferredCodec() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.videoCodec == "" {
		return "h264"
	}
	return h.videoCodec
}

func (h *Hub) setPreferredCodec(codec string) error {
	c := strings.ToLower(strings.TrimSpace(codec))
	switch c {
	case "h264", "vp8":
	default:
		return fmt.Errorf("codec must be h264 or vp8")
	}
	h.mu.Lock()
	h.videoCodec = c
	h.mu.Unlock()
	log.Printf("preferred phone video codec → %s (applies on next connect/resume)", c)
	return nil
}

func ndiSourceName(prefix, deviceName, id string) string {
	return fmt.Sprintf("%s (%s %s)", prefix, deviceName, id)
}
