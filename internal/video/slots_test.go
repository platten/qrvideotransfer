package video

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"
)

// TestWatchdogUnderLoad: more spinning instances than Ps are started at once, each with a 2 s
// watchdog; every one must be stopped by it. With the cap, at most EngineInstances() run at a time,
// an instance waiting for its turn is not stalled, and every one is stopped promptly. That is
// asserted per instance, from its own times, not as one total that also counts the turns: the
// watchdog must cancel it at most 5 s after the stall limit from when it got its turn (the stall is
// counted from the last tick at which it was waiting, so up to a tick earlier), and it must return at
// most 5 s after that cancellation. Without the cap (DESIGN.md section 12) every one must still be
// stopped by the watchdog, but the times are only logged: on a 16-thread development machine they
// were stopped on time, while on the macos-26-intel CI runner five took 16.8 s against a 2 s limit
// (up to 8 times late), which is why the cap is a real defence.
// Not parallel: it replaces the slots.
func TestWatchdogUnderLoad(t *testing.T) {
	e := engineFor(t)
	n := runtime.GOMAXPROCS(0) + 1
	capped := EngineInstances()
	if capped < 1 || capped > max(1, runtime.GOMAXPROCS(0)-1) {
		t.Fatalf("EngineInstances() = %d with GOMAXPROCS %d", capped, runtime.GOMAXPROCS(0))
	}
	const limit, margin = 2 * time.Second, 5 * time.Second
	for _, k := range []int{capped, n} {
		old := slots
		slots = make(chan struct{}, k)
		start := time.Now()
		var wg sync.WaitGroup
		errs := make([]error, n)
		ms := make([]*meter, n)
		ends := make([]time.Time, n)
		for i := range n {
			ms[i] = &meter{}
			wg.Go(func() {
				errs[i] = run(t.Context(), e, job{prog: "ffmpeg", progress: progOutput, timeout: limit,
					args: func(string, string) []string { return spin }, meter: ms[i]})
				ends[i] = time.Now()
			})
		}
		wg.Wait()
		slots = old
		var lateCancel, lateStop time.Duration
		for i, m := range ms {
			ready, cancelled := time.Unix(0, m.readyAt.Load()), time.Unix(0, m.cancelledAt.Load())
			if m.readyAt.Load() == 0 || m.cancelledAt.Load() == 0 {
				t.Errorf("%d slots, instance %d: never ready or never cancelled by the watchdog", k, i)
				continue
			}
			lateCancel = max(lateCancel, cancelled.Sub(ready)-limit)
			lateStop = max(lateStop, ends[i].Sub(cancelled))
		}
		t.Logf("%d instances in %d slots (GOMAXPROCS %d): %v; cancelled at most %v after turn + limit, returned at most %v after the cancellation",
			n, k, runtime.GOMAXPROCS(0), time.Since(start), lateCancel, lateStop)
		if k == capped && (lateCancel > margin || lateStop > margin) {
			t.Errorf("%d instances in %d slots: a cancellation came %v after turn + limit, a return %v after its cancellation (each at most %v)",
				n, k, lateCancel, lateStop, margin)
		}
		for i, err := range errs {
			var le *LimitError
			if !errors.As(err, &le) || le.Limit != "EngineStallTimeout" {
				t.Errorf("%d slots, instance %d: %v", k, i, err)
			}
		}
	}
}

// TestHoldAndWait documents the hazard of both caps (DESIGN.md sections 8 and 12), on worker
// processes (WorkerProcesses) and in process (EngineInstances): a caller that keeps
// that many decoders open without reading them holds every slot, and its next probe or
// decoder waits for a slot until its context ends; waiting for a slot is never a stall. Closing a
// decoder frees its slot. Not parallel: it runs with a single slot.
func TestHoldAndWait(t *testing.T) {
	old := slots
	slots = make(chan struct{}, 1)
	defer func() { slots = old }()
	// Worker processes are capped too (WorkerProcesses): a held decoder holds its worker's turn, and
	// with a single turn the next decoder's probe waits for it until its context ends.
	oldWorkers := workerSlots
	workerSlots = make(chan struct{}, 1)
	defer func() { workerSlots = oldWorkers }()
	w := embedded(t)
	held, err := w.NewDecoder(t.Context(), clip("hevc8.mp4"), DecodeConfig{BrightnessOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	var f Frame
	if err := held.Next(&f); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	wctx, wcancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer wcancel()
	if _, err := w.NewDecoder(wctx, clip("hevc8.mp4"), DecodeConfig{BrightnessOnly: true}); !errors.Is(err, context.DeadlineExceeded) ||
		time.Since(start) < 3*time.Second {
		t.Errorf("waiting for the held worker's turn: %v after %v", err, time.Since(start))
	}
	_ = held.Close()
	// In process (the fallback), the cap applies.
	b := &Embedded{ff{r: engineFor(t)}}
	held, err = b.NewDecoder(t.Context(), clip("hevc8.mp4"), DecodeConfig{BrightnessOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := held.Next(&f); err != nil { // its instance now runs, blocked writing the next frame
		t.Fatal(err)
	}
	start = time.Now() // before the deadline is set, so that the wait is measured whole
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if _, err := b.NewDecoder(ctx, clip("hevc8.mp4"), DecodeConfig{BrightnessOnly: true}); !errors.Is(err, context.DeadlineExceeded) ||
		time.Since(start) < 3*time.Second {
		t.Errorf("waiting for the held slot: %v after %v", err, time.Since(start))
	}
	_ = held.Close()
	d, err := b.NewDecoder(t.Context(), clip("hevc8.mp4"), DecodeConfig{BrightnessOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(decodeAll(t, d)); n != 24 {
		t.Errorf("%d frames after the slot was freed", n)
	}
}
