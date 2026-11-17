package video

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Reserving an instance's 512 MiB happens before any guest code runs, so nothing in a recording can
// slow it: it belongs to loading the engine, which EngineLoadLimit bounds, not to the stall watchdog,
// whose clock starts when the reservation completes. On macos-26-intel two reservations once took
// 30 s under the full test load and both instances were stopped as stalls (CI run 43693218308).

// delayReserve makes the reservation of m's instance take d, until the test ends. Tests that use it
// are not parallel: it replaces a package hook.
func delayReserve(t *testing.T, m *meter, d time.Duration) {
	t.Helper()
	var slow atomic.Pointer[meter]
	slow.Store(m)
	beforeReserve = func(x *meter) {
		if x == slow.Load() {
			time.Sleep(d)
		}
	}
	t.Cleanup(func() { beforeReserve = func(*meter) {} })
}

// stallFromReservation checks that m's instance was stopped as a stall, with the stall counted from
// its reservation: at least the limit after it. (Waiting counts from the last tick at which the
// instance waited, up to a tick early; the reservation is counted from its own time.)
func stallFromReservation(t *testing.T, what string, err error, m *meter, limit time.Duration) {
	t.Helper()
	var le *LimitError
	if !errors.As(err, &le) || le.Limit != "EngineStallTimeout" {
		t.Fatalf("%s: got %v", what, err)
	}
	mem, cancelled := m.memAt.Load(), m.cancelledAt.Load()
	if mem == 0 || cancelled == 0 {
		t.Fatalf("%s: stopped before its memory was reserved: %v", what, err)
	}
	if d := time.Duration(cancelled - mem); d < limit {
		t.Errorf("%s: stopped %v after its reservation, limit %v: the reservation counted as a stall (%v)", what, d, limit, err)
	}
}

// TestReservedAtMonotonic: the stall clock's start at the reservation carries a monotonic clock
// reading, so the stall is measured on the monotonic clock, as it is from any other start: measured
// on the wall clock (the reservation's Unix time alone), a clock step during the 30 s moves the stop
// (TestWatchdog stopped after 29.0 s locally).
func TestReservedAtMonotonic(t *testing.T) {
	t.Parallel()
	now := time.Now()
	got := meterTime(meterStamp(now.Add(-300 * time.Millisecond)))
	if !strings.Contains(got.String(), " m=") {
		t.Errorf("%v has no monotonic clock reading", got)
	}
	if d := now.Sub(got); d != 300*time.Millisecond {
		t.Errorf("reservation %v before the tick, want 300ms", d)
	}
}

// TestReservationNotStall: an in-process instance whose reservation takes longer than the stall
// limit (5 s against 2 s) is not stopped as a stall while it reserves; once it has reserved, the
// watchdog stops it as usual. Not parallel: it replaces a package hook.
func TestReservationNotStall(t *testing.T) {
	e := engineFor(t)
	const limit = 2 * time.Second
	m := &meter{}
	delayReserve(t, m, 5*time.Second)
	err := run(testCtx(t, time.Minute), e, job{prog: "ffmpeg", progress: progOutput, timeout: limit, meter: m,
		args: func(string, string) []string { return spin }})
	stallFromReservation(t, "in process", err, m, limit)
}

// TestReservationLoadLimit: an in-process instance whose reservation outlasts EngineLoadLimit
// (shortened to 2 s; the reservation takes 5 s) is stopped with that limit's error, cancelled when
// the limit runs out after it got its turn, and not stopped as a stall (1 s) before then. Not
// parallel: it replaces a package hook and shortens the limit.
func TestReservationLoadLimit(t *testing.T) {
	loadLimit = 2 * time.Second
	t.Cleanup(func() { loadLimit = EngineLoadLimit })
	e := engineFor(t)
	m := &meter{}
	delayReserve(t, m, 5*time.Second)
	err := run(testCtx(t, time.Minute), e, job{prog: "ffmpeg", progress: progOutput, timeout: time.Second, meter: m,
		args: func(string, string) []string { return spin }})
	var le *LimitError
	if !errors.As(err, &le) || le.Limit != "EngineLoadLimit" || !strings.Contains(err.Error(), "wall ") {
		t.Fatalf("got %v", err)
	}
	// readyAt and cancelledAt are monotonic offsets from meterBase, like the load timer, so the gap
	// cannot come in under the limit (it did by microseconds with wall-clock readings on
	// windows-2025, CI run 43903238445).
	if d := time.Duration(m.cancelledAt.Load() - m.readyAt.Load()); d < loadLimit || d > loadLimit+time.Second {
		t.Errorf("cancelled %v after its turn, limit %v", d, loadLimit)
	}
}

// TestWorkerReservationNotStall: a worker that reports its reservation 5 s after it is ready, then
// hangs, is not stopped as a stall (2 s) while it reserves, and is stopped as one counted from the
// reservation. Not parallel: the timing is the worker's own.
func TestWorkerReservationNotStall(t *testing.T) {
	const limit = 2 * time.Second
	p, ps := testWorkers(t, "slowreserve")
	m := &meter{}
	err := run(testCtx(t, time.Minute), workerRunner{p}, job{prog: "ffmpeg", args: func(string, string) []string { return nil },
		timeout: limit, meter: m})
	stallFromReservation(t, "worker", err, m, limit)
	gone(t, ps.all(), 5*time.Second)
}

// TestWorkerReservedSpin: a worker whose guest reserves the whole 512 MiB and then spins without
// calling the host is stopped as a stall (3 s) soon after, not held to EngineLoadLimit. The worker
// must report its reservation before the guest runs: once it spins, the garbage collection that the
// reservation can start never reaches its stop-the-world, and no other goroutine of the worker runs
// (SECURITY.md T1), its periodic progress report included (CI run 43704037510).
func TestWorkerReservedSpin(t *testing.T) {
	t.Parallel()
	const limit = 3 * time.Second
	p, ps := testWorkers(t)
	m := &meter{}
	err := run(testCtx(t, 2*time.Minute), workerRunner{p}, job{prog: "ffmpeg", module: "spinbig",
		args: func(string, string) []string { return nil }, timeout: limit, meter: m})
	stallFromReservation(t, "reserved spin", err, m, limit)
	if d := time.Duration(m.cancelledAt.Load() - m.memAt.Load()); d > limit+5*time.Second {
		t.Errorf("stopped %v after its reservation", d)
	}
	gone(t, ps.all(), 5*time.Second)
}

// TestWorkerFutureReservation: a worker's reservation time is its word, so one that claims a time a
// day ahead does not hold off the stall watchdog: the claim is taken as the time it arrived, and the
// worker is stopped as a stall (2 s) within the limit plus a few seconds.
func TestWorkerFutureReservation(t *testing.T) {
	t.Parallel()
	const limit = 2 * time.Second
	p, ps := testWorkers(t, "futurereserve")
	err := run(testCtx(t, 30*time.Second), workerRunner{p}, job{prog: "ffmpeg", args: func(string, string) []string { return nil },
		timeout: limit})
	var le *LimitError
	if !errors.As(err, &le) || le.Limit != "EngineStallTimeout" {
		t.Fatalf("got %v", err)
	}
	if el := ps.since(); el > limit+5*time.Second {
		t.Errorf("stopped after %v", el)
	}
	gone(t, ps.all(), 5*time.Second)
}

// TestWorkerReservationLoadLimit: a worker that is ready but never reports its reservation is killed
// when EngineLoadLimit (shortened to 3 s) runs out, with that limit's error and no process left, and
// is not stopped as a stall (1 s) before then. Not parallel: it shortens the limit.
func TestWorkerReservationLoadLimit(t *testing.T) {
	loadLimit = 3 * time.Second
	t.Cleanup(func() { loadLimit = EngineLoadLimit })
	p, ps := testWorkers(t, "readyhang")
	err := run(testCtx(t, time.Minute), workerRunner{p}, job{prog: "ffmpeg", args: func(string, string) []string { return nil },
		timeout: time.Second})
	var le *LimitError
	if !errors.As(err, &le) || le.Limit != "EngineLoadLimit" || strings.Contains(err.Error(), "never ready") {
		t.Fatalf("got %v", err)
	}
	if el := ps.since(); el < loadLimit || el > loadLimit+10*time.Second {
		t.Errorf("stopped after %v", el)
	}
	gone(t, ps.all(), 5*time.Second)
}
