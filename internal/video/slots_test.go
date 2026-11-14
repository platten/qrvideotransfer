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
// an instance waiting for its turn is not stalled, and every one is stopped promptly: that is
// asserted. Without the cap (DESIGN.md section 12) every one must still be stopped by the watchdog,
// but the time is only logged: on a 16-thread development machine they were stopped on time, while
// on the macos-26-intel CI runner five took 16.8 s against a 2 s limit (up to 8 times late), which is
// why the cap is a real defence.
// Not parallel: it replaces the slots.
func TestWatchdogUnderLoad(t *testing.T) {
	e := engineFor(t)
	n := runtime.GOMAXPROCS(0) + 1
	capped := EngineInstances()
	if capped < 1 || capped > max(1, runtime.GOMAXPROCS(0)-1) {
		t.Fatalf("EngineInstances() = %d with GOMAXPROCS %d", capped, runtime.GOMAXPROCS(0))
	}
	const limit = 2 * time.Second
	for _, k := range []int{capped, n} {
		old := slots
		slots = make(chan struct{}, k)
		start := time.Now()
		var wg sync.WaitGroup
		errs := make([]error, n)
		for i := range n {
			wg.Go(func() {
				errs[i] = run(t.Context(), e, job{prog: "ffmpeg", progress: progOutput, timeout: limit,
					args: func(string, string) []string { return spin }})
			})
		}
		wg.Wait()
		slots = old
		rounds := (n + k - 1) / k
		el := time.Since(start)
		t.Logf("%d instances in %d slots (GOMAXPROCS %d): %v", n, k, runtime.GOMAXPROCS(0), el)
		if k == capped && el > time.Duration(rounds)*(limit+2*time.Second)+5*time.Second {
			t.Errorf("%d instances in %d slots took %v", n, k, el)
		}
		for i, err := range errs {
			var le *LimitError
			if !errors.As(err, &le) || le.Limit != "EngineStallTimeout" {
				t.Errorf("%d slots, instance %d: %v", k, i, err)
			}
		}
	}
}

// TestHoldAndWait documents the instance cap's hazard (DESIGN.md section 12): a caller that keeps
// EngineInstances() decoders open without reading them holds every slot, and its next probe or
// decoder waits for a slot until its context ends; waiting for a slot is never a stall. Closing a
// decoder frees its slot. Not parallel: it runs with a single slot.
func TestHoldAndWait(t *testing.T) {
	old := slots
	slots = make(chan struct{}, 1)
	defer func() { slots = old }()
	b := embedded(t)
	held, err := b.NewDecoder(t.Context(), clip("hevc8.mp4"), DecodeConfig{BrightnessOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	var f Frame
	if err := held.Next(&f); err != nil { // its instance now runs, blocked writing the next frame
		t.Fatal(err)
	}
	start := time.Now() // before the deadline is set, so that the wait is measured whole
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
