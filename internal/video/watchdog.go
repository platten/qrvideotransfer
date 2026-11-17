package video

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

// meter collects an instance's progress for the watchdog: a count that grows with progress, and the
// number of calls in which the instance is waiting on qrvt (a full output pipe, an empty input pipe,
// a turn to run), during which it is not stalled.
type meter struct {
	n       atomic.Int64
	reads   atomic.Int64 // bytes read from the input file (probing)
	waiting atomic.Int32
	// blocked counts readers of the instance's output that are themselves waiting on it, such as a
	// decoder waiting for a frame's timestamp; while one is, the instance's waiting does not count.
	blocked atomic.Int32
	extra   func() int64 // further progress, such as an output file's size; may be nil

	// For a time-bound error's breakdown, as meterStamp offsets (0: not yet): when run started the
	// job, when the instance was ready (a worker's ready message, or an in-process instance's turn),
	// its first read of the input, and the last progress the watchdog saw; and the input's read
	// calls and bytes. Kept with a few atomic operations; the text is built only for an error.
	start, readyAt, firstIn, progressAt atomic.Int64
	inCalls, inBytes                    atomic.Int64
	// The phases between readiness and the first read: when the instance's linear memory was
	// reserved and when it opened its input. Both apply to a worker and to an in-process instance.
	memAt, openAt atomic.Int64
	// loading is set when the instance starts loading (a worker process's start, an in-process
	// instance's turn). Until its linear memory is reserved (memAt), it is loading, not stalled:
	// the reservation precedes any guest code, and EngineLoadLimit bounds it instead.
	loading atomic.Bool
	// onReserve, when set, is called once, by the goroutine that reserved the memory, as soon as it
	// is reserved (a worker reports it at once). Set before the instance starts.
	onReserve func()
	// memPeak is the largest linear memory the instance reached, in bytes (timedAlloc).
	memPeak     atomic.Int64
	cancelledAt atomic.Int64 // when the watchdog cancelled the instance
	// For an in-process stop: when the guest reached a check and its module began to close, when its
	// memory was freed, and when the run call returned.
	checkAt, freedAt, runAt atomic.Int64
}

// ready records that the instance is ready to run.
func (m *meter) ready() { m.readyAt.CompareAndSwap(0, meterStamp(time.Now())) }

// reserved records that the instance's linear memory has been reserved.
func (m *meter) reserved() {
	if m.memAt.CompareAndSwap(0, meterStamp(time.Now())) && m.onReserve != nil {
		m.onReserve()
	}
}

// reserving reports whether the instance is loading and has not yet reserved its linear memory.
func (m *meter) reserving() bool { return m.loading.Load() && m.memAt.Load() == 0 }

// opened records that the instance opened its input file.
func (m *meter) opened() { m.openAt.CompareAndSwap(0, meterStamp(time.Now())) }

// input records a read of n bytes from the input file.
func (m *meter) input(n int) {
	if m.inCalls.Add(1) == 1 {
		m.firstIn.CompareAndSwap(0, meterStamp(time.Now()))
	}
	m.inBytes.Add(int64(n))
}

// breakdown says where an instance's time went, at now.
func (m *meter) breakdown(now time.Time) string {
	at := func(v *atomic.Int64) time.Time { return meterTime(v.Load()) }
	secs := func(d time.Duration) string { return fmt.Sprintf("%.1f s", d.Seconds()) }
	s := "wall " + secs(now.Sub(at(&m.start)))
	if m.readyAt.Load() == 0 {
		s += ", never ready"
	} else {
		s += ", start to ready " + secs(at(&m.readyAt).Sub(at(&m.start)))
	}
	if m.readyAt.Load() != 0 {
		for _, p := range []struct {
			name string
			at   *atomic.Int64
		}{{"memory reserved", &m.memAt}, {"input opened", &m.openAt}} {
			if p.at.Load() != 0 {
				s += ", ready to " + p.name + " " + secs(at(p.at).Sub(at(&m.readyAt)))
			}
		}
	}
	if m.firstIn.Load() == 0 || m.readyAt.Load() == 0 {
		s += ", no read after ready"
	} else {
		s += ", ready to first read " + secs(at(&m.firstIn).Sub(at(&m.readyAt)))
	}
	s += fmt.Sprintf(", %d reads, %d bytes", m.inCalls.Load(), m.inBytes.Load())
	if m.progressAt.Load() != 0 {
		s += ", " + secs(now.Sub(at(&m.progressAt))) + " since last progress"
	}
	if m.cancelledAt.Load() != 0 {
		s += ", stopped " + secs(now.Sub(at(&m.cancelledAt))) + " after its cancellation"
		if m.checkAt.Load() != 0 && m.freedAt.Load() != 0 && m.runAt.Load() != 0 {
			s += " (cancellation to guest check " + secs(at(&m.checkAt).Sub(at(&m.cancelledAt))) +
				", module close " + secs(at(&m.freedAt).Sub(at(&m.checkAt))) +
				", run return " + secs(at(&m.runAt).Sub(at(&m.freedAt))) + ")"
		}
	}
	return s + fmt.Sprintf(", %d of %d worker processes running, %d of %d in-process instances running",
		len(workerSlots), cap(workerSlots), len(slots), cap(slots))
}

// timeBound names the limits on time whose errors carry a breakdown.
var timeBound = map[string]bool{"EngineStallTimeout": true, "EngineProbeTime": true, "EngineLoadLimit": true}

// withBreakdown adds m's breakdown to err when it is a time-bound LimitError without one.
func withBreakdown(err error, m *meter) error {
	var le *LimitError
	if errors.As(err, &le) && timeBound[le.Limit] && le.Detail == "" {
		le.Detail = m.breakdown(time.Now())
	}
	return err
}

// progressLen is the size of a worker's progress message: bytes read counted as progress (probing),
// input bytes, input read calls, and nanoseconds from its readiness to its first read, to its linear
// memory being reserved and to its input being opened (-1: not yet), each a big-endian uint64.
const progressLen = 48

func (m *meter) value() int64 {
	v := m.n.Load() + m.reads.Load()
	if m.extra != nil {
		v += m.extra()
	}
	return v
}

// watchTick is how often the watchdog looks at the meter.
const watchTick = time.Second

// watch cancels the instance with the stall error when m shows no progress for timeout, or with
// check's error when it returns one, until done is closed. Time spent waiting, or loading until
// the instance's memory is reserved, is neither a stall nor active time.
func watch(m *meter, timeout time.Duration, check func(*meter, time.Duration) error, cancel context.CancelCauseFunc, done <-chan struct{}) {
	t := time.NewTicker(watchTick)
	defer t.Stop()
	last, since := m.value(), time.Now()
	var active time.Duration
	for {
		select {
		case <-done:
			return
		case now := <-t.C:
			waiting := m.waiting.Load() > 0 && m.blocked.Load() == 0 || m.reserving()
			if !waiting {
				active += watchTick
			}
			if check != nil {
				if err := check(m, active); err != nil {
					m.cancelledAt.Store(meterStamp(time.Now()))
					cancel(err)
					return
				}
			}
			if v := m.value(); v != last || waiting {
				if v != last {
					m.progressAt.Store(meterStamp(now))
				}
				last, since = v, now
			} else if at := m.memAt.Load(); m.loading.Load() && at > meterStamp(since) {
				since = meterTime(at) // the stall clock starts when the reservation completed
			}
			if now.Sub(since) >= timeout {
				m.cancelledAt.Store(meterStamp(time.Now()))
				cancel(stallError(timeout))
				return
			}
		}
	}
}

// meterBase is the fixed time from which the meter's times are offsets. time.Now carries a
// monotonic reading, so the offsets are measured on the monotonic clock, as the watchdog's timers
// are: a wall-clock step cannot move them (review of PR #42, F1; a wall-clock reading once put a
// cancellation microseconds before EngineLoadLimit on windows-2025).
var meterBase = time.Now()

// meterStamp returns t as a meter time: its monotonic offset from meterBase in nanoseconds, never
// 0, which means not yet.
func meterStamp(t time.Time) int64 {
	if d := int64(t.Sub(meterBase)); d != 0 {
		return d
	}
	return 1
}

// meterTime returns the time a meter time stands for, with a monotonic reading.
func meterTime(v int64) time.Time { return meterBase.Add(time.Duration(v)) }

// meterWriter counts bytes written through it, unless count is false; a blocked Write counts as
// waiting.
type meterWriter struct {
	w     io.Writer
	m     *meter
	count bool
}

func (w meterWriter) Write(b []byte) (int, error) {
	w.m.waiting.Add(1)
	n, err := w.w.Write(b)
	w.m.waiting.Add(-1)
	if w.count {
		w.m.n.Add(int64(n))
	}
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
