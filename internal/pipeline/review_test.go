package pipeline

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/payload"
	"github.com/platten/qrvideotransfer/internal/video"
)

// TestFreeSpace: the platform's free-space query answers for a real folder and fails for a
// missing one.
func TestFreeSpace(t *testing.T) {
	n, err := diskFree(t.TempDir())
	if err != nil || n <= 0 {
		t.Errorf("free space %d, %v", n, err)
	}
	if _, err := diskFree(filepath.Join(t.TempDir(), "none")); err == nil {
		t.Error("free space of a missing folder")
	}
}

// TestDiskSpaceLimit: the shard store and the stream file are made only when the temporary
// folder's free space, less 1 GiB, holds them (SECURITY.md section 5 and T3); otherwise the decode
// stops with a LimitError and writes nothing.
func TestDiskSpaceLimit(t *testing.T) {
	in, want := writeInputs(t, 30, 3000)
	path, _ := encodeRaw(t, profile480(in))
	defer func() { diskFree = platformDiskFree }()
	for _, c := range []struct {
		free int64
		ok   bool
	}{{ReserveBytes + 1<<20, true}, {ReserveBytes + 1000, false}, {0, false}} {
		diskFree = func(string) (int64, error) { return c.free, nil }
		tmp, out := t.TempDir(), filepath.Join(t.TempDir(), "out")
		_, err := Decode(context.Background(), DecodeConfig{Recordings: []string{path}, Backend: video.NewRaw(), TempDir: tmp,
			Unpack: payload.UnpackOptions{OutputDir: out}})
		if c.ok {
			if err != nil {
				t.Fatalf("free %d: %v", c.free, err)
			}
			checkOutput(t, out, want)
			continue
		}
		var lim *payload.LimitError
		if !errors.As(err, &lim) || lim.Limit != "free disk space" {
			t.Fatalf("free %d: got %v", c.free, err)
		}
		if fs := filesUnder(t, tmp); len(fs) != 0 {
			t.Errorf("temporary files left: %v", fs)
		}
	}
	// A failed query, at the store and at the stream file, and the space gone by the second check:
	// each fails closed, with nothing left behind.
	for name, free := range map[string]func(call int) (int64, error){
		"query fails": func(int) (int64, error) { return 0, errInjected },
		"query fails at the stream": func(call int) (int64, error) {
			if call == 1 {
				return 1 << 40, nil
			}
			return 0, errInjected
		},
		"space gone before the stream": func(call int) (int64, error) {
			if call == 1 {
				return 1 << 40, nil
			}
			return ReserveBytes, nil
		},
	} {
		calls := 0
		diskFree = func(string) (int64, error) { calls++; return free(calls) }
		tmp, out := t.TempDir(), filepath.Join(t.TempDir(), "out")
		_, err := Decode(context.Background(), DecodeConfig{Recordings: []string{path}, Backend: video.NewRaw(), TempDir: tmp,
			Unpack: payload.UnpackOptions{OutputDir: out}})
		var lim *payload.LimitError
		if !errors.Is(err, errInjected) && !errors.As(err, &lim) {
			t.Errorf("%s: %v", name, err)
		}
		if name != "query fails" && calls != 2 {
			t.Errorf("%s: %d checks, want 2", name, calls)
		}
		if fs := append(filesUnder(t, tmp), filesUnder(t, out)...); len(fs) != 0 {
			t.Errorf("%s: files left: %v", name, fs)
		}
	}
}

// TestReorderBounded: while one worker stalls on a capture, the others read no more than a fixed
// number of captures ahead, so the results waiting to be applied in order stay bounded.
func TestReorderBounded(t *testing.T) {
	in, want := writeInputs(t, 31, 60000)
	path, res := encodeRaw(t, profile480(in))
	frames, info := readFrames(t, video.NewRaw(), path)
	var long []*video.Frame
	for range 4 {
		long = append(long, frames...)
	}
	rec := writeRaw(t, long, info.FPSNum/info.FPSDen)
	// Capture 0 is held until 20 later captures have been processed, which unbounded reading reaches
	// at once, or until no later capture has been processed for 2 s, which is how bounded reading
	// shows: it reads at most workers+2 captures ahead of the one held.
	const ahead = 20
	var done atomic.Int64
	defer func() { processCapture = (*captureWorker).process }()
	processCapture = func(w *captureWorker, f *video.Frame, i int) captureResult {
		if i != 0 {
			r := w.process(f, i)
			done.Add(1)
			return r
		}
		last, since := done.Load(), time.Now()
		for done.Load() < ahead && time.Since(since) < 2*time.Second {
			time.Sleep(10 * time.Millisecond)
			if n := done.Load(); n != last {
				last, since = n, time.Now()
			}
		}
		return captureResult{index: i} // not located: the decode needs the later captures
	}
	const workers = 4
	out := filepath.Join(t.TempDir(), "out")
	d, err := decodeWith(t, DecodeConfig{Recordings: []string{rec}, Backend: video.NewRaw(), Workers: workers, TempDir: t.TempDir(),
		Unpack: payload.UnpackOptions{OutputDir: out}})
	if err != nil {
		t.Fatal(err)
	}
	checkOutput(t, out, want)
	if d.maxWaiting == 0 || d.maxWaiting > workers+2 {
		t.Errorf("%d results waited to be applied, more than %d (F %d)", d.maxWaiting, workers+2, res.Frames)
	}
}

// TestRule11EveryCapture: a frame is attempted once two captures have passed without it, even when
// those captures show no code.
func TestRule11EveryCapture(t *testing.T) {
	key, truth := fixture(t)
	d := &decoder{c: &DecodeConfig{}, tmp: t.TempDir(), frames: map[uint32]*frameAcc{}, softCap: MaxSoftBytes}
	defer d.close()
	for f := range uint32(4) {
		if err := d.apply(captureResult{index: int(f), located: true, key: key, cells: 1, clean: 1,
			phases: []phaseBits{{frame: f, soft: truth[f]}}}); err != nil {
			t.Fatal(err)
		}
	}
	if d.desc == nil || len(d.pending) == 0 {
		t.Fatalf("descriptor %v, %d pending", d.desc != nil, len(d.pending))
	}
	for i := 4; i < 6; i++ {
		if err := d.apply(captureResult{index: i}); err != nil {
			t.Fatal(err)
		}
	}
	if len(d.pending) != 0 || !d.complete() {
		t.Errorf("%d frames still pending after two captures without code; complete %v", len(d.pending), d.complete())
	}
}
