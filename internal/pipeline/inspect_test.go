package pipeline

import (
	"context"
	"errors"
	"os"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/channel"
	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/prng"
	"github.com/platten/qrvideotransfer/internal/video"
)

// The always-on tests of Scan and Inspect use the smallest setting, a 480 x 480 four greys
// `normal` code of 500 bytes (rateCode: 120 x 120 grid, F = 4, 30 fps), since every capture costs a
// run of the vision chain.

// TestScanCaptures: Scan hands a capture to the sink with what the vision chain found: the
// headers, the camera pixels per cell, the palette's clipped share and each code frame's soft bits
// by bit position, which, put in codeword order, are exactly the soft bits Decode sums. With stubbed
// captures: every capture in order, recording after recording, with its presentation time; a sink
// that stops, or fails, ends the scan.
func TestScanCaptures(t *testing.T) {
	tr := rateCode(t, 2, 2, 480, 500)
	a := writeRaw(t, tr.frames[:1], 30)
	want := (&captureWorker{}).process(tr.frames[0], 0)
	g, err := newGeometry(want.key)
	if err != nil {
		t.Fatal(err)
	}
	var got []*Capture
	progress := 0
	sr, err := Scan(context.Background(), ScanConfig{Recordings: []string{a}, Backend: video.NewRaw(), Workers: 1,
		Progress: func(e Event) {
			if e.Stage == StageDecodeVideo && e.Kind == KindProgress {
				progress++
			}
		}}, func(c *Capture) (bool, error) {
		got = append(got, c)
		return false, nil
	})
	if err != nil || sr.Captures != 1 || len(got) != 1 || progress != 1 || len(sr.Probes) != 1 || sr.Probes[0].Width != 480 {
		t.Fatalf("scan: %v, %+v, %d captures, %d progress events", err, sr, len(got), progress)
	}
	c := got[0]
	if !c.Finders || !c.Located || len(c.Headers) == 0 || paramsOf(c.Headers[0]) != want.key || c.Headers[0].FrameIndex != 0 ||
		c.Cells != want.cells || c.Clean != want.clean || len(c.Phases) != 1 || c.Phases[0].Frame != want.phases[0].frame {
		t.Fatalf("capture: %+v", c)
	}
	if c.PixelsPerCell < 3.9 || c.PixelsPerCell > 4.1 || c.PaletteClipped != 0 {
		t.Errorf("%.3f pixels per cell (cells of 4), %.2f of the palette clipped", c.PixelsPerCell, c.PaletteClipped)
	}
	cw := make([]int8, g.cpf*g.sent)
	g.gather(cw, c.Phases[0].Soft, c.Phases[0].Frame)
	if !reflect.DeepEqual(cw, want.phases[0].soft) {
		t.Error("the soft bits differ from Decode's")
	}

	// Stopping and failing, with stubbed captures of blank frames: the PTS goes through.
	blank := &video.Frame{W: 32, H: 32, Bits: 8, Y: make([]uint16, 32*32), Cb: make([]uint16, 16*16), Cr: make([]uint16, 16*16)}
	c4 := writeRaw(t, []*video.Frame{blank, blank, blank, blank}, 30)
	defer func() { scanCapture = (*captureWorker).scan }()
	scanCapture = func(_ *captureWorker, f *video.Frame, i int) captureResult {
		return captureResult{index: i, pts: f.PTS}
	}
	var seen [][3]int
	sr, err = Scan(context.Background(), ScanConfig{Recordings: []string{c4, c4, c4}, Backend: video.NewRaw(), Workers: 2}, func(c *Capture) (bool, error) {
		seen = append(seen, [3]int{c.Recording, c.Index, int(c.PTS / (time.Second / 30))})
		return len(seen) == 6, nil
	})
	if want := [][3]int{{0, 0, 0}, {0, 1, 1}, {0, 2, 2}, {0, 3, 3}, {1, 0, 0}, {1, 1, 1}}; err != nil || sr.Captures != 6 ||
		len(sr.Probes) != 2 || !reflect.DeepEqual(seen, want) {
		t.Errorf("a sink stopping at 6: %v, %+v, recording, index and PTS by capture %v, want %v", err, sr, seen, want)
	}
	boom := errors.New("boom")
	if _, err := Scan(context.Background(), ScanConfig{Recordings: []string{c4}, Backend: video.NewRaw(), Workers: 1}, func(*Capture) (bool, error) {
		return false, boom
	}); !errors.Is(err, boom) {
		t.Errorf("a failing sink: %v", err)
	}
	for _, c := range []ScanConfig{{Backend: video.NewRaw()}, {Recordings: []string{a}}} {
		if _, err := Scan(context.Background(), c, func(*Capture) (bool, error) { return false, nil }); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
	if _, err := Scan(context.Background(), ScanConfig{Recordings: []string{a}, Backend: video.NewRaw()}, nil); err == nil {
		t.Error("no sink accepted")
	}
	if sr, err := Scan(context.Background(), ScanConfig{Recordings: []string{a + ".missing"}, Backend: video.NewRaw()},
		func(*Capture) (bool, error) { return false, nil }); err == nil || len(sr.Probes) != 0 {
		t.Errorf("a missing recording: %v, %+v", err, sr)
	}
}

// TestInspectEncoderFrames: Inspect reads the descriptor from the encoder's own frames, stopping at
// the first capture, with every field the encode wrote, the display rate (descriptor byte 37)
// included.
func TestInspectEncoderFrames(t *testing.T) {
	tr := rateCode(t, 2, 2, 480, 500)
	res := tr.res
	var events []Event
	r, err := Inspect(context.Background(), InspectConfig{Path: writeRaw(t, tr.frames, 30), Backend: video.NewRaw(), Workers: 1,
		Progress: func(e Event) { events = append(events, e) }})
	if err != nil {
		t.Fatal(err)
	}
	d := r.Descriptor
	if d == nil || !r.HeaderRead || r.Stats.Captures != 1 {
		t.Fatalf("%+v", r)
	}
	if d.Session != res.Session || d.StreamLen != res.StreamLen || d.Blocks != res.Blocks || d.K != res.K || d.M != res.M ||
		d.Frames != res.Frames || d.CodewordsPerFrame != res.Codewords || d.DisplayFPS != 30 || d.ShardSize != 128 {
		t.Errorf("descriptor %+v, encode %+v", d, res)
	}
	h := r.Header
	if h.GridX != 120 || h.GridY != 120 || h.Modulation != 2 || h.InnerCode != 2 || int(h.TotalFrames) != res.Frames ||
		h.Session != uint16(res.Session) || r.Probe.Width != 480 || r.Probe.Height != 480 {
		t.Errorf("header %+v, probe %+v", h, r.Probe)
	}
	if last := events[len(events)-1]; last.Stage != StageDemodulate || last.Kind != KindFinished {
		t.Errorf("events end with %+v", last)
	}
	for _, c := range []InspectConfig{{Backend: video.NewRaw()}, {Path: "x.raw"}, {Path: "missing.raw", Backend: video.NewRaw()}} {
		if _, err := Inspect(context.Background(), c); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
}

// TestInspectStopsEarly: on a filmed recording Inspect stops once the descriptor decodes, before
// the recording ends: a 60 fps camera, two passes, 16 captures. Long only (QRVT_LONG_TESTS=1), for
// the always-on cost; always on, TestInspectEncoderFrames stops at the first of 4 captures.
func TestInspectStopsEarly(t *testing.T) {
	if !rateLong() {
		t.Skip("long only")
	}
	tr := rateCode(t, 2, 2, 480, 500)
	c := preset(t, "tripod", 908, 908)
	n := 4 * tr.res.Frames
	rec := filmFrames(t, tr.frames, 30, 4, c, channel.Run{Start: time.Second / 30 / 8, Captures: n, Seed: 0x5eed + 1})
	r, err := Inspect(context.Background(), InspectConfig{Path: rec, Backend: video.NewRaw(), Workers: 1})
	if err != nil || r.Descriptor == nil || r.Descriptor.Session != tr.res.Session {
		t.Fatalf("%v, %+v", err, r)
	}
	t.Logf("%d of %d captures read", r.Stats.Captures, n)
	if r.Stats.Captures > 3 {
		t.Errorf("%d captures read before the descriptor decoded, want at most 3 of %d", r.Stats.Captures, n)
	}
}

// TestInspectHeadersOnly: a recording whose headers are read but whose descriptor is not ends in
// Decode's ShortError, with the failure diagnostics, and the header read; one whose captures agree
// on an unsupported format version ends in the agreed refusal (exit 5 through the root package).
func TestInspectHeadersOnly(t *testing.T) {
	tr := rateCode(t, 2, 2, 480, 500)
	g := prng.SplitMix64{State: 3}
	// Noise of +-40 levels leaves the headers readable and nothing else: measured over seeds 1 to 8
	// on the first two frames, at +-30 to +-50 every seed read headers and no descriptor.
	var frames []*video.Frame
	for _, f := range tr.frames[:1] {
		n := *f
		n.Y = make([]uint16, len(f.Y))
		for i, v := range f.Y {
			n.Y[i] = uint16(max(0, min(255, int(v)+int(g.Next()%81)-40)))
		}
		frames = append(frames, &n)
	}
	rec := writeRaw(t, frames, 30)
	r, err := Inspect(context.Background(), InspectConfig{Path: rec, Backend: video.NewRaw(), Workers: 1})
	var short *ShortError
	if !errors.As(err, &short) || r == nil || !r.HeaderRead || r.Descriptor != nil || int(r.Header.TotalFrames) != tr.res.Frames {
		t.Fatalf("%v, %+v", err, r)
	}
	if short.Captures != 1 || short.HeadersRead != 1 || short.Descriptor || len(short.Blocks) != 0 || short.FramesTotal != tr.res.Frames {
		t.Errorf("%+v", short)
	}
	_, _, derr := decodeTo(t, rec)
	var dshort *ShortError
	if !errors.As(derr, &dshort) || !reflect.DeepEqual(dshort, short) {
		t.Errorf("Decode's shortfall %+v, Inspect's %+v", derr, short)
	}

	// Stubbed captures, as TestShortDiagnostics.
	key := params{session: 9, gridX: 120, gridY: 120, mod: 2, inner: 2, frames: 4}
	v2 := refused(key, frame.HeaderVersion, func(h *frame.Header) { h.Version = 2 })
	blank := &video.Frame{W: 32, H: 32, Bits: 8, Y: make([]uint16, 32*32), Cb: make([]uint16, 16*16), Cr: make([]uint16, 16*16)}
	rec = writeRaw(t, []*video.Frame{blank, blank, blank, blank}, 30)
	defer func() { scanCapture = (*captureWorker).scan }()
	scanCapture = func(_ *captureWorker, _ *video.Frame, i int) captureResult {
		r := v2
		r.index = i
		r.reject.Header.FrameIndex = uint32(i)
		return r
	}
	r, err = Inspect(context.Background(), InspectConfig{Path: rec, Backend: video.NewRaw()})
	if !errors.As(err, &short) || short.Reject == nil || short.Reject.Reason != frame.HeaderVersion || short.Reject.Header.Version != 2 ||
		r == nil || r.HeaderRead {
		t.Errorf("an agreed version 2: %v, %+v", err, r)
	}
}

// TestInspectNoiseResources (SECURITY.md section 9.5): Inspect on noise ends within the time bound
// and the heap bound, with nothing known, as Decode does (TestHostileRecordingResources): 2 frames
// at 848 x 480 always, and in the long version 32 at 3840 x 2160 with GOMAXPROCS 16 and 32.
func TestInspectNoiseResources(t *testing.T) {
	raceSkipStatistics(t)
	run := func(n, w, h int) {
		rec := writeRaw(t, noiseFrames(n, w, h, 9), 30)
		var r *InspectResult
		var err error
		d, peak := heapPeak(t, func() {
			r, err = Inspect(context.Background(), InspectConfig{Path: rec, Backend: video.NewRaw()})
		})
		var short *ShortError
		if !errors.As(err, &short) || r == nil || r.HeaderRead || short.Captures != int64(n) {
			t.Errorf("noise: %v, %+v", err, r)
		}
		t.Logf("inspect, %d noise frames at %d x %d, GOMAXPROCS %d: %.1f s, heap growth %.1f MiB", n, w, h, runtime.GOMAXPROCS(0),
			d.Seconds(), float64(peak)/(1<<20))
		if d > hostileTime || peak > hostileHeap {
			t.Errorf("%.1f s and %d bytes of heap growth, bounds %v and %d", d.Seconds(), peak, hostileTime, hostileHeap)
		}
	}
	if os.Getenv("QRVT_LONG_TESTS") != "1" {
		run(2, 848, 480)
		return
	}
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(0))
	for _, procs := range []int{16, 32} {
		runtime.GOMAXPROCS(procs)
		run(32, 3840, 2160)
	}
}
