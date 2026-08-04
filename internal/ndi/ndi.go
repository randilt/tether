package ndi

import (
	"fmt"
	"sync"
	"unsafe"
)

// FourCC UYVY — matches ffmpeg -pix_fmt uyvy422.
var FourCCUYVY = [4]byte{'U', 'Y', 'V', 'Y'}

const (
	frameProgressive int32 = 1
	timecodeSynth    int64 = 0x7fffffffffffffff // NDIlib_send_timecode_synthesize
)

type sendCreateSettings struct {
	name       *byte
	groups     *byte
	clockVideo bool
	clockAudio bool
}

type videoFrameV2 struct {
	Xres, Yres         int32
	FourCC             [4]byte
	FrameRateN         int32
	FrameRateD         int32
	PictureAspectRatio float32
	FrameFormatType    int32
	Timecode           int64
	Data               *byte
	LineStride         int32
	Metadata           *byte
	Timestamp          int64
}

// Sender is one NDI network source.
type Sender struct {
	instance uintptr
	name     string
}

var (
	mu       sync.Mutex
	loaded   bool
	loadErr  error
	initOnce sync.Once
)

// Available reports whether the NDI runtime loaded. Safe to call anytime.
func Available() bool {
	_ = Init("")
	mu.Lock()
	defer mu.Unlock()
	return loaded && loadErr == nil
}

// InitError is the last Init failure (runtime missing, etc.).
func InitError() error {
	_ = Init("")
	mu.Lock()
	defer mu.Unlock()
	return loadErr
}

// Init loads the NDI shared library. libraryPath may be empty (auto-discover).
func Init(libraryPath string) error {
	initOnce.Do(func() {
		loadErr = loadLibrary(libraryPath)
		if loadErr != nil {
			return
		}
		if !ndilibInitialize() {
			loadErr = fmt.Errorf("NDIlib_initialize returned false")
			return
		}
		loaded = true
	})
	mu.Lock()
	defer mu.Unlock()
	return loadErr
}

// RedistURL is where users download the NDI runtime.
const RedistURL = "https://ndi.video/for-developers/ndi-sdk/"

// NewSender creates a discoverable NDI source. groups may be empty.
func NewSender(name, groups string) (*Sender, error) {
	if err := Init(""); err != nil {
		return nil, err
	}
	settings := &sendCreateSettings{
		name:       cString(name),
		clockVideo: true,
		clockAudio: false,
	}
	if groups != "" {
		settings.groups = cString(groups)
	}
	inst := ndilibSendCreate(uintptr(unsafe.Pointer(settings)))
	if inst == 0 {
		return nil, fmt.Errorf("NDIlib_send_create_v2 failed for %q", name)
	}
	return &Sender{instance: inst, name: name}, nil
}

func (s *Sender) Name() string {
	if s == nil {
		return ""
	}
	return s.name
}

// SendUYVY sends one progressive UYVY frame. data must remain valid for the call.
func (s *Sender) SendUYVY(data []byte, width, height, fpsN, fpsD int) {
	if s == nil || s.instance == 0 || len(data) == 0 {
		return
	}
	if fpsN <= 0 {
		fpsN = 30
	}
	if fpsD <= 0 {
		fpsD = 1
	}
	frame := videoFrameV2{
		Xres:            int32(width),
		Yres:            int32(height),
		FourCC:          FourCCUYVY,
		FrameRateN:      int32(fpsN),
		FrameRateD:      int32(fpsD),
		FrameFormatType: frameProgressive,
		Timecode:        timecodeSynth,
		Data:            &data[0],
		LineStride:      int32(width * 2),
	}
	ndilibSendVideo(s.instance, uintptr(unsafe.Pointer(&frame)))
}

func (s *Sender) Destroy() {
	if s == nil || s.instance == 0 {
		return
	}
	ndilibSendDestroy(s.instance)
	s.instance = 0
}

func cString(name string) *byte {
	b := make([]byte, len(name)+1)
	copy(b, name)
	return &b[0]
}
