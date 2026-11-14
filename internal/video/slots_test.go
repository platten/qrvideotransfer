package video

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"
)

// TestWatchdogUnderLoad: compiled Wasm cannot be preempted by the Go scheduler, so with an instance
// spinning on every P the watchdog's goroutine would never run. More spinning instances than Ps are
// started at once, each with a 2 s watchdog; every one must be stopped by it, and promptly: at most
// EngineInstances() run at a time, leaving a P free, and an instance waiting for its turn is not
// stalled.
func TestWatchdogUnderLoad(t *testing.T) {
	e := engineFor(t)
	n := runtime.GOMAXPROCS(0) + 1
	slots := EngineInstances()
	if slots < 1 || slots > max(1, runtime.GOMAXPROCS(0)-1) {
		t.Fatalf("EngineInstances() = %d with GOMAXPROCS %d", slots, runtime.GOMAXPROCS(0))
	}
	const limit = 2 * time.Second
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
	rounds := (n + slots - 1) / slots
	if el := time.Since(start); el > time.Duration(rounds)*(limit+2*time.Second)+5*time.Second {
		t.Errorf("%d instances in %d slots took %v", n, slots, el)
	}
	for i, err := range errs {
		var le *LimitError
		if !errors.As(err, &le) || le.Limit != "EngineStallTimeout" {
			t.Errorf("instance %d: %v", i, err)
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
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	start := time.Now()
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
