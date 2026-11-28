package pipeline

import (
	"context"
	"errors"
	"time"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/video"
)

// ScanConfig are the inputs of [Scan].
type ScanConfig struct {
	Recordings []string
	Backend    video.Backend
	Workers    int
	Progress   func(Event)
}

// Capture is what [Scan] reports of one capture.
type Capture struct {
	Recording      int
	Index          int
	PTS            time.Duration
	Finders        bool
	Headers        []frame.Header
	Reject         frame.HeaderError
	Located        bool
	Phases         []Phase
	Cells, Clean   int
	PixelsPerCell  float64
	PaletteClipped float64
}

// Phase is one code frame's soft bits in a capture.
type Phase struct {
	Frame uint32
	Soft  []int8
}

// ScanResult describes a finished scan.
type ScanResult struct {
	Captures int64
	Probes   []video.ClipInfo
	Warnings []Warning
}

// Scan is not built yet.
func Scan(ctx context.Context, c ScanConfig, sink func(*Capture) (stop bool, err error)) (*ScanResult, error) {
	return nil, errors.New("pipeline: Scan not built yet")
}

// scanCapture runs the vision chain on one capture for Scan; tests replace it.
var scanCapture = (*captureWorker).scan

func (w *captureWorker) scan(f *video.Frame, index int) captureResult { return captureResult{index: index} }
