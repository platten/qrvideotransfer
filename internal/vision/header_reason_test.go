package vision

import (
	"slices"
	"testing"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/video"
)

// geomPaintHeader paints header h into the given copies of a rendered frame of c, as the renderer
// draws a header: light for bit 1, dark for bit 0.
func geomPaintHeader(c *geomCode, f *video.Frame, h frame.Header, copies ...int) {
	bits := h.Encode()
	for _, cp := range copies {
		for s := range frame.HeaderSlots {
			v := uint16(16)
			if bits[s] {
				v = 235
			}
			geomPaintSlot(c, f, cp, s, v)
		}
	}
}

// geomGreyHeader paints every slot of the given copies mid-grey: no copy decodes, none passes the
// CRC.
func geomGreyHeader(c *geomCode, f *video.Frame, copies ...int) {
	for _, cp := range copies {
		for s := range frame.HeaderSlots {
			geomPaintSlot(c, f, cp, s, 126)
		}
	}
}

// TestReadReason: ReadReason returns Read's headers and HeaderOK on a valid frame; on a frame whose
// header was re-encoded with a value Validate refuses it returns no header and that reason, with
// the refused header, which beats a CRC failure in the other copies; a header painted grey is a
// CRC failure, and no soft bits at all a length failure.
func TestReadReason(t *testing.T) {
	for _, g := range []struct{ gx, gy, pitch int }{{212, 120, 4}, {480, 270, 8}} {
		c := geomNewCode(t, g.gx, g.gy, g.pitch, 2, 3, 6)
		for k := range 3 {
			f, q := geomPlain(c, k), geomPlainQuad(c)
			want, _ := SlotReader{}.Read(f, q)
			hs, rej := SlotReader{}.ReadReason(f, q)
			if len(want) == 0 || !slices.Equal(hs, want) || rej.Reason != frame.HeaderOK {
				t.Errorf("%dx%d frame %d: ReadReason %v %v, Read %v", g.gx, g.gy, k, hs, rej.Reason, want)
			}
		}
	}
	c := geomNewCode(t, 212, 120, 4, 2, 1, 6)
	q := geomPlainQuad(c)
	edit := func(e func(*frame.Header)) frame.Header {
		h := c.headers[0]
		e(&h)
		return h
	}
	v2 := edit(func(h *frame.Header) { h.Version = 2 })
	mod5 := edit(func(h *frame.Header) { h.Modulation = 5 })
	inner7 := edit(func(h *frame.Header) { h.InnerCode = 7 })
	cases := []struct {
		name  string
		paint func(f *video.Frame)
		want  frame.HeaderReason
		value frame.Header
	}{
		{"version 2", func(f *video.Frame) { geomPaintHeader(c, f, v2, 0, 1, 2, 3) }, frame.HeaderVersion, v2},
		{"modulation 5", func(f *video.Frame) { geomPaintHeader(c, f, mod5, 0, 1, 2, 3) }, frame.HeaderModulation, mod5},
		{"inner code 7", func(f *video.Frame) { geomPaintHeader(c, f, inner7, 0, 1, 2, 3) }, frame.HeaderInner, inner7},
		{"version 2 beats CRC", func(f *video.Frame) {
			geomPaintHeader(c, f, v2, 3)
			geomGreyHeader(c, f, 0, 1, 2)
		}, frame.HeaderVersion, v2},
		{"grey", func(f *video.Frame) { geomGreyHeader(c, f, 0, 1, 2, 3) }, frame.HeaderCRC, frame.Header{}},
	}
	for _, tc := range cases {
		f := geomPlain(c, 0)
		tc.paint(f)
		hs, rej := SlotReader{}.ReadReason(f, q)
		if len(hs) != 0 || rej.Reason != tc.want || rej.Header != tc.value {
			t.Errorf("%s: got %v, reason %v with %+v; want reason %v with %+v", tc.name, hs, rej.Reason, rej.Header, tc.want, tc.value)
		}
		if got, ok := (SlotReader{}).Read(f, q); ok || len(got) != 0 {
			t.Errorf("%s: Read gives %v", tc.name, got)
		}
	}
	if hs, rej := (SlotReader{}).ReadReason(nil, q); len(hs) != 0 || rej.Reason != frame.HeaderLength {
		t.Errorf("no frame: got %v, reason %v; want HeaderLength", hs, rej.Reason)
	}
	var zero [4][frame.HeaderSlots]int8
	var rej frame.HeaderError
	if hs := decodeCopies(&zero, &rej); len(hs) != 0 || rej.Reason != frame.HeaderLength {
		t.Errorf("no contrast: got %v, reason %v; want HeaderLength", hs, rej.Reason)
	}
}

// TestDetectRefusal: the detector records what its last Detect saw: on a frame whose header says
// version 2, finders and the version refusal; on a valid frame, finders and no refusal; on a blank
// frame, nothing. The record is reset by each Detect.
func TestDetectRefusal(t *testing.T) {
	c := geomNewCode(t, 212, 120, 4, 2, 1, 6)
	h := c.headers[0]
	h.Version = 2
	v2 := geomPlain(c, 0)
	geomPaintHeader(c, v2, h, 0, 1, 2, 3)
	blank := geomPlain(c, 0)
	for i := range blank.Y {
		blank.Y[i] = 126
	}
	var d FinderDetector
	for _, tc := range []struct {
		name    string
		f       *video.Frame
		found   bool
		finders bool
		reason  frame.HeaderReason
	}{
		{"version 2", v2, false, true, frame.HeaderVersion},
		{"valid", geomPlain(c, 0), true, true, frame.HeaderOK},
		{"blank", blank, false, false, frame.HeaderOK},
		{"version 2 again", v2, false, true, frame.HeaderVersion},
	} {
		_, ok := d.Detect(tc.f, nil)
		finders, rej := d.Refusal()
		if ok != tc.found || finders != tc.finders || rej.Reason != tc.reason {
			t.Errorf("%s: located %v, finders %v, reason %v; want %v, %v, %v", tc.name, ok, finders, rej.Reason, tc.found, tc.finders, tc.reason)
		}
		if tc.reason == frame.HeaderVersion && rej.Header.Version != 2 {
			t.Errorf("%s: refused header %+v, want version 2", tc.name, rej.Header)
		}
	}
}

// TestReadReasonAllocs: ReadReason allocates no more than Read, on a valid frame and on one whose
// header every lens candidate refuses (the lens loop's longest path).
func TestReadReasonAllocs(t *testing.T) {
	c := geomNewCode(t, 212, 120, 4, 2, 1, 6)
	q := geomPlainQuad(c)
	h := c.headers[0]
	h.Version = 2
	v2 := geomPlain(c, 0)
	geomPaintHeader(c, v2, h, 0, 1, 2, 3)
	for name, f := range map[string]*video.Frame{"valid": geomPlain(c, 0), "version 2": v2} {
		read := testing.AllocsPerRun(3, func() { SlotReader{}.Read(f, q) })
		reason := testing.AllocsPerRun(3, func() { SlotReader{}.ReadReason(f, q) })
		if reason > read {
			t.Errorf("%s: ReadReason %.0f allocations, Read %.0f", name, reason, read)
		}
	}
}
