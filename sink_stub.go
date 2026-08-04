//go:build !linux

package main

import "fmt"

func newV4L2Sink(device string) (videoSink, error) {
	return nil, fmt.Errorf("v4l2loopback is Linux-only — use -ndi with NDI Tools / OBS Virtual Camera")
}
