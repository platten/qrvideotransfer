package qrvt

import (
	"github.com/platten/qrvideotransfer/internal/pipeline"
	"github.com/platten/qrvideotransfer/internal/video"
)

// testBackends maps backend names that only tests register to their backends; nil in a program.
var testBackends map[string]video.Backend

// convertEvent turns a pipeline event into the root package's.
func convertEvent(e pipeline.Event) Event { return Event{} }

// convertError turns the pipeline's and payload's errors into the typed errors of this package.
func convertError(err error) error { return err }
