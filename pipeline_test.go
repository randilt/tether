package main

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestPipelineCloseTerminatesProcess(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	p := &videoPipeline{cmd: cmd, stdin: stdin, deviceID: "test"}

	done := make(chan struct{})
	go func() {
		p.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close hung — process not terminated")
	}

	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("expected pid %d terminated after Close", pid)
	}
}

func TestStopDevicePipeLockedKillsBeforeNil(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid

	d := &device{ID: "d1", pipe: &videoPipeline{cmd: cmd, stdin: stdin, deviceID: "d1"}}
	h := &Hub{devices: map[string]*device{"d1": d}, activeID: "d1"}
	h.stopVCamLocked()
	if d.pipe != nil {
		t.Fatal("pipe pointer should be nil after stop")
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("old ffmpeg stand-in pid %d still alive after stopVCamLocked", pid)
	}
}

func TestPipelineCloseIdempotent(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &videoPipeline{cmd: cmd, stdin: stdin, deviceID: "test"}
	p.Close()
	p.Close()
}

func TestVideoMIMESupported(t *testing.T) {
	if !videoMIMESupported("video/H264") {
		t.Fatal("H264 should be supported")
	}
	if !videoMIMESupported("video/VP8") {
		t.Fatal("VP8 should be supported")
	}
	if videoMIMESupported("video/VP9") {
		t.Fatal("VP9 not wired yet")
	}
}

func TestNewVideoDepacketizer(t *testing.T) {
	d, err := newVideoDepacketizer("video/H264")
	if err != nil || d.FFmpegFormat() != "h264" {
		t.Fatalf("h264: %v %v", err, d)
	}
	d, err = newVideoDepacketizer("video/VP8")
	if err != nil || d.FFmpegFormat() != "ivf" {
		t.Fatalf("vp8: %v %v", err, d)
	}
}

func TestAnnexBHasNALType(t *testing.T) {
	// 00 00 00 01 | nal_type=7 (SPS)
	sps := []byte{0, 0, 0, 1, 0x67, 0x42}
	if !annexBHasNALType(sps, 7) {
		t.Fatal("expected SPS")
	}
	if annexBHasNALType(sps, 5) {
		t.Fatal("should not report IDR")
	}
	idr := []byte{0, 0, 1, 0x65, 0x88}
	if !annexBHasNALType(idr, 5) {
		t.Fatal("expected IDR")
	}
}
