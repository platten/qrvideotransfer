package video

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBreakdown: a time-bound error carries where the instance's time went: wall time, start to
// ready, ready to first read, reads and bytes, time since the last progress and the instances
// running; the text is sanitised, and other limits get none.
func TestBreakdown(t *testing.T) {
	t.Parallel()
	now := time.Now()
	m := &meter{}
	m.start.Store(now.Add(-63 * time.Second).UnixNano())
	m.readyAt.Store(now.Add(-62 * time.Second).UnixNano())
	m.memAt.Store(now.Add(-61700 * time.Millisecond).UnixNano())
	m.openAt.Store(now.Add(-61950 * time.Millisecond).UnixNano())
	m.firstIn.Store(now.Add(-61900 * time.Millisecond).UnixNano())
	m.inCalls.Store(12)
	m.inBytes.Store(1234567)
	m.progressAt.Store(now.Add(-30 * time.Second).UnixNano())
	m.cancelledAt.Store(now.Add(-200 * time.Millisecond).UnixNano())
	b := m.breakdown(now)
	if !strings.Contains(b, "stopped 0.2 s after its cancellation") {
		t.Errorf("breakdown %q lacks the stop latency", b)
	}
	for _, want := range []string{"wall 63.0 s", "start to ready 1.0 s", "ready to first read 0.1 s",
		"ready to memory reserved 0.3 s", "ready to input opened 0.1 s",
		"12 reads, 1234567 bytes", "30.0 s since last progress", "worker processes running", "in-process instances running"} {
		if !strings.Contains(b, want) {
			t.Errorf("breakdown %q lacks %q", b, want)
		}
	}
	if b := (&meter{}).breakdown(now); !strings.Contains(b, "never ready") || !strings.Contains(b, "no read") {
		t.Errorf("an instance that never started: %q", b)
	}

	err := withBreakdown(stallError(time.Second), m)
	var le *LimitError
	if !errors.As(err, &le) || !strings.Contains(err.Error(), "since last progress") || !printable(err.Error()) {
		t.Errorf("stall error: %v", err)
	}
	le.Detail = "\x1b[31mforged\x9b"
	if s := le.Error(); !printable(s) {
		t.Errorf("unsanitised detail: %q", s)
	}
	if err := withBreakdown(&LimitError{Limit: "MaxProbeReads"}, m); strings.Contains(err.Error(), "wall") {
		t.Errorf("a read bound got a time breakdown: %v", err)
	}
	if err := withBreakdown(context.Canceled, m); !errors.Is(err, context.Canceled) {
		t.Errorf("another error: %v", err)
	}
}

// TestBreakdownOnTimeBounds: the probe's time bound, the stall watchdog and the engine-load limit,
// as run reports them, all carry the breakdown.
func TestBreakdownOnTimeBounds(t *testing.T) {
	t.Parallel()
	slow := fakeRunner(func(ctx context.Context, j *job, m *meter) (int, error) {
		for ctx.Err() == nil {
			m.reads.Add(1)
			time.Sleep(100 * time.Millisecond)
		}
		return 0, nil
	})
	err := run(t.Context(), slow, job{check: probeLimits(100, time.Second)})
	if !strings.Contains(err.Error(), "EngineProbeTime") || !strings.Contains(err.Error(), "wall ") {
		t.Errorf("probe time: %v", err)
	}
	// An in-process instance's stop is timed in three parts after its cancellation: until the guest
	// reached a check, the module's close, and the return from the run call.
	stop := spin1(&spinning.inProcess)
	err = run(t.Context(), engineFor(t), job{prog: "ffmpeg", timeout: time.Second, progress: progOutput,
		args: func(string, string) []string { return spin }})
	stop()
	for _, want := range []string{"cancellation to guest check", "module close", "run return"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("in-process stall %v lacks %q", err, want)
		}
	}
	stuck := fakeRunner(func(ctx context.Context, j *job, m *meter) (int, error) { <-ctx.Done(); return 0, nil })
	err = run(t.Context(), stuck, job{timeout: time.Second})
	if !strings.Contains(err.Error(), "EngineStallTimeout") || !strings.Contains(err.Error(), "since last progress") {
		t.Errorf("stall: %v", err)
	}
}

// ctxDoneLate is a context already cancelled whose cancellation has not yet reached the contexts
// derived from it (the context package propagates a foreign context's cancellation from another
// goroutine; here it never arrives).
type ctxDoneLate struct{ context.Context }

func (ctxDoneLate) Err() error { return context.Canceled }

// TestConcatReportsCancel: when Concat's context is done, Concat reports the cancellation, not the
// engine's error from its closed input, whatever order the two arrive in (TestConcatCancel once got
// "dimensions not set" on ubuntu-24.04-arm).
func TestConcatReportsCancel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	run := filepath.Join(dir, "run.h264")
	if err := os.WriteFile(run, []byte{0, 0, 0, 1}, 0o600); err != nil {
		t.Fatal(err)
	}
	failing := fakeRunner(func(context.Context, *job, *meter) (int, error) { return 1, nil }) // the engine's EOF error
	b := &ff{r: failing}
	err := b.Concat(ctxDoneLate{context.Background()}, []string{run}, filepath.Join(dir, "out.mp4"), 12)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v", err)
	}
}
