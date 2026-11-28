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

// TestScanCaptures: Scan hands every capture to the sink in order, recording after recording, with
// what the vision chain found: the headers, the camera pixels per cell, the palette's clipped
// share, the presentation time and each code frame's soft bits by bit position, which, put in
// codeword order, are exactly the soft bits Decode sums. A sink that stops ends the scan.
func TestScanCaptures(t *testing.T) {
	in, _ := writeInputs(t, 5, 3000)
	path, res := encodeRaw(t, profile480(in))
	frames, _ := readFrames(t, video.NewRaw(), path)
	var w captureWorker
	want := make([]captureResult, len(frames))
	for i, f := range frames {
		want[i] = w.process(f, i)
	}
	var got []*Capture
	var events []Event
	sr, err := Scan(context.Background(), ScanConfig{Recordings: []string{path, path}, Backend: video.NewRaw(), Workers: 3,
		Progress: func(e Event) { events = append(events, e) }}, func(c *Capture) (bool, error) {
		got = append(got, c)
		return false, nil
	})
	if err != nil || sr.Captures != int64(2*len(frames)) || len(sr.Probes) != 2 || sr.Probes[0].Width != 848 {
		t.Fatalf("scan: %v, %+v", err, sr)
	}
	for i, c := range got {
		rec, k := i/len(frames), i%len(frames)
		g, err := newGeometry(want[k].key)
		if err != nil {
			t.Fatal(err)
		}
		if c.Recording != rec || c.Index != k || c.PTS != time.Duration(k)*time.Second/12 {
			t.Fatalf("capture %d: recording %d, index %d, PTS %v", i, c.Recording, c.Index, c.PTS)
		}
		if !c.Finders || !c.Located || len(c.Headers) == 0 || paramsOf(c.Headers[0]) != want[k].key ||
			c.Headers[0].FrameIndex != uint32(k)%uint32(res.Frames) || c.Cells != want[k].cells || c.Clean != want[k].clean {
			t.Fatalf("capture %d: %+v", i, c)
		}
		if c.PixelsPerCell < 3.9 || c.PixelsPerCell > 4.1 || c.PaletteClipped != 0 {
			t.Errorf("capture %d: %.3f pixels per cell (cells of 4), %.2f of the palette clipped", i, c.PixelsPerCell, c.PaletteClipped)
		}
		if len(c.Phases) != len(want[k].phases) {
			t.Fatalf("capture %d: %d phases, want %d", i, len(c.Phases), len(want[k].phases))
		}
		for j, ph := range c.Phases {
			cw := make([]int8, g.cpf*g.sent)
			g.gather(cw, ph.Soft, ph.Frame)
			if ph.Frame != want[k].phases[j].frame || !reflect.DeepEqual(cw, want[k].phases[j].soft) {
				t.Errorf("capture %d, phase %d: frame %d, soft bits differ from Decode's", i, j, ph.Frame)
			}
		}
	}
	progress := 0
	for _, e := range events {
		if e.Stage == StageDecodeVideo && e.Kind == KindProgress {
			progress++
		}
	}
	if progress != len(got) {
		t.Errorf("%d progress events for %d captures", progress, len(got))
	}

	n := 0
	sr, err = Scan(context.Background(), ScanConfig{Recordings: []string{path, path}, Backend: video.NewRaw()}, func(*Capture) (bool, error) {
		n++
		return n == 3, nil
	})
	if err != nil || n != 3 || sr.Captures != 3 || len(sr.Probes) != 1 {
		t.Errorf("a sink stopping at 3: %v, %d calls, %+v", err, n, sr)
	}
	boom := errors.New("boom")
	if _, err := Scan(context.Background(), ScanConfig{Recordings: []string{path}, Backend: video.NewRaw()}, func(*Capture) (bool, error) {
		return false, boom
	}); !errors.Is(err, boom) {
		t.Errorf("a failing sink: %v", err)
	}
	for _, c := range []ScanConfig{{Backend: video.NewRaw()}, {Recordings: []string{path}}} {
		if _, err := Scan(context.Background(), c, func(*Capture) (bool, error) { return false, nil }); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
	if _, err := Scan(context.Background(), ScanConfig{Recordings: []string{path}, Backend: video.NewRaw()}, nil); err == nil {
		t.Error("no sink accepted")
	}
}

// TestInspectEncoderFrames: Inspect reads the descriptor from the encoder's own frames, stopping at
// the first capture, with every field the encode wrote, the display rate (descriptor byte 37)
// included.
func TestInspectEncoderFrames(t *testing.T) {
	in, _ := writeInputs(t, 5, 3000)
	cfg := profile480(in)
	path, res := encodeRaw(t, cfg)
	var events []Event
	r, err := Inspect(context.Background(), InspectConfig{Path: path, Backend: video.NewRaw(), Progress: func(e Event) { events = append(events, e) }})
	if err != nil {
		t.Fatal(err)
	}
	d := r.Descriptor
	if d == nil || !r.HeaderRead || r.Stats.Captures != 1 {
		t.Fatalf("%+v", r)
	}
	if d.Session != res.Session || d.StreamLen != res.StreamLen || d.Blocks != res.Blocks || d.K != res.K || d.M != res.M ||
		d.Frames != res.Frames || d.CodewordsPerFrame != res.Codewords || d.DisplayFPS != 12 || d.ShardSize != 128 {
		t.Errorf("descriptor %+v, encode %+v", d, res)
	}
	h := r.Header
	if h.GridX != 212 || h.GridY != 120 || h.Modulation != 2 || h.InnerCode != 2 || int(h.TotalFrames) != res.Frames ||
		h.Session != uint16(res.Session) || r.Probe.Width != 848 || r.Probe.Height != 480 {
		t.Errorf("header %+v, probe %+v", h, r.Probe)
	}
	if len(events) == 0 || events[len(events)-1].Stage != StageDemodulate || events[len(events)-1].Kind != KindFinished {
		t.Errorf("events end with %+v", events[len(events)-1])
	}
	if _, err := Inspect(context.Background(), InspectConfig{Backend: video.NewRaw()}); err == nil {
		t.Error("no path accepted")
	}
	if _, err := Inspect(context.Background(), InspectConfig{Path: path}); err == nil {
		t.Error("no backend accepted")
	}
	if _, err := Inspect(context.Background(), InspectConfig{Path: path + ".missing", Backend: video.NewRaw()}); err == nil {
		t.Error("a missing file accepted")
	}
}

// TestInspectStopsEarly: on a filmed recording Inspect stops once the descriptor decodes, long
// before the recording ends, where Decode reads on. Statistical: skipped under the race detector.
func TestInspectStopsEarly(t *testing.T) {
	raceSkipStatistics(t)
	tr := rateCode(t, 2, 2, 480, 500)
	c := preset(t, "tripod", 908, 908)
	rec := filmFrames(t, tr.frames, 30, 4, c, channel.Run{Start: time.Second / 30 / 8, Captures: 6 * tr.res.Frames, Seed: 0x5eed + 1})
	r, err := Inspect(context.Background(), InspectConfig{Path: rec, Backend: video.NewRaw(), Workers: 4})
	if err != nil || r.Descriptor == nil || r.Descriptor.Session != tr.res.Session {
		t.Fatalf("%v, %+v", err, r)
	}
	t.Logf("%d of %d captures read", r.Stats.Captures, 6*tr.res.Frames)
	if r.Stats.Captures > 3 {
		t.Errorf("%d captures read before the descriptor decoded, want at most 3 of %d", r.Stats.Captures, 6*tr.res.Frames)
	}
}

// TestInspectHeadersOnly: a recording whose headers are read but whose descriptor is not ends in a
// ShortError with the failure diagnostics and the header read; one that agrees on an unsupported
// format version ends in the agreed refusal (exit 5 through the root package), as Decode's.
func TestInspectHeadersOnly(t *testing.T) {
	in, _ := writeInputs(t, 5, 3000)
	path, res := encodeRaw(t, profile480(in))
	frames, _ := readFrames(t, video.NewRaw(), path)
	g := prng.SplitMix64{State: 3}
	// Noise of +-40 levels leaves the headers readable and nothing else: measured over seeds 1 to 4, at
	// +-30 to +-50 every seed read headers and no descriptor; at +-20 one decoded it, at +-60 one read
	// no header.
	for _, f := range frames {
		for i := range f.Y {
			f.Y[i] = uint16(max(0, min(255, int(f.Y[i])+int(g.Next()%81)-40)))
		}
	}
	rec := writeRaw(t, frames[:2], 12)
	r, err := Inspect(context.Background(), InspectConfig{Path: rec, Backend: video.NewRaw()})
	var short *ShortError
	if !errors.As(err, &short) || r == nil || !r.HeaderRead || r.Descriptor != nil || int(r.Header.TotalFrames) != res.Frames {
		t.Fatalf("%v, %+v", err, r)
	}
	if short.Captures != 2 || short.HeadersRead == 0 || short.Descriptor || len(short.Blocks) != 0 || short.FramesTotal != res.Frames {
		t.Errorf("%+v", short)
	}
	_, _, derr := decodeTo(t, rec)
	var dshort *ShortError
	if !errors.As(derr, &dshort) || !reflect.DeepEqual(dshort, short) {
		t.Errorf("Decode's shortfall %+v, Inspect's %+v", derr, short)
	}

	// Stubbed captures, as TestShortDiagnostics.
	key, _ := fixture(t)
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
// and the heap bound, with nothing known, as Decode does (TestHostileRecordingResources): 16 frames
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
		run(16, 848, 480)
		return
	}
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(0))
	for _, procs := range []int{16, 32} {
		runtime.GOMAXPROCS(procs)
		run(32, 3840, 2160)
	}
}
