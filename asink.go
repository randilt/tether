package main

import (
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4/pkg/media/oggwriter"
)

// asink pipes WebRTC Opus (via Ogg) into ffmpeg → local audio out.
// dest examples: "pulse:default", "alsa:default", "alsa:hw:Loopback,0,0", "" (disabled)
type asink struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	ogg    *oggwriter.OggWriter
	dest   string
	mu     sync.Mutex
	closed bool
}

func startASink(dest string, sampleRate uint32, channels uint16) (*asink, error) {
	if dest == "" {
		return nil, fmt.Errorf("audio sink disabled")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, fmt.Errorf("ffmpeg not in PATH: %w", err)
	}
	if sampleRate == 0 {
		sampleRate = 48000
	}
	if channels == 0 {
		channels = 2
	}

	format, device, err := parseAudioDest(dest)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command("ffmpeg",
		"-hide_banner",
		"-loglevel", "warning",
		"-fflags", "nobuffer",
		"-flags", "low_delay",
		"-probesize", "32",
		"-analyzeduration", "0",
		"-avioflags", "direct",
		"-f", "ogg",
		"-i", "pipe:0",
		"-vn",
		"-c:a", "pcm_s16le",
		"-f", format,
		device,
	)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = &ffmpegLog{prefix: "ffmpeg-audio"}

	ogg, err := oggwriter.NewWith(stdin, sampleRate, channels)
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("ogg writer: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = ogg.Close()
		_ = stdin.Close()
		return nil, fmt.Errorf("start ffmpeg audio: %w", err)
	}

	log.Printf("asink: ffmpeg → %s:%s (pid %d)", format, device, cmd.Process.Pid)
	return &asink{cmd: cmd, stdin: stdin, ogg: ogg, dest: dest}, nil
}

func (a *asink) PID() int {
	if a == nil || a.cmd == nil || a.cmd.Process == nil {
		return 0
	}
	return a.cmd.Process.Pid
}

func parseAudioDest(dest string) (format, device string, err error) {
	dest = strings.TrimSpace(dest)
	switch {
	case dest == "default" || dest == "pulse" || dest == "pulse:default":
		return "pulse", "default", nil
	case strings.HasPrefix(dest, "pulse:"):
		return "pulse", strings.TrimPrefix(dest, "pulse:"), nil
	case dest == "alsa" || dest == "alsa:default":
		return "alsa", "default", nil
	case strings.HasPrefix(dest, "alsa:"):
		return "alsa", strings.TrimPrefix(dest, "alsa:"), nil
	default:
		return "", "", fmt.Errorf("unknown audio dest %q (use pulse:default, alsa:default, alsa:hw:Loopback,0,0)", dest)
	}
}

func (a *asink) WriteRTP(pkt *rtp.Packet) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.ogg == nil {
		return io.ErrClosedPipe
	}
	return a.ogg.WriteRTP(pkt)
}

func (a *asink) Close() {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.closed = true
	if a.ogg != nil {
		_ = a.ogg.Close()
		a.ogg = nil
	}
	_ = a.stdin.Close()
	a.mu.Unlock()

	if a.cmd.Process != nil {
		_ = a.cmd.Process.Kill()
	}
	_ = a.cmd.Wait()
	log.Printf("asink: stopped (%s)", a.dest)
}
