package video

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Limits of docs/SECURITY.md section 5. None of them can be raised by a flag.
const (
	// EngineStallTimeout is how long an engine instance may go without progress before it is
	// cancelled: no output frame when decoding, no input read when probing, no growth of the output
	// file when copying a stream for the HEVC fast path, no input frame taken when encoding.
	EngineStallTimeout = 30 * time.Second

	// EngineProbeTime bounds a probe's running time, not counting time spent waiting for an instance
	// slot.
	EngineProbeTime = 15 * time.Second
	// MaxProbeReads bounds the bytes a probe reads from its input beyond four times the file's size.
	MaxProbeReads = 16 << 20

	// MaxDecodedPixelsPerInputByte bounds the work a file can demand: decoded luma samples over the
	// file's size in bytes. It is checked from the probe before decoding and again while decoding.
	MaxDecodedPixelsPerInputByte = 10_000

	// MaxCaptureWidth bounds the picture width accepted from a video file.
	MaxCaptureWidth = 8192
	// MaxCaptureHeight bounds the picture height accepted from a video file.
	MaxCaptureHeight = 8192

	// EngineMemoryLimit is each engine instance's linear memory, reserved up front; it is also the
	// instance's ceiling.
	EngineMemoryLimit = 512 << 20
)

// Sentinel errors. Test with errors.Is.
var (
	// ErrInvalidVideo reports a file that is not a readable video: empty, truncated, of an unknown
	// container or structurally corrupt.
	ErrInvalidVideo = errors.New("video: not a readable video file")
	// ErrUnsupported reports a valid video that qrvt does not read or write, such as a 12-bit or a
	// 4:2:2 source, or an option this backend or build lacks.
	ErrUnsupported = errors.New("video: unsupported")
	// ErrOddRun reports an elementary-stream run with an odd number of frames (DESIGN.md section 12:
	// consecutive runs must alternate idr_pic_id, so each run has an even length).
	ErrOddRun = errors.New("video: an elementary-stream run must have an even number of frames")
	// ErrFrame reports a frame passed to an encoder that does not match its configuration.
	ErrFrame = errors.New("video: frame does not match the encoder")
)

// LimitError reports that a video exceeds one of the limits above. Limit is the constant's name.
type LimitError struct {
	Limit      string
	Value, Max float64
	Hint       string // what to do about it; may be empty
}

// Error implements the error interface.
func (e *LimitError) Error() string {
	s := fmt.Sprintf("video: %s exceeded: %s, limit %s", e.Limit, num(e.Value), num(e.Max))
	if e.Hint != "" {
		s += "; " + e.Hint
	}
	return s
}

func num(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%.1f", v)
}

// stallError is the cancellation cause the watchdog uses.
var stallError = &LimitError{
	Limit: "EngineStallTimeout", Value: EngineStallTimeout.Seconds(), Max: EngineStallTimeout.Seconds(),
	Hint: "the video engine made no progress for that many seconds and was stopped",
}

// ratioHint is the advice given with MaxDecodedPixelsPerInputByte.
const ratioHint = "the file demands far more decoding work than its size suggests; " +
	"for a legitimate recording, trim it to the part that shows the code"

// checkWork applies MaxDecodedPixelsPerInputByte to pixels decoded (or promised) from a file of
// size bytes.
func checkWork(pixels float64, size int64) error {
	if size <= 0 {
		return fmt.Errorf("%w: empty file", ErrInvalidVideo)
	}
	if r := pixels / float64(size); r > MaxDecodedPixelsPerInputByte {
		return &LimitError{Limit: "MaxDecodedPixelsPerInputByte", Value: r, Max: MaxDecodedPixelsPerInputByte, Hint: ratioHint}
	}
	return nil
}

// checkSize applies the capture size limit.
func checkSize(w, h int) error {
	if w <= 0 || h <= 0 {
		return fmt.Errorf("%w: picture size %dx%d", ErrInvalidVideo, w, h)
	}
	if w > MaxCaptureWidth {
		return &LimitError{Limit: "MaxCaptureWidth", Value: float64(w), Max: MaxCaptureWidth}
	}
	if h > MaxCaptureHeight {
		return &LimitError{Limit: "MaxCaptureHeight", Value: float64(h), Max: MaxCaptureHeight}
	}
	return nil
}

// sanitise makes text that came from a video file or an engine safe to put in an error message:
// control characters become spaces and the result is at most n bytes.
func sanitise(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return s
}
