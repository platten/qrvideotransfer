package pipeline

import (
	"context"
	"errors"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/video"
)

// InspectConfig are the inputs of [Inspect].
type InspectConfig struct {
	Path     string
	Backend  video.Backend
	Workers  int
	Progress func(Event)
}

// InspectResult is what [Inspect] read.
type InspectResult struct {
	Probe      video.ClipInfo
	Header     frame.Header
	HeaderRead bool
	Descriptor *frame.Descriptor
	Stats      Stats
	Warnings   []Warning
}

// Inspect is not built yet.
func Inspect(ctx context.Context, c InspectConfig) (*InspectResult, error) {
	return nil, errors.New("pipeline: Inspect not built yet")
}
