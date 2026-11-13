// Package video reads and writes video files through three backends: ffmpeg embedded as
// WebAssembly (default), an external ffmpeg process, and a raw-frame file format for tests. It is
// the only package that may import os/exec (docs/SECURITY.md section 9.6).
package video

import (
	"context"
	"time"
)

// Frame is one decoded or to-be-encoded picture in Y'CbCr 4:2:0.
type Frame struct {
	W, H      int
	Y, Cb, Cr []uint16 // 8- or 10-bit samples; Cb and Cr nil for brightness-only
	Bits      uint8
	Index     int
	PTS       time.Duration
}

// ClipInfo describes a video file.
type ClipInfo struct {
	Codec         string // for example "h264" or "hevc"
	Width, Height int
	BitDepth      int
	FPSNum        int // frame rate as a fraction; 0/0 if unknown
	FPSDen        int
	Frames        int64 // 0 if unknown
	Duration      time.Duration
	Rotation      int    // degrees from the container's display matrix
	Transfer      string // transfer function, for example "bt709" or "arib-std-b67"
}

// EncodeConfig configures an [Encoder].
type EncodeConfig struct {
	Width, Height int
	FPS           int
	Quantiser     int
	BitDepth      int // 8, or 10 for the experimental HDR output
}

// DecodeConfig configures a [Decoder].
type DecodeConfig struct {
	Start, Length  time.Duration // time slice; Length 0 means to the end
	BrightnessOnly bool          // decode the Y plane only
	BitDepth       int           // 8, or 10 when the source is 10-bit
}

// Encoder writes frames to a video file.
type Encoder interface {
	// WriteFrame encodes one frame.
	WriteFrame(*Frame) error
	// Close finishes and closes the file.
	Close() error
}

// Decoder reads frames from a video file. Next returns io.EOF at the end.
type Decoder interface {
	// Info describes the file being read.
	Info() ClipInfo
	// Next reads the next frame into the given frame, reusing its buffers.
	Next(*Frame) error
	// Close releases the decoder.
	Close() error
}

// Backend opens video files for reading and writing.
type Backend interface {
	// Probe describes a video file without decoding it.
	Probe(ctx context.Context, path string) (ClipInfo, error)
	// NewEncoder creates a video file at path.
	NewEncoder(ctx context.Context, path string, c EncodeConfig) (Encoder, error)
	// NewDecoder opens a video file for reading.
	NewDecoder(ctx context.Context, path string, c DecodeConfig) (Decoder, error)
}
