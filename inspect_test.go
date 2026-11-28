package qrvt

import (
	"context"
	"errors"
	"testing"

	"github.com/platten/qrvideotransfer/internal/prng"
	"github.com/platten/qrvideotransfer/internal/video"
)

// TestInspect: Inspect on the encoder's own frames reports every field of StreamInfo, the display
// rate from the stream descriptor among them; frames whose headers are read but whose stream
// descriptor is not give the header's fields (Descriptor false) beside a NotEnoughDataError with
// the failure diagnostics; noise gives the error alone. The smallest setting, a 480 x 480 code of
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

	noise := []*video.Frame{{W: 128, H: 128, Bits: 8, Y: make([]uint16, 128*128), Cb: make([]uint16, 64*64), Cr: make([]uint16, 64*64)}}
	for i := range noise[0].Y {
		noise[0].Y[i] = uint16(g.Next() & 0xff)
	}
	si, err = Inspect(context.Background(), InspectOptions{Path: writeRawFrames(t, noise, 30), Backend: testSpec})
	if !errors.As(err, &short) || si != nil || short.HeadersRead != 0 || short.Captures != 1 {
		t.Errorf("noise: %+v, %v", si, err)
	}
}
