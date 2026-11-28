package qrvt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/platten/qrvideotransfer/internal/prng"
	"github.com/platten/qrvideotransfer/internal/video"
)

// TestInspect: Inspect on the encoder's own frames reports every field of StreamInfo, the display
// rate from the stream descriptor among them; frames whose headers are read but whose stream
// descriptor is not give the header's fields (Descriptor false) beside a NotEnoughDataError with
// the failure diagnostics (noise: TestInspectNoise). The smallest setting, a 480 x 480 code of
// 500 bytes (F = 4), since each capture costs a run of the vision chain.
func TestInspect(t *testing.T) {
	in, _ := payloadDir(t, 31, 500)
	p := Profile{Width: 480, Height: 480, Cell: 4, FPS: 30, Modulation: Gray4, Protection: Normal, ParityPercent: 20}
	path, rep := encodeTest(t, EncodeOptions{Inputs: []string{in}, Profile: p, Compression: CompressNone})
	frames := readRaw(t, path)
	events := 0 // the first frame alone: a capture read ahead would cost a run of the vision chain
	si, err := Inspect(context.Background(), InspectOptions{Path: writeRawFrames(t, frames[:1], 30), Backend: testSpec, Workers: 1,
		Progress: func(Event) { events++ }})
	if err != nil {
		t.Fatal(err)
	}
	want := StreamInfo{Session: rep.Session, Width: 480, Height: 480, GridX: 120, GridY: 120, Modulation: Gray4, Protection: Normal,
		Frames: rep.Frames, FPS: 30, StreamLen: rep.StreamLen, ShardSize: 128, Blocks: rep.Blocks, K: rep.K, M: rep.M,
		StreamHash: si.StreamHash, Descriptor: true}
	if *si != want || si.StreamHash == [16]byte{} || events == 0 {
		t.Errorf("got %+v, %d events\nwant %+v", *si, events, want)
	}
	if _, err := Inspect(context.Background(), InspectOptions{Backend: testSpec}); err == nil {
		t.Error("no path accepted")
	}
	if _, err := Inspect(context.Background(), InspectOptions{Path: path, Backend: "/nonexistent/ffmpeg"}); err == nil {
		t.Error("a missing ffmpeg accepted")
	}

	// +-40 levels of noise: headers read, nothing else (internal/pipeline's TestInspectHeadersOnly).
	frames = frames[:1]
	g := prng.SplitMix64{State: 3}
	for _, f := range frames {
		for i := range f.Y {
			f.Y[i] = uint16(max(0, min(255, int(f.Y[i])+int(g.Next()%81)-40)))
		}
	}
	si, err = Inspect(context.Background(), InspectOptions{Path: writeRawFrames(t, frames, 30), Backend: testSpec, Workers: 1})
	var short *NotEnoughDataError
	if !errors.As(err, &short) || short.Captures != 1 || short.HeadersRead != 1 || short.Descriptor || short.FramesTotal != rep.Frames {
		t.Fatalf("%v (%+v)", err, short)
	}
	partial := &StreamInfo{Width: 480, Height: 480, GridX: 120, GridY: 120, Modulation: Gray4, Protection: Normal, Frames: rep.Frames}
	if si == nil || *si != *partial {
		t.Errorf("partial %+v, want %+v", si, partial)
	}
}

// inspectNoiseAlloc is the ceiling on what TestInspectNoise's Inspect may allocate in all,
// generous: 14.4 MiB were measured (linux amd64, 2026-11-28).
const inspectNoiseAlloc = 128 << 20

// TestInspectNoise: Inspect is an entry point for hostile input, so a short noise recording at the
// smallest size, 3 captures at 480 x 480, is inspected always (the 60 s and 1 GiB bound at 4K is
// internal/pipeline's long-only TestInspectNoiseResources). It ends in the typed NotEnoughDataError
// with nothing known and a nil StreamInfo, as documented when no header is read; it makes no shard
// store and writes nothing, in the temporary folder or beside the recording; and it allocates
// under a generous ceiling.
func TestInspectNoise(t *testing.T) {
	g := prng.SplitMix64{State: 9}
	var noise []*video.Frame
	for range 3 {
		f := &video.Frame{W: 480, H: 480, Bits: 8, Y: make([]uint16, 480*480), Cb: make([]uint16, 240*240), Cr: make([]uint16, 240*240)}
		for _, p := range [][]uint16{f.Y, f.Cb, f.Cr} {
			for i := range p {
				p[i] = uint16(g.Next() & 0xff)
			}
		}
		noise = append(noise, f)
	}
	rec := writeRawFrames(t, noise, 30)
	tmp := t.TempDir() // the temporary folder Inspect would use, were it to write
	for _, v := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(v, tmp)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	si, err := Inspect(context.Background(), InspectOptions{Path: rec, Backend: testSpec})
	runtime.ReadMemStats(&after)
	var short *NotEnoughDataError
	if !errors.As(err, &short) || si != nil || short.Captures != 3 || short.HeadersRead != 0 || short.Descriptor ||
		len(short.Blocks) != 0 || short.FramesTotal != 0 {
		t.Errorf("noise: %+v, %v (%+v)", si, err, short)
	}
	if left, err := os.ReadDir(tmp); err != nil || len(left) != 0 {
		t.Errorf("the temporary folder holds %v (%v)", left, err)
	}
	if beside, err := os.ReadDir(filepath.Dir(rec)); err != nil || len(beside) != 1 {
		t.Errorf("beside the recording: %v (%v)", beside, err)
	}
	alloc := after.TotalAlloc - before.TotalAlloc
	t.Logf("3 noise captures at 480 x 480: %.1f MiB allocated", float64(alloc)/(1<<20))
	if alloc > inspectNoiseAlloc {
		t.Errorf("%d bytes allocated, ceiling %d", alloc, inspectNoiseAlloc)
	}
}
