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

// videoPipeline runs one ffmpeg decode per device, fanning out to sinks.
// ffmpeg is started lazily on the first clean H264 access unit (SPS+PPS+IDR)
// so probing never begins on an empty pipe or mid-GOP garbage.
type videoPipeline struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	sinks    []videoSink
	deviceID string
	mime     string
	format   string
	mu       sync.Mutex
	closed   bool
	started  bool

	h264      bool
	keyed     bool
	paramSets []byte
	hasSPS    bool
	hasPPS    bool
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
	isH264 := false
	switch {
	case strings.Contains(m, "vp8"):
		format = "ivf"
	case strings.Contains(m, "h264"):
		format = "h264"
		isH264 = true
	default:
		return nil, fmt.Errorf("pipeline: unsupported mime %q", mime)
	}

	names := make([]string, 0, len(sinks))
	for _, s := range sinks {
		names = append(names, s.Name())
	}
	log.Printf("pipeline %s: ready → %s (%s, waiting for keyframe)", deviceID, strings.Join(names, ", "), format)

	return &videoPipeline{
		sinks:    sinks,
		deviceID: deviceID,
		mime:     mime,
		format:   format,
		h264:     isH264,
	}, nil
}

func (p *videoPipeline) startLocked() error {
	if p.started {
		return nil
	}
	if p.closed {
		return io.ErrClosedPipe
	}

	// Once we have a primer, keep probe tiny — bitstream starts at a clean AU.
	args := []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-fflags", "nobuffer+discardcorrupt",
		"-flags", "low_delay",
		"-probesize", "32",
		"-analyzeduration", "0",
		"-f", p.format,
		"-i", "pipe:0",
	}

	var extraFiles []*os.File
	var pipeReaders []*os.File
	var pipeSinks []videoSink

	for _, s := range p.sinks {
		if s.WantPipe() {
			r, w, err := os.Pipe()
			if err != nil {
				cleanupFiles(extraFiles, pipeReaders)
				return err
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
		return err
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = &ffmpegLog{prefix: "ffmpeg"}
	cmd.ExtraFiles = extraFiles

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		cleanupFiles(extraFiles, pipeReaders)
		for _, s := range p.sinks {
			_ = s.Close()
		}
		return fmt.Errorf("start ffmpeg: %w", err)
	}

	for _, w := range extraFiles {
		_ = w.Close()
	}
	for i, s := range pipeSinks {
		go s.Consume(pipeReaders[i])
	}

	p.cmd = cmd
	p.stdin = stdin
	p.started = true
	log.Printf("pipeline %s: ffmpeg started (pid %d)", p.deviceID, cmd.Process.Pid)
	return nil
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

	if p.h264 && !p.keyed {
		hasSPS := annexBHasNALType(bitstream, 7)
		hasPPS := annexBHasNALType(bitstream, 8)
		hasIDR := annexBHasNALType(bitstream, 5)
		if hasSPS {
			p.hasSPS = true
		}
		if hasPPS {
			p.hasPPS = true
		}
		// Keep SPS/PPS (and any AU that carries them). Drop other pre-key slices.
		if hasSPS || hasPPS {
			p.paramSets = append(p.paramSets, bitstream...)
		}
		if !hasIDR || !p.hasSPS || !p.hasPPS {
			return nil
		}

		p.keyed = true
		if err := p.startLocked(); err != nil {
			return err
		}
		log.Printf("pipeline %s: locked onto H264 IDR — feeding decoder", p.deviceID)

		if hasSPS || hasPPS {
			// This AU already landed in paramSets.
			_, err := p.stdin.Write(p.paramSets)
			p.paramSets = nil
			return err
		}
		if len(p.paramSets) > 0 {
			if _, err := p.stdin.Write(p.paramSets); err != nil {
				return err
			}
			p.paramSets = nil
		}
		_, err := p.stdin.Write(bitstream)
		return err
	}

	if !p.started {
		if err := p.startLocked(); err != nil {
			return err
		}
	}
	_, err := p.stdin.Write(bitstream)
	return err
}

// annexBHasNALType reports whether Annex-B data contains a NAL of the given type
// (e.g. 5=IDR, 7=SPS, 8=PPS).
func annexBHasNALType(b []byte, nalType byte) bool {
	i := 0
	for i < len(b) {
		if i+3 >= len(b) {
			break
		}
		if b[i] != 0 || b[i+1] != 0 {
			i++
			continue
		}
		var hdrIdx int
		if b[i+2] == 1 {
			hdrIdx = i + 3
		} else if i+4 < len(b) && b[i+2] == 0 && b[i+3] == 1 {
			hdrIdx = i + 4
		} else {
			i++
			continue
		}
		if hdrIdx >= len(b) {
			break
		}
		if b[hdrIdx]&0x1f == nalType {
			return true
		}
		i = hdrIdx + 1
	}
	return false
}

func (p *videoPipeline) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	stdin := p.stdin
	cmd := p.cmd
	sinks := p.sinks
	p.mu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
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
