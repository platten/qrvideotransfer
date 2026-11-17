package pipeline

import (
	"context"
	"math"
	"path/filepath"
	"testing"

	"github.com/platten/qrvideotransfer/internal/payload"
	"github.com/platten/qrvideotransfer/internal/prng"
	"github.com/platten/qrvideotransfer/internal/video"
)

// truthOf runs the vision chain on every frame of a perfect recording and returns the transfer's
// parameters and, per code frame, its codeword-ordered soft bits (all confident and right).
func truthOf(t *testing.T, path string) (params, map[uint32][]int8) {
	t.Helper()
	frames, _ := readFrames(t, video.NewRaw(), path)
	var w captureWorker
	var key params
	truth := map[uint32][]int8{}
	for i, f := range frames {
		r := w.process(f, i)
		if !r.located || len(r.phases) != 1 {
			t.Fatalf("frame %d: located %v, %d phases", i, r.located, len(r.phases))
		}
		key = r.key
		truth[r.phases[0].frame] = r.phases[0].soft
	}
	return key, truth
}

// awgn returns soft bits of a BPSK channel at the given Es/N0 for the signs of ref: each bit's LLR
// in quarter nats (modem.LLRScale), 2 y / sigma^2 with y = +-1 + noise, rounded and clipped.
func awgn(g *prng.SplitMix64, ref []int8, esn0dB float64) []int8 {
	sigma := math.Sqrt(1 / (2 * math.Pow(10, esn0dB/10)))
	out := make([]int8, len(ref))
	for i, r := range ref {
		u1 := (float64(g.Next()>>11) + 0.5) / (1 << 53)
		u2 := float64(g.Next()>>11) / (1 << 53)
		s := 1.0
		if r < 0 {
			s = -1
		}
		y := s + sigma*math.Sqrt(-2*math.Log(u1))*math.Cos(2*math.Pi*u2)
		out[i] = int8(max(-127, min(127, math.Round(4*2*y/(sigma*sigma)))))
	}
	return out
}

// TestSoftBitsAddUp: two recordings whose soft bits are each too weak for the inner code decode
// together, because the soft bits of each code frame are summed across recordings (DESIGN.md
// section 7, rule 7). The captures' soft bits are made directly, as a BPSK channel at an Es/N0 of
// -1.5 dB on the true codeword bits: the vision chain is bypassed because, under the simulator's
// noise, the detector and the crediting of cells give out before the inner code does, so a
// filmed recording cannot isolate the summing. One capture per frame and recording: alone that is
// far below the `normal` code's threshold (0.85 dB at FER 1e-2, DESIGN.md section 8), summed it
// is 1.5 dB. The descriptor, summed over the four frames of the first recording, decodes at once.
func TestSoftBitsAddUp(t *testing.T) {
	in, want := writeInputs(t, 4, 6000)
	path, res := encodeRaw(t, profile480(in))
	key, truth := truthOf(t, path)
	g := prng.SplitMix64{State: 12}
	cfg := &DecodeConfig{Unpack: payload.UnpackOptions{OutputDir: filepath.Join(t.TempDir(), "out")}}
	d := &decoder{c: cfg, tmp: t.TempDir(), frames: map[uint32]*frameAcc{}, softCap: MaxSoftBytes}
	defer d.close()
	index := 0
	record := func() {
		t.Helper()
		for f := range uint32(res.Frames) {
			r := captureResult{index: index, located: true, key: key, cells: 1, clean: 1,
				phases: []phaseBits{{frame: f, soft: awgn(&g, truth[f], -1.5)}}}
			if err := d.apply(r); err != nil {
				t.Fatal(err)
			}
			index++
		}
		if err := d.flush(); err != nil {
			t.Fatal(err)
		}
		index += 2
	}
	record()
	if d.desc == nil {
		t.Fatal("the descriptor did not decode from four frames")
	}
	if d.complete() || d.stats.CodewordsOK > int64(res.Frames) {
		t.Fatalf("one recording alone decoded %d codewords", d.stats.CodewordsOK)
	}
	alone := d.stats.CodewordsOK
	record()
	if !d.complete() {
		t.Fatalf("two recordings: %d codewords, %d of %d blocks", d.stats.CodewordsOK, d.ready, d.plan.Blocks)
	}
	t.Logf("codewords decoded: %d after one recording, %d after two", alone, d.stats.CodewordsOK)
	stream, err := d.repair(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if _, err := payload.Unpack(context.Background(), stream, d.desc.StreamLen, cfg.Unpack); err != nil {
		t.Fatal(err)
	}
	checkOutput(t, cfg.Unpack.OutputDir, want)
}

// TestAdjacentCapturesHalf: a capture of the same frame as the capture just before it adds half
// its soft bits; one that is not adjacent adds them whole (DESIGN.md section 7, rule 7).
func TestAdjacentCapturesHalf(t *testing.T) {
	in, _ := writeInputs(t, 5, 3000)
	path, _ := encodeRaw(t, profile480(in))
	key, truth := truthOf(t, path)
	d := &decoder{c: &DecodeConfig{}, tmp: t.TempDir(), frames: map[uint32]*frameAcc{}, softCap: MaxSoftBytes}
	defer d.close()
	g := d.geomFor(t, key)
	soft := make([]int8, len(truth[1]))
	for i := range soft {
		soft[i] = 20
	}
	for _, index := range []int{0, 1, 3} { // 1 follows 0; 3 does not follow 1
		if err := d.apply(captureResult{index: index, located: true, key: key, cells: 1, clean: 1,
			phases: []phaseBits{{frame: 1, soft: soft}}}); err != nil {
			t.Fatal(err)
		}
	}
	got := d.frames[1].soft[1][0]
	if got != 20+10+20 {
		t.Errorf("summed soft bit %d, want 50", got)
	}
	_ = g
}

// geomFor locks d onto key as a first capture would.
func (d *decoder) geomFor(t *testing.T, key params) *geometry {
	t.Helper()
	if err := d.apply(captureResult{index: -10, located: true, key: key, cells: 1, clean: 1}); err != nil {
		t.Fatal(err)
	}
	return d.geom
}

// TestAddSoftSaturates: sums saturate at -127 and 127.
func TestAddSoftSaturates(t *testing.T) {
	dst := []int8{120, -120, 0, -128}
	addSoft(dst, []int8{100, -100, -128, -128}, 2)
	if want := []int8{127, -127, -127, -127}; string(int8Bytes(dst)) != string(int8Bytes(want)) {
		t.Errorf("got %v, want %v", dst, want)
	}
	if neg(-128) != 127 || neg(5) != -5 {
		t.Error("neg")
	}
}

func int8Bytes(s []int8) []byte {
	b := make([]byte, len(s))
	for i, v := range s {
		b[i] = byte(v)
	}
	return b
}

// TestEvict: beyond the cap, the least confident frame's soft bits go first.
func TestEvict(t *testing.T) {
	in, _ := writeInputs(t, 6, 3000)
	path, _ := encodeRaw(t, profile480(in))
	key, truth := truthOf(t, path)
	d := &decoder{c: &DecodeConfig{}, tmp: t.TempDir(), frames: map[uint32]*frameAcc{}, softCap: MaxSoftBytes}
	defer d.close()
	weak := make([]int8, len(truth[0]))
	for i := range weak {
		weak[i] = 1
	}
	for i, f := range []uint32{0, 2} {
		soft := truth[f]
		if f == 0 {
			soft = weak
		}
		if err := d.apply(captureResult{index: 10 * i, located: true, key: key, cells: 1, clean: 1,
			phases: []phaseBits{{frame: f, soft: soft}}}); err != nil {
			t.Fatal(err)
		}
	}
	d.softCap = d.softBytes - 1
	d.evict()
	if d.frames[0].soft[1] != nil || d.frames[2].soft[1] == nil {
		t.Error("the confident frame was evicted, or the weak one kept")
	}
	if err := d.attempt(0, d.frames[0]); err != nil || d.frames[0].left != d.geom.cpf-1 {
		t.Errorf("an evicted frame's attempt: %v, %d left", err, d.frames[0].left)
	}
	d.softCap = -1
	d.evict()
	if d.softBytes != 0 {
		t.Errorf("%d bytes left", d.softBytes)
	}
}

// TestPhaseFrame: a phase maps to the header with that phase, else to the next or previous frame,
// wrapping at the seam; two steps away maps to nothing.
func TestPhaseFrame(t *testing.T) {
	cases := []struct {
		hs    []uint32
		p     int
		want  uint32
		found bool
	}{
		{[]uint32{5}, 1, 5, true}, {[]uint32{5}, 2, 6, true}, {[]uint32{5}, 0, 4, true}, {[]uint32{5}, 3, 0, false},
		{[]uint32{7}, 0, 0, true}, {[]uint32{0}, 3, 7, true}, {[]uint32{5, 6}, 2, 6, true}, {nil, 0, 0, false},
	}
	for _, c := range cases {
		if got, ok := phaseFrame(c.hs, c.p, 8); got != c.want || ok != c.found {
			t.Errorf("phaseFrame(%v, %d) = %d, %v; want %d, %v", c.hs, c.p, got, ok, c.want, c.found)
		}
	}
}
