package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// videoPipeline runs one low-latency ffmpeg decode per device, fanning out to sinks.
type videoPipeline struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	sinks    []videoSink
	deviceID string
	mime     string
	mu       sync.Mutex
	closed   bool
}

func startVideoPipeline(deviceID, mime string, sinks []videoSink) (*videoPipeline, error) {
	if len(sinks) == 0 {
		return nil, fmt.Errorf("no video sinks")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, fmt.Errorf("ffmpeg not in PATH: %w", err)
	}

	format := "h264"
	m := strings.ToLower(mime)
	switch {
	case strings.Contains(m, "vp8"):
		format = "ivf"
	case strings.Contains(m, "h264"):
		format = "h264"
	default:
		return nil, fmt.Errorf("pipeline: unsupported mime %q", mime)
	}

	args := []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-fflags", "nobuffer",
		"-flags", "low_delay",
		"-probesize", "32",
		"-analyzeduration", "0",
		"-avioflags", "direct",
		"-fpsprobesize", "0",
		"-f", format,
		"-i", "pipe:0",
	}

	var extraFiles []*os.File
	var pipeReaders []*os.File
	var pipeSinks []videoSink
	names := make([]string, 0, len(sinks))

	for _, s := range sinks {
		names = append(names, s.Name())
		if s.WantPipe() {
			r, w, err := os.Pipe()
			if err != nil {
				for _, f := range extraFiles {
					_ = f.Close()
				}
				for _, f := range pipeReaders {
					_ = f.Close()
				}
				return nil, err
			}
			fd := 3 + len(extraFiles) // ExtraFiles[0] → fd 3
			extraFiles = append(extraFiles, w)
			pipeReaders = append(pipeReaders, r)
			pipeSinks = append(pipeSinks, s)
			args = append(args, s.PipeArgs(fd)...)
			continue
		}
		args = append(args, s.OutputArgs()...)
	}

	cmd := exec.Command("ffmpeg", args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cleanupFiles(extraFiles, pipeReaders)
		return nil, err
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = &ffmpegLog{prefix: "ffmpeg"}
	cmd.ExtraFiles = extraFiles

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		cleanupFiles(extraFiles, pipeReaders)
		for _, s := range sinks {
			_ = s.Close()
		}
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}

	// Parent closes write ends so EOF propagates when ffmpeg exits.
	for _, w := range extraFiles {
		_ = w.Close()
	}

	for i, s := range pipeSinks {
		go s.Consume(pipeReaders[i])
	}

	log.Printf("pipeline %s: ffmpeg → %s (pid %d, %s)", deviceID, strings.Join(names, ", "), cmd.Process.Pid, format)
	return &videoPipeline{
		cmd:      cmd,
		stdin:    stdin,
		sinks:    sinks,
		deviceID: deviceID,
		mime:     mime,
	}, nil
}

func cleanupFiles(sets ...[]*os.File) {
	for _, set := range sets {
		for _, f := range set {
			_ = f.Close()
		}
	}
}

func (p *videoPipeline) PID() int {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *videoPipeline) Write(bitstream []byte) error {
	if len(bitstream) == 0 {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return io.ErrClosedPipe
	}
	_, err := p.stdin.Write(bitstream)
	return err
}

func (p *videoPipeline) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	_ = p.stdin.Close()
	sinks := p.sinks
	p.mu.Unlock()

	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	_ = p.cmd.Wait()
	for _, s := range sinks {
		_ = s.Close()
	}
	log.Printf("pipeline %s: stopped", p.deviceID)
}

// ffmpegLog is shared by video/audio ffmpeg subprocesses.
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
