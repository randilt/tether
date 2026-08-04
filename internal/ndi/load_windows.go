//go:build windows

package ndi

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

var (
	ndiDLL *syscall.LazyDLL

	procInitialize *syscall.LazyProc
	procSendCreate *syscall.LazyProc
	procSendDestroy *syscall.LazyProc
	procSendVideo  *syscall.LazyProc
)

func loadLibrary(libraryPath string) error {
	if libraryPath == "" {
		libraryPath = discoverLibrary()
	}
	if libraryPath == "" {
		return fmt.Errorf("NDI runtime not found — install from %s", RedistURL)
	}
	ndiDLL = syscall.NewLazyDLL(libraryPath)
	if err := ndiDLL.Load(); err != nil {
		return fmt.Errorf("open NDI library %s: %w (install from %s)", libraryPath, err, RedistURL)
	}
	procInitialize = ndiDLL.NewProc("NDIlib_initialize")
	procSendCreate = ndiDLL.NewProc("NDIlib_send_create_v2")
	procSendDestroy = ndiDLL.NewProc("NDIlib_send_destroy")
	procSendVideo = ndiDLL.NewProc("NDIlib_send_send_video_v2")
	return nil
}

func discoverLibrary() string {
	var candidates []string
	if p := os.Getenv("NDI_RUNTIME_DIR_V6"); p != "" {
		candidates = append(candidates, filepath.Join(p, "Processing.NDI.Lib.x64.dll"))
	}
	if p := os.Getenv("NDI_RUNTIME_DIR_V5"); p != "" {
		candidates = append(candidates, filepath.Join(p, "Processing.NDI.Lib.x64.dll"))
	}
	candidates = append(candidates,
		`C:\Program Files\NDI\NDI 6 Runtime\v6\Processing.NDI.Lib.x64.dll`,
		`C:\Program Files\NDI\NDI 5 Runtime\v5\Processing.NDI.Lib.x64.dll`,
		"Processing.NDI.Lib.x64.dll",
	)
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return "Processing.NDI.Lib.x64.dll"
}

func ndilibInitialize() bool {
	r, _, _ := procInitialize.Call()
	return r != 0
}

func ndilibSendCreate(settings uintptr) uintptr {
	r, _, _ := procSendCreate.Call(settings)
	return r
}

func ndilibSendDestroy(instance uintptr) {
	procSendDestroy.Call(instance)
}

func ndilibSendVideo(instance uintptr, frame uintptr) {
	procSendVideo.Call(instance, frame)
}
