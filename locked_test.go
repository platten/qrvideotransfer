package qrvt

import (
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/channel"
	"github.com/platten/qrvideotransfer/internal/prng"
)

// TestLockedRateNotEnoughData is the always-on safety case of DESIGN.md section 15's equal-rate row:
// a camera locked to the display rate (30 against 30 fps, 4 ms exposure, 8 ms readout) at a phase
// offset where every pass repeats the same frame mixing does not decode, and the public Decode
// then returns a NotEnoughDataError and writes nothing. The guidance sentence the command line adds
// to that error is asserted in internal/cli (TestGuidance and TestDecodeFailureGuidance); the two
// together cover the row's safety property. A 500-byte transfer (F = 4) at 848 x 480, filmed for
// one pass at offset 1/8 by `tripod` at 1600 x 900, falls short by 7 shards (the 30,000-byte
// transfers of the long sweep decoded no codeword there in 4 passes). The long sweep over every
// modulation, protection, exposure and offset is internal/pipeline's TestLockedRateSafety.
// Statistical: skipped under the race detector.
func TestLockedRateNotEnoughData(t *testing.T) {
	if raceEnabled {
		t.Skip("asserted in the non-race test step")
	}
	defer func() { testRandom = nil }()
	testRandom = &prngReader{prng.SplitMix64{State: 0x10c4}}
	in, _ := payloadDir(t, 41, 500)
	p := profile480
	p.FPS = 30
	path, rep := encodeTest(t, EncodeOptions{Inputs: []string{in}, Profile: p})
	c, err := channel.Preset("tripod")
	if err != nil {
		t.Fatal(err)
	}
	c = c.Resize(1600, 900)
	c.Timing.FPS, c.Timing.Exposure, c.Timing.Readout = 30, 4*time.Millisecond, 8*time.Millisecond
	rec := film(t, readRaw(t, path), 30, c, channel.Run{Start: time.Second / 30 / 8, Captures: rep.Frames, Seed: 0x5eed + 1}, false)
	out, dr, err := decodeTest(t, DecodeOptions{Recordings: []string{rec}})
	var short *NotEnoughDataError
	if !errors.As(err, &short) {
		t.Fatalf("got %v, want a NotEnoughDataError", err)
	}
	if dr != nil {
		t.Errorf("a report beside the error: %+v", dr)
	}
	n := 0
	_ = filepath.WalkDir(out, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	if n != 0 {
		t.Errorf("%d files written", n)
	}
	t.Logf("F %d: %v", rep.Frames, err)
}
