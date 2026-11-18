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

	"github.com/platten/qrvideotransfer/internal/frame"
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
// version decodes 16 frames at 848 x 480; the long version (QRVT_LONG_TESTS=1) 32 at 3840 x 2160
// with GOMAXPROCS forced to 16 and to 32, which the decode's memory cap must absorb.
func TestHostileRecordingResources(t *testing.T) {
	raceSkipStatistics(t)
	if os.Getenv("QRVT_LONG_TESTS") != "1" {
		hostileRecording(t, 16, 848, 480)
		return
	}
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(0))
	for _, procs := range []int{16, 32} {
		runtime.GOMAXPROCS(procs)
		hostileRecording(t, 32, 3840, 2160)
	}
}

// hostileRecording decodes n noise frames of w x h and asserts the bounds.
func hostileRecording(t *testing.T, n, w, h int) {
	t.Helper()
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
	t.Logf("%d noise frames at %d x %d, GOMAXPROCS %d: %.1f s, heap growth %.1f MiB", n, w, h, runtime.GOMAXPROCS(0), d.Seconds(), float64(peak)/(1<<20))
	if d > hostileTime || peak > hostileHeap {
		t.Errorf("%.1f s and %d bytes of heap growth, bounds %v and %d", d.Seconds(), peak, hostileTime, hostileHeap)
	}
}

// locatedFrames returns n captures of a transfer encoded at w x h with cell-pixel cells in
// modulation 1, whose grid stage holds at least as much as modulations 2 and 3 at 4K (DESIGN.md
// section 19): the encoder's frames, repeated.
func locatedFrames(t *testing.T, n, w, h, cell int) ([]*video.Frame, map[string][32]byte) {
	t.Helper()
	in, want := writeInputs(t, 77, 20000)
	path, _ := encodeRaw(t, EncodeConfig{Inputs: []string{in}, Width: w, Height: h, Cell: cell, FPS: 12, Modulation: 1,
		Inner: 2, Parity: 20, Compression: payload.CompressNone})
	frames, _ := readFrames(t, video.NewRaw(), path)
	var out []*video.Frame
	for len(out) < n {
		out = append(out, frames...)
	}
	return out[:max(n, len(frames))], want
}

// largestGridFrame renders a code frame of the largest grid a header can claim, 2047 x 1393 cells,
// and scales it to 3840 x 2160 (1.9 and 1.6 pixels a cell, above the vision's 1.5-pixel gate): the
// capture whose grid stage holds the most at 4K. Its symbols are a fixed pattern, not codewords: it
// is located and its soft bits read, and nothing decodes. (Pseudo-random symbols, as real codewords
// look, are not located at 1.6 pixels a cell; a sender chooses the symbols.)
func largestGridFrame(t *testing.T) *video.Frame {
	t.Helper()
	c := EncodeConfig{Inputs: []string{"unused"}, Width: 4 * frame.MaxGrid, Height: 4 * 1393, Cell: 4, FPS: 12, Modulation: 1,
		Inner: 2, Parity: 20, Compression: payload.CompressNone, Random: &prngReader{prng.SplitMix64{State: 3}}}
	e, err := newEncoding(&c, &payload.PackResult{StreamLen: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	symbols := make([]uint8, len(e.render.Layout().DataCells()))
	for i := range symbols {
		symbols[i] = uint8(i*7919>>3) & 1
	}
	w, h := e.render.Size()
	f := &video.Frame{W: w, H: h, Bits: 8, Y: make([]uint16, w*h), Cb: make([]uint16, w*h/4), Cr: make([]uint16, w*h/4)}
	if err := e.render.Render(f, e.header, symbols); err != nil {
		t.Fatal(err)
	}
	f.W, f.H, f.Bits = w, h, 8
	return boxScale(f, 3840, 2160)
}

// boxScale averages f's samples into a w x h frame, each plane on its own.
func boxScale(f *video.Frame, w, h int) *video.Frame {
	out := &video.Frame{W: w, H: h, Bits: f.Bits, Y: make([]uint16, w*h), Cb: make([]uint16, w*h/4), Cr: make([]uint16, w*h/4)}
	scale := func(dst, src []uint16, dw, dh, sw, sh int) {
		for y := range dh {
			y0, y1 := y*sh/dh, max((y+1)*sh/dh, y*sh/dh+1)
			for x := range dw {
				x0, x1 := x*sw/dw, max((x+1)*sw/dw, x*sw/dw+1)
				sum, n := 0, 0
				for yy := y0; yy < y1; yy++ {
					for xx := x0; xx < x1; xx++ {
						sum += int(src[yy*sw+xx])
						n++
					}
				}
				dst[y*dw+x] = uint16(sum / n)
			}
		}
	}
	scale(out.Y, f.Y, w, h, f.W, f.H)
	scale(out.Cb, f.Cb, w/2, h/2, f.W/2, f.H/2)
	scale(out.Cr, f.Cr, w/2, h/2, f.W/2, f.H/2)
	return out
}

// locatedDecode decodes frames with the default workers at GOMAXPROCS procs and returns the
// decoder, the time, the heap's growth and the error.
func locatedDecode(t *testing.T, frames []*video.Frame, procs int) (*decoder, time.Duration, uint64, error) {
	t.Helper()
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(procs))
	rec := writeRaw(t, frames, 12)
	var d *decoder
	var err error
	took, peak := heapPeak(t, func() {
		d, err = decodeWith(t, DecodeConfig{Recordings: []string{rec}, Backend: video.NewRaw(), TempDir: t.TempDir(),
			Unpack: payload.UnpackOptions{OutputDir: filepath.Join(t.TempDir(), "out")}})
	})
	t.Logf("%d captures at %d x %d, GOMAXPROCS %d: %.1f s, heap growth %.0f MiB; grid stage: budget %d MiB, at most %d at once holding %d MiB",
		len(frames), frames[0].W, frames[0].H, procs, took.Seconds(), float64(peak)/(1<<20), d.gate.budget>>20, d.gate.maxInside, d.gate.maxUsed>>20)
	if d.gate.maxUsed > d.gate.budget {
		t.Errorf("the grid stage held %d bytes, over its budget %d", d.gate.maxUsed, d.gate.budget)
	}
	return d, took, peak, err
}

// TestLocatedRecordingResources (SECURITY.md section 9.5): recordings that show a transfer, whose
// grid stage holds far more than a noise recording's, keep the decode's grid stage within its
// budget. Long version only (QRVT_LONG_TESTS=1; the gate's bound is asserted always by
// TestStageGateGridChange): 24 captures at 3840 x 2160
// with the 4K preset's grid, GOMAXPROCS 16 and 32, within 1 GiB of heap growth, one at a time in
// the grid stage. Beyond the measured boundary the bound is not met, and the figures are logged,
// not asserted (DESIGN.md section 19; M7 decides): 8 captures at 7680 x 4320 with the 4K grid, one
// worker; 6 captures at 4K of the largest grid a header can claim, whose stage alone exceeds the
// budget and runs alone.
func TestLocatedRecordingResources(t *testing.T) {
	raceSkipStatistics(t)
	if os.Getenv("QRVT_LONG_TESTS") != "1" {
		t.Skip("long test: QRVT_LONG_TESTS=1")
	}
	frames, want := locatedFrames(t, 24, 3840, 2160, 4)
	for _, procs := range []int{16, 32} {
		d, _, peak, err := locatedDecode(t, frames, procs)
		if err != nil {
			t.Fatal(err)
		}
		checkOutput(t, d.c.Unpack.OutputDir, want)
		if peak > hostileHeap || d.gate.maxInside != 1 {
			t.Errorf("4K: heap growth %d bytes (bound %d), %d units at once", peak, hostileHeap, d.gate.maxInside)
		}
	}
	frames, _ = locatedFrames(t, 8, 7680, 4320, 8)
	if _, _, peak, err := locatedDecode(t, frames, 16); err != nil {
		t.Errorf("8K, the 4K grid: %v", err)
	} else {
		t.Logf("8K, the 4K grid: heap growth %.0f MiB, against the 1 GiB bound", float64(peak)/(1<<20))
	}
	f := largestGridFrame(t)
	d, _, peak, _ := locatedDecode(t, []*video.Frame{f, f, f, f, f, f}, 16)
	t.Logf("4K, the largest grid: heap growth %.0f MiB, against the 1 GiB bound", float64(peak)/(1<<20))
	if d.gate.maxInside != 1 || d.gate.maxUsed != d.gate.budget {
		t.Errorf("4K, the largest grid: %d units at once holding %d of %d", d.gate.maxInside, d.gate.maxUsed, d.gate.budget)
	}
}

// TestEvictionLoopCost measures the open item of SECURITY.md section 5 (soft-bit store, M7): a
// decode held at its cap, where every new frame forces an eviction.
// Long version only (QRVT_LONG_TESTS=1): the decoder is filled to the full 256 MB cap with frames of
// a transfer whose descriptor never decodes, then fed captures of further frames; it logs the cost
// per capture at the cap and the heap's growth, and asserts both bounds over the fill and 200
// captures at the cap: the eviction's ordered structures keep a capture's cost from growing with
// what is held (DESIGN.md section 19).
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
	if peak > hostileHeap || fillTime+loopTime > hostileTime {
		t.Errorf("heap growth %d bytes and %.1f s, bounds %d and %v", peak, (fillTime + loopTime).Seconds(), hostileHeap, hostileTime)
	}
}
