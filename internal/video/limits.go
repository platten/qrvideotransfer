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
	// cancelled: no complete, timestamped output frame when decoding, no input read when probing, no
	// growth of the output file when copying a stream for the HEVC fast path, no input frame taken
	// when encoding.
	EngineStallTimeout = 30 * time.Second

	// EngineProbeTime bounds a probe's running time, not counting time spent waiting for an instance
	// slot. Probes of the test clips and of the S1 and S2 recordings (up to 2.6 GB, 4K H.264 and
	// HEVC, MP4, MOV, Matroska and MPEG-TS) took at most 1.2 s with close-on-cancel on.
	EngineProbeTime = 15 * time.Second
	// MaxProbeReads bounds the bytes a probe reads from its input: at most probeReadsPerByte times
	// the file's size plus MaxProbeReads (three times ffprobe's default probe size of 5 MB). The
	// same probes read at most 1.07 times the file's size (an MPEG-TS file).
	MaxProbeReads = 16 << 20

	// MaxDecodedPixelsPerInputByte bounds the work a file can demand: decoded luma samples over the
	// file's size in bytes. It is checked from the probe before decoding and again while decoding.
	MaxDecodedPixelsPerInputByte = 10_000

	// MaxCaptureLongSide bounds the longer side of a picture accepted from a video file, whatever its
	// orientation: with MaxCaptureShortSide, 8K UHD, landscape or portrait-coded (some cameras code
	// portrait recordings as 4320 x 7680 instead of tagging a rotation).
	MaxCaptureLongSide = 7680
	// MaxCaptureShortSide bounds the shorter side of a picture accepted from a video file.
	MaxCaptureShortSide = 4320

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

// probeReadsPerByte is the part of the probe's read bound proportional to the file's size.
const probeReadsPerByte = 4

// stallError is the cancellation cause the watchdog uses after a stall of d.
func stallError(d time.Duration) *LimitError {
	return &LimitError{Limit: "EngineStallTimeout", Value: d.Seconds(), Max: d.Seconds(),
		Hint: "the video engine made no progress for that many seconds and was stopped"}
}

// probeLimits returns the probe's bounds for a file of size bytes, for the watchdog's check: at most
// probeReadsPerByte x size + MaxProbeReads bytes read, and maxTime running (EngineProbeTime).
func probeLimits(size int64, maxTime time.Duration) func(*meter, time.Duration) error {
	maxReads := probeReadsPerByte*size + MaxProbeReads
	return func(m *meter, active time.Duration) error {
		if r := m.reads.Load(); r > maxReads {
			return &LimitError{Limit: "MaxProbeReads", Value: float64(r), Max: float64(maxReads),
				Hint: "the video engine reread the file far more than probing needs and was stopped"}
		}
		if active >= maxTime {
			return &LimitError{Limit: "EngineProbeTime", Value: active.Seconds(), Max: maxTime.Seconds(),
				Hint: "probing the file took far longer than a video file needs and was stopped"}
		}
		return nil
	}
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
	long, short := max(w, h), min(w, h)
	if long > MaxCaptureLongSide {
		return &LimitError{Limit: "MaxCaptureLongSide", Value: float64(long), Max: MaxCaptureLongSide}
	}
	if short > MaxCaptureShortSide {
		return &LimitError{Limit: "MaxCaptureShortSide", Value: float64(short), Max: MaxCaptureShortSide}
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
