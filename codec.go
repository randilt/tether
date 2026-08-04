package main

import (
	"fmt"
	"log"
	"strings"
)

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
