package main

import (
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/pion/rtp/codecs"
)

// videoDepacketizer turns RTP payloads into a bitstream ffmpeg can ingest.
type videoDepacketizer interface {
	// Unmarshal returns bytes to write to the pipeline stdin (may be empty if incomplete).
	Unmarshal(payload []byte) ([]byte, error)
	// FFmpegFormat is the -f value for stdin (h264 | ivf).
	FFmpegFormat() string
}

func videoMIMESupported(mime string) bool {
	m := strings.ToLower(mime)
	return strings.Contains(m, "h264") || strings.Contains(m, "vp8")
}

func newVideoDepacketizer(mime string) (videoDepacketizer, error) {
	m := strings.ToLower(mime)
	switch {
	case strings.Contains(m, "h264"):
		return &h264Depack{pkt: &codecs.H264Packet{}}, nil
	case strings.Contains(m, "vp8"):
		return &vp8IVFDepack{pkt: &codecs.VP8Packet{}}, nil
	default:
		return nil, fmt.Errorf("unsupported video codec %q", mime)
	}
}

type h264Depack struct {
	pkt *codecs.H264Packet
}

func (d *h264Depack) FFmpegFormat() string { return "h264" }

func (d *h264Depack) Unmarshal(payload []byte) ([]byte, error) {
	return d.pkt.Unmarshal(payload)
}

// vp8IVFDepack wraps VP8 access units in IVF so ffmpeg can read `-f ivf`.
type vp8IVFDepack struct {
	pkt        *codecs.VP8Packet
	headerDone bool
	frameNum   uint64
}

func (d *vp8IVFDepack) FFmpegFormat() string { return "ivf" }

func (d *vp8IVFDepack) Unmarshal(payload []byte) ([]byte, error) {
	frame, err := d.pkt.Unmarshal(payload)
	if err != nil || len(frame) == 0 {
		return frame, err
	}
	out := make([]byte, 0, 32+12+len(frame))
	if !d.headerDone {
		out = append(out, ivfFileHeader(1280, 720)...)
		d.headerDone = true
	}
	out = append(out, ivfFrameHeader(len(frame), d.frameNum)...)
	out = append(out, frame...)
	d.frameNum++
	return out, nil
}

func ivfFileHeader(w, h uint16) []byte {
	b := make([]byte, 32)
	copy(b[0:4], "DKIF")
	binary.LittleEndian.PutUint16(b[4:6], 0)  // version
	binary.LittleEndian.PutUint16(b[6:8], 32) // header size
	copy(b[8:12], "VP80")
	binary.LittleEndian.PutUint16(b[12:14], w)
	binary.LittleEndian.PutUint16(b[14:16], h)
	binary.LittleEndian.PutUint32(b[16:20], 30) // timebase den
	binary.LittleEndian.PutUint32(b[20:24], 1)  // timebase num
	binary.LittleEndian.PutUint32(b[24:28], 0)  // frame count
	binary.LittleEndian.PutUint32(b[28:32], 0)  // unused
	return b
}

func ivfFrameHeader(size int, pts uint64) []byte {
	b := make([]byte, 12)
	binary.LittleEndian.PutUint32(b[0:4], uint32(size))
	binary.LittleEndian.PutUint64(b[4:12], pts)
	return b
}
