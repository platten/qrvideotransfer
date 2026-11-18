package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/payload"
	"github.com/platten/qrvideotransfer/internal/prng"
	"github.com/platten/qrvideotransfer/internal/video"
)

// The bounds of SECURITY.md section 9.5 for a hostile input: it finishes within 60 s and the
// process's memory stays under 1 GiB; the Go heap's growth is sampled and asserted.
const (
	hostileTime = 60 * time.Second
	hostileHeap = 1 << 30
)

// heapPeak samples the Go heap in use every 10 ms while run runs and returns run's duration and the
// largest growth over the heap before it.
func heapPeak(t *testing.T, run func()) (time.Duration, uint64) {
	t.Helper()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	base := ms.HeapInuse
	var peak atomic.Uint64
	done := make(chan struct{})
	go func() {
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			if m.HeapInuse > base && m.HeapInuse-base > peak.Load() {
				peak.Store(m.HeapInuse - base)
			}
			select {
			case <-done:
				return
			case <-tick.C:
			}
		}
	}()
	t0 := time.Now()
	run()
	d := time.Since(t0)
	close(done)
	return d, peak.Load()
}

// noiseFrames returns n frames of w x h pseudo-random samples.
func noiseFrames(n, w, h int, seed uint64) []*video.Frame {
	g := prng.SplitMix64{State: seed}
	var out []*video.Frame
	for range n {
		f := &video.Frame{W: w, H: h, Bits: 8, Y: make([]uint16, w*h), Cb: make([]uint16, w*h/4), Cr: make([]uint16, w*h/4)}
		for _, p := range [][]uint16{f.Y, f.Cb, f.Cr} {
			for i := range p {
				p[i] = uint16(g.Next() & 0xff)
			}
		}
		out = append(out, f)
	}
	return out
}

// TestHostileRecordingResources (SECURITY.md section 9.5): recordings that show no transfer, noise,
// end within the time bound and the heap bound, with nothing known and nothing written. The short
// version decodes 16 frames at 848 x 480; the long version (QRVT_LONG_TESTS=1) 32 at 3840 x 2160.
func TestHostileRecordingResources(t *testing.T) {
	n, w, h := 16, 848, 480
	if os.Getenv("QRVT_LONG_TESTS") == "1" {
		n, w, h = 32, 3840, 2160
	}
	rec := writeRaw(t, noiseFrames(n, w, h, 9), 30)
	out := filepath.Join(t.TempDir(), "out")
	var err error
	d, peak := heapPeak(t, func() {
		_, err = Decode(context.Background(), DecodeConfig{Recordings: []string{rec}, Backend: video.NewRaw(), TempDir: t.TempDir(),
			Unpack: payload.UnpackOptions{OutputDir: out}})
	})
	var short *ShortError
	if !errors.As(err, &short) || len(short.Blocks) != 0 {
		t.Errorf("noise: %v, want a ShortError with nothing known", err)
	}
	if fs := filesUnder(t, out); len(fs) != 0 {
		t.Errorf("files written: %v", fs)
	}
	t.Logf("%d noise frames at %d x %d: %.1f s, heap growth %.1f MiB", n, w, h, d.Seconds(), float64(peak)/(1<<20))
	if d > hostileTime || peak > hostileHeap {
		t.Errorf("%.1f s and %d bytes of heap growth, bounds %v and %d", d.Seconds(), peak, hostileTime, hostileHeap)
	}
}

// TestEvictionLoopCost measures the open item of SECURITY.md section 5 (soft-bit store, M7): a
// decode held at its cap, where every new frame forces an eviction that rescans everything held.
// Long version only (QRVT_LONG_TESTS=1): the decoder is filled to the full 256 MB cap with frames of
// a transfer whose descriptor never decodes, then fed captures of further frames; it logs the cost
// per capture at the cap and the heap's growth, and asserts the heap bound. The time a recording
// of such captures costs grows with its length, so no time bound is asserted; see DESIGN.md
// section 19.
func TestEvictionLoopCost(t *testing.T) {
	if os.Getenv("QRVT_LONG_TESTS") != "1" {
		t.Skip("long test: QRVT_LONG_TESTS=1")
	}
	key, truth := fixture(t)
	key.frames = 1 << 20
	g, err := newGeometry(key)
	if err != nil {
		t.Fatal(err)
	}
	soft := append([]int8(nil), truth[0]...)
	clear(soft[:g.sent])
	d := &decoder{c: &DecodeConfig{}, tmp: t.TempDir(), frames: map[uint32]*frameAcc{}, softCap: MaxSoftBytes}
	defer d.close()
	perFrame := frameRecordBytes(g.cpf) + int64((g.cpf-1)*g.sent)
	fill := int(MaxSoftBytes / perFrame)
	idx := 0
	add := func(f int) {
		if err := d.apply(captureResult{index: 3 * idx, located: true, key: key, cells: 1, clean: 1,
			phases: []phaseBits{{frame: uint32(f), soft: soft}}}); err != nil {
			t.Fatal(err)
		}
		idx++
	}
	var fillTime, loopTime time.Duration
	const loop = 200
	_, peak := heapPeak(t, func() {
		t0 := time.Now()
		for f := range fill {
			add(f)
		}
		fillTime = time.Since(t0)
		t0 = time.Now()
		for f := range loop {
			add(fill + f)
		}
		loopTime = time.Since(t0)
	})
	t.Logf("%d frames fill the cap (%d bytes held) in %.1f s; then %d captures at the cap took %.1f s, %.1f ms each; heap growth %.0f MiB",
		fill, d.usedBytes(), fillTime.Seconds(), loop, loopTime.Seconds(), 1000*loopTime.Seconds()/loop, float64(peak)/(1<<20))
	if d.usedBytes() > d.softCap {
		t.Errorf("%d bytes held over the cap %d", d.usedBytes(), d.softCap)
	}
	if peak > hostileHeap {
		t.Errorf("heap growth %d bytes, bound %d", peak, hostileHeap)
	}
}
