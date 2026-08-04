//go:build linux

package main

import (
	"fmt"
	"io"
	"os"
)

// Fixed output geometry for v4l2loopback. Phone streams are often portrait
// (e.g. 1080x1920); without an explicit -s, ffmpeg's VIDIOC_G_FMT on a fresh
// loopback node fails with EINVAL and the whole pipeline dies.
const (
	v4l2OutW = 1280
	v4l2OutH = 720
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
		"-vf", fmt.Sprintf("scale=%d:%d", v4l2OutW, v4l2OutH),
		"-c:v", "rawvideo",
		"-pix_fmt", "yuv420p",
		"-s", fmt.Sprintf("%dx%d", v4l2OutW, v4l2OutH),
		"-r", "30",
		"-f", "v4l2",
		s.device,
	}
}

func (s *v4l2Sink) PipeArgs(fd int) []string { return nil }

func (s *v4l2Sink) Consume(r io.Reader) {}

func (s *v4l2Sink) Close() error { return nil }
