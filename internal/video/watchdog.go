package video

import (
	"context"
	"io"
	"sync/atomic"
	"time"
)

// meter collects an instance's progress for the watchdog: a count that grows with progress, and the
// number of calls in which the instance is waiting on qrvt (a full output pipe, an empty input pipe),
// during which it is not stalled.
type meter struct {
	n       atomic.Int64
	waiting atomic.Int32
	extra   func() int64
}

func (m *meter) value() int64 { panic("not implemented") }

// watchTick is how often the watchdog looks at the meter.
const watchTick = time.Second

// watch cancels the instance with the stall error when m shows no progress for timeout, until done
// is closed.
func watch(m *meter, timeout time.Duration, cancel context.CancelCauseFunc, done <-chan struct{}) {
	panic("not implemented")
}

// meterWriter counts bytes written through it; a blocked Write counts as waiting.
type meterWriter struct {
	w io.Writer
	m *meter
}

func (w meterWriter) Write(b []byte) (int, error) { panic("not implemented") }

// meterReader counts bytes read through it; a blocked Read counts as waiting.
type meterReader struct {
	r io.Reader
	m *meter
}

func (r meterReader) Read(b []byte) (int, error) { panic("not implemented") }
