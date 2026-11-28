package qrvt

import (
	"context"
	"errors"
	"testing"

	"github.com/platten/qrvideotransfer/internal/prng"
)

// TestInspect: Inspect on the encoder's own frames reports every field of StreamInfo, the display
// rate from the stream descriptor among them.
func TestInspect(t *testing.T) {
	in, _ := payloadDir(t, 31, 3000)
	path, rep := encodeTest(t, EncodeOptions{Inputs: []string{in}, Profile: profile480, Compression: CompressNone})
	var events int
	si, err := Inspect(context.Background(), InspectOptions{Path: path, Backend: testSpec, Progress: func(Event) { events++ }})
	if err != nil {
		t.Fatal(err)
	}
	if events == 0 {
		t.Error("no events")
	}
	want := StreamInfo{Session: rep.Session, Width: 848, Height: 480, GridX: 212, GridY: 120, Modulation: Gray4, Protection: Normal,
		Frames: rep.Frames, FPS: 12, StreamLen: rep.StreamLen, ShardSize: 128, Blocks: rep.Blocks, K: rep.K, M: rep.M,
		StreamHash: si.StreamHash, Descriptor: true}
	if *si != want {
		t.Errorf("got %+v\nwant %+v", *si, want)
	}
	if si.StreamHash == [16]byte{} {
		t.Error("no stream hash")
	}
	if _, err := Inspect(context.Background(), InspectOptions{Backend: testSpec}); err == nil {
		t.Error("no path accepted")
	}
	if _, err := Inspect(context.Background(), InspectOptions{Path: path, Backend: "/nonexistent/ffmpeg"}); err == nil {
		t.Error("a missing ffmpeg accepted")
	}
}

// TestInspectPartial: a recording whose frame headers are read but whose stream descriptor is not
// gives the header's fields (Descriptor false) beside a NotEnoughDataError with the failure
// diagnostics; noise gives the error alone.
func TestInspectPartial(t *testing.T) {
	in, _ := payloadDir(t, 32, 3000)
	path, rep := encodeTest(t, EncodeOptions{Inputs: []string{in}, Profile: profile480, Compression: CompressNone})
	frames := readRaw(t, path)
	g := prng.SplitMix64{State: 3}
	for _, f := range frames { // +-40 levels: headers read, nothing else (internal/pipeline's TestInspectHeadersOnly)
		for i := range f.Y {
			f.Y[i] = uint16(max(0, min(255, int(f.Y[i])+int(g.Next()%81)-40)))
		}
	}
	rec := writeRawFrames(t, frames[:2], 12)
	si, err := Inspect(context.Background(), InspectOptions{Path: rec, Backend: testSpec})
	var short *NotEnoughDataError
	if !errors.As(err, &short) || short.Captures != 2 || short.HeadersRead == 0 || short.Descriptor || short.FramesTotal != rep.Frames {
		t.Fatalf("%v (%+v)", err, short)
	}
	want := &StreamInfo{Width: 848, Height: 480, GridX: 212, GridY: 120, Modulation: Gray4, Protection: Normal, Frames: rep.Frames}
	if si == nil || *si != *want {
		t.Errorf("partial %+v, want %+v", si, want)
	}

	noise := readRaw(t, path)[:4]
	for _, f := range noise {
		for i := range f.Y {
			f.Y[i] = uint16(g.Next() & 0xff)
		}
	}
	si, err = Inspect(context.Background(), InspectOptions{Path: writeRawFrames(t, noise, 12), Backend: testSpec})
	if !errors.As(err, &short) || si != nil || short.HeadersRead != 0 || short.Captures != 4 {
		t.Errorf("noise: %+v, %v", si, err)
	}
}
