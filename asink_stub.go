//go:build !linux

package main

import (
	"fmt"

	"github.com/pion/rtp"
)

// Local Pulse/ALSA hear-back is Linux-only. On Windows/macOS use NDI audio in OBS.
type asink struct {
	closed bool
}

func startASink(dest string, sampleRate uint32, channels uint16) (*asink, error) {
	return nil, fmt.Errorf("local audio sink is Linux-only — use -ndi and receive audio in OBS/vMix")
}

func (a *asink) PID() int { return 0 }

func (a *asink) WriteRTP(pkt *rtp.Packet) error {
	return fmt.Errorf("audio sink unavailable on this OS")
}

func (a *asink) Close() {}
