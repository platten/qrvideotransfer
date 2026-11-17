package main

import (
	"math"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/modem"
	"github.com/platten/qrvideotransfer/internal/video"
	"github.com/platten/qrvideotransfer/internal/vision"
)

// TestExpectedBits: the bits the analysis expects of data cell e in code frame idx are those the
// painter paints. Each data cell's centre pixel of a rendered frame is classified to the nearest
// level colour, the level turned back into its symbol, and the symbol's bits compared with
// expectedBit, for every modulation, two frame indices and two cell sizes.
func TestExpectedBits(t *testing.T) {
	for _, cell := range []int{8, 12} {
		pl := makePlan(960, 720, 1)
		for _, mod := range []uint8{1, 2, 3} {
			pt, err := newPainter(pl.Width, pl.Height, cell, mod, pl.TotalFrames)
			if err != nil {
				t.Fatal(err)
			}
			g := newSegGeom(pt.header(0))
			buf := make([]byte, pt.frameSize())
			for _, idx := range []uint32{0, 7} {
				pt.render(idx, buf)
				w, h := pl.Width, pl.Height
				wrong := 0
				for e, c := range pt.layout.DataCells() {
					x, y := int(c.X)*cell+cell/2, int(c.Y)*cell+cell/2
					px := ycc{buf[y*w+x], buf[w*h+(y/2)*(w/2)+x/2], buf[w*h+(w/2)*(h/2)+(y/2)*(w/2)+x/2]}
					lv := nearestLevel(pt.levels, px)
					sym := levelSymbol(mod, lv)
					for k := range g.b {
						want := uint8(sym >> (g.b - 1 - k) & 1)
						if got := g.expectedBit(idx, e*g.b+k); got != want {
							wrong++
						}
					}
				}
				if wrong != 0 {
					t.Errorf("cell %d, modulation %d, frame %d: %d bits differ from the painted symbols", cell, mod, idx, wrong)
				}
			}
		}
	}
}

func nearestLevel(levels []ycc, px ycc) int {
	best, bd := 0, math.MaxFloat64
	for i, c := range levels {
		d := 0.0
		for k := range 3 {
			v := float64(c[k]) - float64(px[k])
			d += v * v
		}
		if d < bd {
			best, bd = i, d
		}
	}
	return best
}

// levelSymbol inverts symbolLevel.
func levelSymbol(mod uint8, lv int) int {
	for v := range 1 << modBits(mod) {
		if symbolLevel(mod, v) == lv {
			return v
		}
	}
	return -1
}

func modBits(mod uint8) int { c, _ := modem.ByID(mod); return c.BitsPerCell() }

// TestLocate: a header is placed in the plan whose frame count, segment grid and modulation it
// matches; a frame index of the 1080p plan with a 4K plan's grid, a foreign session, a grid that
// disagrees with the segment and an index past the end are not placed.
func TestLocate(t *testing.T) {
	plans := []plan{makePlan(1920, 1080, 4), makePlan(3840, 2160, 4), makePlan(2560, 1440, 4)}
	h := func(pl plan, si int, off uint32) frame.Header {
		s := pl.Segments[si]
		return frame.Header{Version: frame.Version, Session: session, FrameIndex: s.FirstIndex + off, TotalFrames: pl.TotalFrames,
			GridX: uint16(s.GridX), GridY: uint16(s.GridY), Modulation: s.Modulation, InnerCode: innerCode}
	}
	for pi, pl := range plans {
		for si := range pl.Segments {
			for _, off := range []uint32{0, uint32(pl.Segments[si].CodeFrames - 1)} {
				gp, gs, ok := locate(plans, h(pl, si, off))
				if !ok || gp != pi || gs != si {
					t.Errorf("plan %d segment %d offset %d: located %d %d %v", pi, si, off, gp, gs, ok)
				}
			}
		}
	}
	bad := h(plans[0], 0, 3)
	bad.GridX, bad.GridY = 240, 135 // a grid of the plan, not of this segment
	if _, _, ok := locate(plans, bad); ok {
		t.Error("a grid that disagrees with the segment was placed")
	}
	bad = h(plans[0], 0, 3)
	bad.Session = 1
	if _, _, ok := locate(plans, bad); ok {
		t.Error("a foreign session was placed")
	}
	bad = h(plans[0], 0, 3)
	bad.Modulation = 3
	if _, _, ok := locate(plans, bad); ok {
		t.Error("a modulation that disagrees with the segment was placed")
	}
	bad = h(plans[0], 0, 3)
	bad.FrameIndex = plans[0].TotalFrames
	if _, _, ok := locate(plans, bad); ok {
		t.Error("an index past the end was placed")
	}
}

// TestPhaseFrame: a phase (frame index mod 4) is given the header's own index when it matches, the
// next or the previous index when it is one step away, and nothing when it is two away (the next and
// the previous are equally likely) or would precede frame 0. A second header decides for its own
// phase.
func TestPhaseFrame(t *testing.T) {
	for _, k := range []struct {
		hs   []uint32
		p    int
		want uint32
		ok   bool
	}{
		{[]uint32{9}, 1, 9, true},
		{[]uint32{9}, 2, 10, true},
		{[]uint32{9}, 0, 8, true},
		{[]uint32{9}, 3, 0, false},
		{[]uint32{0}, 3, 0, false},
		{[]uint32{9, 11}, 3, 11, true},
		{[]uint32{9, 10}, 2, 10, true},
	} {
		got, ok := phaseFrame(k.hs, k.p)
		if ok != k.ok || ok && got != k.want {
			t.Errorf("headers %v phase %d: %d %v, want %d %v", k.hs, k.p, got, ok, k.want, k.ok)
		}
	}
}

// TestFrameAccounting: soft bits from the captures of one code frame are summed per bit and judged
// against the expected bits: a bit is stated when its sum is not zero and wrong when its sign
// disagrees; a cell counts as clean when some capture credited it. Delivered bits per cell are
// b x stated share x (1 - H2(error rate among stated bits)).
func TestFrameAccounting(t *testing.T) {
	const cells, b = 4, 2
	exp := []uint8{0, 1, 1, 0, 0, 0, 1, 1}
	a := newFrameAcc(cells, b, 0)
	// Capture 1 credits cells 0 and 1, both right.
	a.add([]int8{20, -20, -20, 20, 0, 0, 0, 0}, []bool{true, true, false, false}, 0)
	// Capture 2 credits cells 1 and 2; cell 2's second bit is wrong, cell 1 adds to capture 1.
	a.add([]int8{0, 0, -5, 5, 30, -30, 0, 0}, []bool{false, true, true, false}, 1)
	r := a.finish(func(q int) uint8 { return exp[q] })
	if r.cells != cells || r.clean != 3 || r.stated != 6 || r.wrong != 1 || r.captures != 2 {
		t.Fatalf("record %+v", r)
	}
	if got, want := r.delivered(b), 2*0.75*(1-h2(1.0/6)); math.Abs(got-want) > 1e-12 {
		t.Errorf("delivered %.6f, want %.6f", got, want)
	}
	// A bit whose two captures cancel is not stated.
	c := newFrameAcc(1, 1, 5)
	c.add([]int8{10}, []bool{true}, 5)
	c.add([]int8{-10}, []bool{true}, 6)
	if r := c.finish(func(int) uint8 { return 0 }); r.stated != 0 || r.clean != 1 || r.delivered(1) != 0 {
		t.Errorf("cancelled bit: %+v", r)
	}
}

// TestSegmentAccounting: frames of a segment group into passes by capture index; within a pass every
// index between the first and the last seen counts, and an index never seen delivers nothing. Two
// passes over the same indices count twice.
func TestSegmentAccounting(t *testing.T) {
	rec := func(idx uint32, first int, stated int) frameRec {
		return frameRec{idx: idx, firstCap: first, cells: 10, clean: 10, stated: stated, captures: 1}
	}
	full := 20                                                                  // 10 cells x 2 bits, all stated, none wrong: 2 bits per cell
	recs := []frameRec{rec(100, 0, full), rec(101, 2, full), rec(103, 6, full), // 102 missed
		rec(100, 1000, full), rec(101, 1002, full)} // a second pass
	s := summarise(recs, 2, 60)
	if s.Expected != 6 || s.Seen != 5 || s.Passes != 2 {
		t.Fatalf("summary %+v", s)
	}
	if want := 2.0 * 5 / 6; math.Abs(s.Delivered-want) > 1e-12 {
		t.Errorf("delivered %.4f, want %.4f", s.Delivered, want)
	}
	if math.Abs(s.CleanFrame-5.0/6) > 1e-12 {
		t.Errorf("clean share per code frame %.4f, want %.4f", s.CleanFrame, 5.0/6)
	}
}

// TestCalibration: confidence buckets as in the honesty tests (ten buckets of |s|, stated error
// 1 / (1 + e^(|s| / LLRScale))), split by the phase weight of the cell.
// TestTimeOffset: the recording's offset into the pattern is the median over the placed captures of
// pattern time less capture time, taken modulo the pattern's length so that a loop counts as one
// offset; a capture is then placed in the segment showing at its time plus the offset.
func TestTimeOffset(t *testing.T) {
	pl := makePlan(1920, 1080, 4) // 108 s
	// Pattern times of captures at 0.5, 1.0, ... with the recording starting 3 s into the pattern;
	// one stray value, and one capture from the second loop (pattern time wrapped).
	var pts, pat []float64
	for i := 1; i <= 20; i++ {
		pts, pat = append(pts, float64(i)/2), append(pat, float64(i)/2+3)
	}
	pts, pat = append(pts, 50), append(pat, 99)
	pts, pat = append(pts, 106), append(pat, 1)
	off := patternOffset(pts, pat, pl.duration())
	if math.Abs(off-3) > 1e-9 {
		t.Fatalf("offset %.3f, want 3", off)
	}
	for _, k := range []struct {
		t    float64
		want int
	}{{0, 0}, {1.5, 1}, {9.5, 3}, {105.5, 0}, {104.9, 26}} {
		if got := pl.segmentAt(k.t + off); got != k.want {
			t.Errorf("capture at %.1f s: segment %d, want %d", k.t, got, k.want)
		}
	}
}

func TestCalibration(t *testing.T) {
	var c calTable
	c.add(100, 0)
	c.add(-100, 0)
	c.add(5, 1)
	if c.N[100*calBuckets/128] != 2 || c.Errs[100*calBuckets/128] != 1 || c.N[0] != 1 || c.Errs[0] != 1 {
		t.Fatalf("table %+v", c)
	}
	if want := 2 / (1 + math.Exp(100.0/modem.LLRScale)); math.Abs(c.Stated[7]-want) > 1e-15 {
		t.Errorf("stated %.3g, want %.3g", c.Stated[7], want)
	}
}

// TestAnalyseRendered runs the whole chain on synthetic captures: code frames of a 960 x 720 plan
// rendered by the painter (cell 8, so a 120 x 90 grid) and placed in a larger grey picture, as a
// camera filming the screen head-on with no blur would see them, two captures per code frame. Every
// capture is located, placed in its segment and fully credited; every bit is stated and right, so
// each modulation delivers its bits per cell, and the clean share is 1.
func TestAnalyseRendered(t *testing.T) {
	pl := makePlan(960, 720, 1)
	a := newAnalyser([]plan{pl})
	agg := newAggregator(a, 30)
	var det vision.FinderDetector
	index := 0
	for si, s := range pl.Segments {
		if s.Cell != 8 || s.Rate != 12 {
			continue
		}
		pt, err := newPainter(pl.Width, pl.Height, s.Cell, s.Modulation, pl.TotalFrames)
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, pt.frameSize())
		for k := range uint32(2) {
			pt.render(s.FirstIndex+k, buf)
			f := placeRendered(buf, pl.Width, pl.Height, 1200, 900)
			for j := range 2 {
				// Pattern time of the code frame, plus 1/30 s per capture: a camera at 30 fps
				// filming a pattern that started 1 s before the recording.
				f.PTS = time.Duration((s.Start + float64(k)/float64(s.Rate) + float64(j)/30 - 1) * float64(time.Second))
				r := a.capture(f, &det, index)
				if !r.located || r.plan != 0 || r.seg != si {
					t.Fatalf("segment %d frame %d: capture located %v in plan %d segment %d", si, k, r.located, r.plan, r.seg)
				}
				if r.clean != r.cells || r.clipped != 0 {
					t.Errorf("segment %d frame %d: %d of %d cells clean, %d clipped", si, k, r.clean, r.cells, r.clipped)
				}
				if math.Abs(r.pxPerCell-8) > 0.1 {
					t.Errorf("segment %d: %.2f camera pixels per cell, want 8", si, r.pxPerCell)
				}
				agg.add(r)
				index++
			}
		}
	}
	rep := agg.finish()
	if len(rep.Segments) != 3 {
		t.Fatalf("%d segments reported, want 3", len(rep.Segments))
	}
	for _, sr := range rep.Segments {
		b := modBits(pl.Segments[sr.Seg].Modulation)
		if sr.Sum.Expected != 2 || sr.Sum.Seen != 2 || math.Abs(sr.Sum.Delivered-float64(b)) > 1e-9 || sr.Sum.CleanFrame != 1 {
			t.Errorf("segment %d: %+v, want 2 frames seen and %d bits per cell", sr.Seg, sr.Sum, b)
		}
		if sr.Captures != 4 || sr.ByTime != 4 || sr.Located != 1 || math.Abs(sr.RateRatio-2.5) > 1e-9 {
			t.Errorf("segment %d: %d captures located of %d by time, camera-to-display ratio %.2f", sr.Seg, sr.Captures, sr.ByTime, sr.RateRatio)
		}
		// 4 captures at 30 fps show 1.6 code frames at 12 fps; 2 were seen, so they count.
		if math.Abs(sr.DeliveredAll-float64(b)) > 1e-9 {
			t.Errorf("segment %d: %.3f bits per cell over every frame shown, want %d", sr.Seg, sr.DeliveredAll, b)
		}
	}
}

// placeRendered puts a rendered yuv420p frame of w x h in the middle of a mid-grey 8-bit capture of
// cw x ch, its brightness scaled by 0.9 above black, as a camera exposing white below its clip.
func placeRendered(buf []byte, w, h, cw, ch int) *video.Frame {
	f := &video.Frame{W: cw, H: ch, Bits: 8, Y: make([]uint16, cw*ch), Cb: make([]uint16, cw/2*(ch/2)), Cr: make([]uint16, cw/2*(ch/2))}
	for i := range f.Y {
		f.Y[i] = uint16(midGrey[0])
	}
	for i := range f.Cb {
		f.Cb[i], f.Cr[i] = uint16(midGrey[1]), uint16(midGrey[2])
	}
	x0, y0 := (cw-w)/2&^1, (ch-h)/2&^1
	for y := range h {
		for x := range w {
			f.Y[(y0+y)*cw+x0+x] = uint16(16 + 0.9*float64(int(buf[y*w+x])-16))
		}
	}
	u, v := buf[w*h:], buf[w*h+(w/2)*(h/2):]
	for y := range h / 2 {
		for x := range w / 2 {
			f.Cb[(y0/2+y)*(cw/2)+x0/2+x] = uint16(u[y*(w/2)+x])
			f.Cr[(y0/2+y)*(cw/2)+x0/2+x] = uint16(v[y*(w/2)+x])
		}
	}
	return f
}
