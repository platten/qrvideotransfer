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
	extra   func() int64 // further progress, such as an output file's size; may be nil
	reads   atomic.Int64
	blocked atomic.Int32
}

func (m *meter) value() int64 {
	v := m.n.Load()
	if m.extra != nil {
		v += m.extra()
	}
	return v
}

// watchTick is how often the watchdog looks at the meter.
const watchTick = time.Second

// watch cancels the instance with the stall error when m shows no progress for timeout, until done
// is closed.
func watch(m *meter, timeout time.Duration, cancel context.CancelCauseFunc, done <-chan struct{}) {
	t := time.NewTicker(watchTick)
	defer t.Stop()
	last, since := m.value(), time.Now()
	for {
		select {
		case <-done:
			return
		case now := <-t.C:
			if v := m.value(); v != last || m.waiting.Load() > 0 {
				last, since = v, now
			} else if now.Sub(since) >= timeout {
				cancel(stallError)
				return
			}
		}
	}
}

// meterWriter counts bytes written through it; a blocked Write counts as waiting.
type meterWriter struct {
	w io.Writer
	m *meter
}

func (w meterWriter) Write(b []byte) (int, error) {
	w.m.waiting.Add(1)
	n, err := w.w.Write(b)
	w.m.waiting.Add(-1)
	w.m.n.Add(int64(n))
	return n, err
}

// meterReader counts bytes read through it; a blocked Read counts as waiting.
type meterReader struct {
	r io.Reader
	m *meter
}

func (r meterReader) Read(b []byte) (int, error) {
	r.m.waiting.Add(1)
	n, err := r.r.Read(b)
	r.m.waiting.Add(-1)
	r.m.n.Add(int64(n))
	return n, err
}
