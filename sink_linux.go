//go:build linux

package main

import (
	"fmt"
	"io"
	"os"
)

// v4l2Sink writes decoded raw frames to a v4l2loopback device (Linux).
type v4l2Sink struct {
	device string
}

func newV4L2Sink(device string) (videoSink, error) {
	if device == "" {
		return nil, fmt.Errorf("v4l2 device path is empty")
	}
	if _, err := os.Stat(device); err != nil {
		return nil, fmt.Errorf("%s not found — load v4l2loopback (see README): %w", device, err)
	}
	return &v4l2Sink{device: device}, nil
}

func (s *v4l2Sink) Name() string { return "v4l2:" + s.device }

func (s *v4l2Sink) WantPipe() bool { return false }

func (s *v4l2Sink) OutputArgs() []string {
	return []string{
		"-an",
		"-c:v", "rawvideo",
		"-pix_fmt", "yuv420p",
		"-fps_mode", "passthrough",
		"-f", "v4l2",
		"-fflags", "flush_packets",
		s.device,
	}
}

func (s *v4l2Sink) PipeArgs(fd int) []string { return nil }

func (s *v4l2Sink) Consume(r io.Reader) {}

func (s *v4l2Sink) Close() error { return nil }
