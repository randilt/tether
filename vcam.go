package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
)

// vcam pipes Annex-B H264 into ffmpeg → v4l2loopback with low-latency flags.
type vcam struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	device string
	mu     sync.Mutex
	closed bool
}

func startVCam(device string) (*vcam, error) {
	if device == "" {
		return nil, fmt.Errorf("v4l2 device path is empty")
	}
	if _, err := os.Stat(device); err != nil {
		return nil, fmt.Errorf("%s not found — load v4l2loopback (see README): %w", device, err)
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, fmt.Errorf("ffmpeg not in PATH: %w", err)
	}

	// Decode H264 from stdin, emit raw frames to the loopback device.
	// Flags intentionally sacrifice robustness for glass-to-glass latency.
	cmd := exec.Command("ffmpeg",
		"-hide_banner",
		"-loglevel", "warning",
		"-fflags", "nobuffer",
		"-flags", "low_delay",
		"-probesize", "32",
		"-analyzeduration", "0",
		"-avioflags", "direct",
		"-fpsprobesize", "0",
		"-f", "h264",
		"-i", "pipe:0",
		"-an",
		"-c:v", "rawvideo",
		"-pix_fmt", "yuv420p",
		"-fps_mode", "passthrough",
		"-f", "v4l2",
		"-fflags", "flush_packets",
		device,
	)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = &ffmpegLog{prefix: "ffmpeg"}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}

	log.Printf("vcam: ffmpeg → %s (pid %d)", device, cmd.Process.Pid)
	return &vcam{cmd: cmd, stdin: stdin, device: device}, nil
}

func (v *vcam) Write(annexB []byte) error {
	if len(annexB) == 0 {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return io.ErrClosedPipe
	}
	_, err := v.stdin.Write(annexB)
	return err
}

func (v *vcam) Close() {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return
	}
	v.closed = true
	_ = v.stdin.Close()
	v.mu.Unlock()

	if v.cmd.Process != nil {
		_ = v.cmd.Process.Kill()
	}
	_ = v.cmd.Wait()
	log.Printf("vcam: stopped (%s)", v.device)
}

type ffmpegLog struct {
	prefix string
	buf    []byte
}

func (l *ffmpegLog) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	for {
		i := -1
		for j, b := range l.buf {
			if b == '\n' {
				i = j
				break
			}
		}
		if i < 0 {
			break
		}
		line := string(l.buf[:i])
		l.buf = l.buf[i+1:]
		if line != "" {
			log.Printf("%s: %s", l.prefix, line)
		}
	}
	return len(p), nil
}
