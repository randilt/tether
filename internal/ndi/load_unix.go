//go:build unix

package ndi

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/ebitengine/purego"
)

var (
	ndilibInitialize func() bool
	ndilibSendCreate func(settings uintptr) uintptr
	ndilibSendDestroy func(instance uintptr)
	ndilibSendVideo  func(instance uintptr, frame uintptr)
)

func loadLibrary(libraryPath string) error {
	if libraryPath == "" {
		libraryPath = discoverLibrary()
	}
	if libraryPath == "" {
		return fmt.Errorf("NDI runtime not found — install from %s", RedistURL)
	}
	lib, err := purego.Dlopen(libraryPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return fmt.Errorf("open NDI library %s: %w (install from %s)", libraryPath, err, RedistURL)
	}
	purego.RegisterLibFunc(&ndilibInitialize, lib, "NDIlib_initialize")
	purego.RegisterLibFunc(&ndilibSendCreate, lib, "NDIlib_send_create_v2")
	purego.RegisterLibFunc(&ndilibSendDestroy, lib, "NDIlib_send_destroy")
	purego.RegisterLibFunc(&ndilibSendVideo, lib, "NDIlib_send_send_video_v2")
	return nil
}

func discoverLibrary() string {
	var candidates []string
	if p := os.Getenv("NDI_RUNTIME_DIR_V6"); p != "" {
		candidates = append(candidates,
			filepath.Join(p, "libndi.so.6"),
			filepath.Join(p, "libndi.so"),
			filepath.Join(p, "libndi.dylib"),
		)
	}
	if p := os.Getenv("NDI_RUNTIME_DIR_V5"); p != "" {
		candidates = append(candidates,
			filepath.Join(p, "libndi.so.5"),
			filepath.Join(p, "libndi.so"),
			filepath.Join(p, "libndi.dylib"),
		)
	}
	switch runtime.GOOS {
	case "darwin":
		candidates = append(candidates,
			"/usr/local/lib/libndi.dylib",
			"/Library/NDI SDK for Apple/lib/macOS/libndi.dylib",
		)
	case "linux":
		candidates = append(candidates,
			"libndi.so.6",
			"libndi.so.5",
			"libndi.so",
			"/usr/local/lib/libndi.so.6",
			"/usr/lib/libndi.so.6",
			"/usr/lib/x86_64-linux-gnu/libndi.so.6",
		)
	}
	for _, c := range candidates {
		if c == "libndi.so.6" || c == "libndi.so.5" || c == "libndi.so" {
			// Let the dynamic linker search the path.
			return c
		}
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	// Last resort: bare soname for Dlopen path search.
	if runtime.GOOS == "linux" {
		return "libndi.so"
	}
	if runtime.GOOS == "darwin" {
		return "/usr/local/lib/libndi.dylib"
	}
	return ""
}
