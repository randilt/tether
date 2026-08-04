package main

import "io"

// videoSink is an ffmpeg output stage attached to a videoPipeline.
type videoSink interface {
	Name() string
	Close() error
	// WantPipe is true when this sink reads raw frames from an ffmpeg pipe fd.
	WantPipe() bool
	// OutputArgs is used when WantPipe is false (e.g. v4l2 device path).
	OutputArgs() []string
	// PipeArgs is used when WantPipe is true; fd is the ExtraFiles descriptor (3+).
	PipeArgs(fd int) []string
	// Consume reads raw video from r until EOF/error. Called in a goroutine.
	Consume(r io.Reader)
}
