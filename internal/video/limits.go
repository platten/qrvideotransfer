package video

import (
	"errors"
	"time"
)

// Limits of docs/SECURITY.md section 5. None of them can be raised by a flag.
const (
	EngineStallTimeout = 30 * time.Second

	MaxDecodedPixelsPerInputByte = 10_000

	MaxCaptureWidth = 8192

	MaxCaptureHeight = 8192

	EngineMemoryLimit = 512 << 20
)

// Sentinel errors. Test with errors.Is.
var (
	ErrInvalidVideo = errors.New("video: not a readable video file")

	ErrUnsupported = errors.New("video: unsupported")

	ErrOddRun = errors.New("video: an elementary-stream run must have an even number of frames")

	ErrFrame = errors.New("video: frame does not match the encoder")
)

// LimitError reports that a video exceeds one of the limits above. Limit is the constant's name.
type LimitError struct {
	Limit      string
	Value, Max float64
	Hint       string
}

// Error implements the error interface.
func (e *LimitError) Error() string { panic("not implemented") }

func num(v float64) string { panic("not implemented") }

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
func checkWork(pixels float64, size int64) error { panic("not implemented") }

// checkSize applies the capture size limit.
func checkSize(w, h int) error { panic("not implemented") }

// sanitise makes text that came from a video file or an engine safe to put in an error message:
// control characters become spaces and the result is at most n bytes.
func sanitise(s string, n int) string { panic("not implemented") }
