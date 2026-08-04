package main

import (
	"fmt"
	"io"
	"log"
	"sync"

	"github.com/randilt/tether/internal/ndi"
)

// ndiSink consumes uyvy422 frames from an ffmpeg pipe and publishes an NDI source.
type ndiSink struct {
	sender *ndi.Sender
	name   string
	width  int
	height int
	mu     sync.Mutex
	closed bool
}

func newNDISink(sourceName, groups string, width, height int) (*ndiSink, error) {
	if width <= 0 || height <= 0 {
		width, height = 1280, 720
	}
	sender, err := ndi.NewSender(sourceName, groups)
	if err != nil {
		return nil, err
	}
	return &ndiSink{
		sender: sender,
		name:   sourceName,
		width:  width,
		height: height,
	}, nil
}

func (s *ndiSink) Name() string { return "ndi:" + s.name }

func (s *ndiSink) WantPipe() bool { return true }

func (s *ndiSink) PipeArgs(fd int) []string {
	return []string{
		"-an",
		"-vf", fmt.Sprintf("scale=%d:%d", s.width, s.height),
		"-c:v", "rawvideo",
		"-pix_fmt", "uyvy422",
		"-f", "rawvideo",
		fmt.Sprintf("pipe:%d", fd),
	}
}

func (s *ndiSink) OutputArgs() []string {
	// Not used for pipe sinks; satisfy type switch fallbacks.
	return s.PipeArgs(1)
}

func (s *ndiSink) Consume(r io.Reader) {
	frameSize := s.width * s.height * 2
	buf := make([]byte, frameSize)
	for {
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return
		}
		_, err := io.ReadFull(r, buf)
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed || s.sender == nil {
			s.mu.Unlock()
			return
		}
		frame := make([]byte, frameSize)
		copy(frame, buf)
		sender := s.sender
		w, h := s.width, s.height
		s.mu.Unlock()
		sender.SendUYVY(frame, w, h, 30, 1)
	}
}

func (s *ndiSink) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	sender := s.sender
	s.sender = nil
	s.mu.Unlock()
	if sender != nil {
		sender.Destroy()
	}
	log.Printf("ndi: stopped source %q", s.name)
	return nil
}
