package vision

import (
	"image"
	"math"
	"testing"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/video"
)

// TestHeaderPlain: every copy of a rendered frame decodes alone, at 16:9 and 16:10.
func TestHeaderPlain(t *testing.T) {
	for _, g := range []struct{ gx, gy, pitch int }{{212, 120, 4}, {432, 270, 4}, {480, 270, 8}} {
		c := geomNewCode(t, g.gx, g.gy, g.pitch, 2, 3, 6)
		f := geomPlain(c, 2)
		q := geomPlainQuad(c)
		hs, ok := SlotReader{}.Read(f, q)
		if !ok || len(hs) != 1 || hs[0] != c.headers[2] {
			t.Errorf("%dx%d: got %v %v, want %v", g.gx, g.gy, hs, ok, c.headers[2])
		}
		soft, ok := copySoft(f, q)
		if !ok {
			t.Fatalf("%dx%d: no soft bits", g.gx, g.gy)
		}
		for cp := range 4 {
			if h, ok := frame.DecodeHeader(soft[cp][:]); !ok || h != c.headers[2] {
				t.Errorf("%dx%d: copy %d alone gives %v %v", g.gx, g.gy, cp, h, ok)
			}
		}
	}
}

// geomPaintSlot paints slot s of copy cp of a rendered frame of c: light, dark, or mid-grey.
func geomPaintSlot(c *geomCode, f *video.Frame, cp, s int, v uint16) {
	r := c.layout.Slot(frame.Corner(cp), s/4, s%4).Pixels(c.pitch) //nolint:gosec // 0..3
	r = r.Intersect(image.Rect(0, 0, f.W, f.H))
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			f.Y[y*f.W+x] = v
		}
	}
}

// geomFlip inverts slot s of copy cp, given the header shown.
func geomFlip(c *geomCode, f *video.Frame, h frame.Header, cp, s int) {
	v := uint16(235)
	if h.Encode()[s] {
		v = 16
	}
	geomPaintSlot(c, f, cp, s, v)
}

// TestHeaderSplit: when the top copies show one code frame and the bottom copies another, both
// headers are returned, one each.
func TestHeaderSplit(t *testing.T) {
	c := geomNewCode(t, 212, 120, 4, 2, 2, 7)
	f := geomPlain(c, 0)
	half := f.W * f.H / 2
	copy(f.Y[half:], c.input.Frames[1].Y[half:])
	hs, ok := SlotReader{}.Read(f, geomPlainQuad(c))
	if !ok || len(hs) != 2 {
		t.Fatalf("got %v %v, want two headers", hs, ok)
	}
	if !(hs[0] == c.headers[0] && hs[1] == c.headers[1]) && !(hs[0] == c.headers[1] && hs[1] == c.headers[0]) {
		t.Errorf("got %v, want %v and %v", hs, c.headers[0], c.headers[1])
	}
}

// TestHeaderPairs: when copies 0 and 1 each have a different quarter of their slots inverted and
// copies 2 and 3 are blank, no copy decodes alone but the pair does.
func TestHeaderPairs(t *testing.T) {
	c := geomNewCode(t, 212, 120, 4, 2, 1, 8)
	f := geomPlain(c, 0)
	h := c.headers[0]
	for s := range frame.HeaderSlots {
		switch s % 4 {
		case 0:
			geomFlip(c, f, h, 0, s)
		case 1:
			geomFlip(c, f, h, 1, s)
		}
		geomPaintSlot(c, f, 2, s, 126)
		geomPaintSlot(c, f, 3, s, 126)
	}
	q := geomPlainQuad(c)
	soft, _ := copySoft(f, q)
	for cp := range 4 {
		if _, ok := frame.DecodeHeader(soft[cp][:]); ok {
			t.Fatalf("copy %d decodes alone; the test needs it not to", cp)
		}
	}
	hs, ok := SlotReader{}.Read(f, q)
	if !ok || len(hs) != 1 || hs[0] != h {
		t.Errorf("got %v %v, want %v", hs, ok, h)
	}
}

// TestHeaderAllFour: when each copy has two different eighths of its slots inverted, so that no copy
// and no pair decodes, the sum of all four does.
func TestHeaderAllFour(t *testing.T) {
	c := geomNewCode(t, 212, 120, 4, 2, 1, 9)
	f := geomPlain(c, 0)
	h := c.headers[0]
	for s := range frame.HeaderSlots {
		g := s % 8
		geomFlip(c, f, h, g/2, s) // groups 0-1 in copy 0, 2-3 in copy 1, ...
		geomFlip(c, f, h, (g/2+1)%4, s)
	}
	q := geomPlainQuad(c)
	soft, _ := copySoft(f, q)
	for a := range 4 {
		if _, ok := frame.DecodeHeader(soft[a][:]); ok {
			t.Fatalf("copy %d decodes alone; the test needs it not to", a)
		}
		for b := a + 1; b < 4; b++ {
			var sum [frame.HeaderSlots]int8
			for s := range sum {
				sum[s] = geomSat(int(soft[a][s]) + int(soft[b][s]))
			}
			if _, ok := frame.DecodeHeader(sum[:]); ok {
				t.Fatalf("pair %d,%d decodes; the test needs it not to", a, b)
			}
		}
	}
	hs, ok := SlotReader{}.Read(f, q)
	if !ok || len(hs) != 1 || hs[0] != h {
		t.Errorf("got %v %v, want %v", hs, ok, h)
	}
}

// geomSat clips v to -127..127.
func geomSat(v int) int8 { return int8(max(-127, min(127, v))) } //nolint:gosec // clipped

// TestHeaderNothing: a frame without a code, a quad off the frame, and malformed frames read nothing.
func TestHeaderNothing(t *testing.T) {
	c := geomNewCode(t, 212, 120, 4, 2, 1, 10)
	q := geomPlainQuad(c)
	blank := geomPlain(c, 0)
	for i := range blank.Y {
		blank.Y[i] = 126
	}
	off := q
	for k := range off.Centres {
		off.Centres[k].X += 1e5
	}
	nan := q
	nan.Centres[2].X = math.NaN()
	for name, tc := range map[string]struct {
		f *video.Frame
		q Quad
	}{
		"blank": {blank, q}, "off": {geomPlain(c, 0), off}, "nan": {geomPlain(c, 0), nan},
		"degenerate": {geomPlain(c, 0), Quad{Module: 4}},
		"nil":        {nil, q}, "short": {&video.Frame{W: 848, H: 480, Bits: 8, Y: make([]uint16, 10)}, q},
	} {
		if hs, ok := (SlotReader{}).Read(tc.f, tc.q); ok || len(hs) != 0 {
			t.Errorf("%s: got %v %v", name, hs, ok)
		}
	}
}
