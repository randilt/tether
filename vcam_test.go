package main

import (
	"io"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestVCamCloseTerminatesProcess(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	v := &vcam{cmd: cmd, stdin: stdin, device: "test-device"}

	done := make(chan error, 1)
	go func() {
		v.Close()
		done <- nil
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close hung — process not terminated")
	}

	// Signal(0) probes liveness; dead pid should fail.
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("expected pid %d terminated after Close", pid)
	}
}

func TestStopVCamLockedKillsBeforeNil(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid

	h := &Hub{vcam: &vcam{cmd: cmd, stdin: stdin, device: "test-device"}}
	h.stopVCamLocked()
	if h.vcam != nil {
		t.Fatal("vcam pointer should be nil after stop")
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("old ffmpeg stand-in pid %d still alive after stopVCamLocked", pid)
	}
}

func TestVCamCloseIdempotent(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	v := &vcam{cmd: cmd, stdin: stdin, device: "test"}
	v.Close()
	v.Close() // must not panic
	_, _ = io.WriteString(stdin, "x") // closed pipe ok to ignore
}
