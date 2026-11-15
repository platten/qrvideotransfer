package vision

import (
	"math"
	"slices"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/video"
)

// Header reading parameters (docs/DESIGN.md section 10, step 2).
const (
	headerSoftScale   = 64 // soft bit for a slot exactly at the finder's black (minus: white)
	headerMinContrast = 12 // smallest finder white minus black, 8-bit code values
	headerK1Steps     = 13 // lens candidates tried, evenly over [0, headerK1Top]
	headerK1Top       = 0.12
)

// SlotReader is the HeaderReader of docs/DESIGN.md section 10, step 2. The zero value is ready to
// use and safe for concurrent use.
type SlotReader struct{}

var _ HeaderReader = SlotReader{}

// Read reads the four header copies of the code whose finders are q. It decodes each copy alone,
// then the sums of pairs of copies that did not decode, then, if nothing decoded, the sum of all
// four, and returns one header per distinct frame index, in the order found.
func (SlotReader) Read(f *video.Frame, q Quad) ([]frame.Header, bool) {
	soft, ok := copySoft(f, q)
	if !ok {
		return nil, false
	}
	var out []frame.Header
	try := func(copies ...int) bool {
		var sum [frame.HeaderSlots]int8
		for s := range sum {
			t := 0
			for _, c := range copies {
				t += int(soft[c][s])
			}
			sum[s] = int8(max(-127, min(127, t)))
		}
		h, ok := frame.DecodeHeader(sum[:])
		if ok && !slices.ContainsFunc(out, func(o frame.Header) bool { return o.FrameIndex == h.FrameIndex }) {
			out = append(out, h)
		}
		return ok
	}
	var failed []int
	for c := range 4 {
		if !try(c) {
			failed = append(failed, c)
		}
	}
	for i, a := range failed {
		for _, b := range failed[i+1:] {
			try(a, b)
		}
	}
	if len(out) == 0 {
		try(0, 1, 2, 3)
	}
	return out, len(out) > 0
}

// copySoft returns the soft bits of the four header copies, indexed by copy and slot number. Each
// slot is the mean of a 3 x 3 grid over the central half of its footprint, compared with the black
// and white of the copy's own finder. The lens's k1 is chosen among headerK1Steps values by the
// slots' total contrast.
func copySoft(f *video.Frame, q Quad) ([4][frame.HeaderSlots]int8, bool) {
	var soft [4][frame.HeaderSlots]int8
	v, ok := geomView(f)
	if !ok {
		return soft, false
	}
	m, ok := headerModel(&v, q)
	if !ok {
		return soft, false
	}
	var raw [4][frame.HeaderSlots]float32
	refs, ok := headerRefs(&v, &m)
	if !ok {
		return soft, false
	}
	headerSample(&v, &m, &raw)
	for c := range 4 {
		black, white := refs[c][0], refs[c][1]
		if white-black < headerMinContrast {
			continue
		}
		for s, val := range raw[c] {
			t := (val - black) / (white - black)
			soft[c][s] = int8(math.Round(float64(max(-127, min(127, headerSoftScale*(1-2*t))))))
		}
	}
	return soft, true
}

// headerFractions are the finder centres in finder-rectangle fractions, by corner.
var headerFractions = [4]gpt{{0, 0}, {1, 0}, {0, 1}, {1, 1}}

// headerModel fits the fraction-to-capture model of q, choosing the lens's k1 by the contrast of
// the header slots.
func headerModel(v *geomLuma, q Quad) (geomModel, bool) {
	dst := make([]gpt, 4)
	for k, c := range q.Centres {
		dst[k] = gpt(c)
	}
	if !geomFinite(dst...) || !(q.Module > 0) {
		return geomModel{}, false
	}
	var best geomModel
	bestScore, found := -1.0, false
	var raw [4][frame.HeaderSlots]float32
	for i := range headerK1Steps {
		k1 := headerK1Top * float64(i) / float64(headerK1Steps-1)
		m, _, ok := geomFitModel(headerFractions[:], dst, geomNewLens(v.w, v.h, k1))
		if !ok {
			continue
		}
		refs, ok := headerRefs(v, &m)
		if !ok {
			continue
		}
		headerSample(v, &m, &raw)
		score := 0.0
		for c := range 4 {
			black, white := refs[c][0], refs[c][1]
			if white-black < headerMinContrast {
				continue
			}
			for _, val := range raw[c] {
				score += math.Min(1, math.Abs(float64(2*(val-black)/(white-black)-1)))
			}
		}
		if score > bestScore {
			best, bestScore, found = m, score, true
		}
	}
	return best, found
}

// headerAxes returns the half-axes of a footprint of fx by fy fractions at fraction p.
func headerAxes(m *geomModel, p gpt, fx, fy float64) (gpt, gpt) {
	ex := m.at(p.add(gpt{fx, 0})).sub(m.at(p.sub(gpt{fx, 0}))).mul(0.5)
	ey := m.at(p.add(gpt{0, fy})).sub(m.at(p.sub(gpt{0, fy}))).mul(0.5)
	return ex, ey
}

// headerSample samples every slot of every copy into raw.
func headerSample(v *geomLuma, m *geomModel, raw *[4][frame.HeaderSlots]float32) {
	for c := range 4 {
		for s := range frame.HeaderSlots {
			p := gpt(frame.HeaderSlot(c, s/4, s%4))
			ex, ey := headerAxes(m, p, 0.25/231, 0.25/126) // central half of a slot
			raw[c][s] = v.patch(m.at(p), ex, ey, 3)
		}
	}
}

// headerRefs measures each finder's black and white: its centre and the two modules above and
// below it at ring 1 are dark, the modules two above and two below (ring 2) light; the bottom-right
// finder is inverted. Only the vertical direction is used, where a module is exactly 1/126 of the
// finder rectangle whatever the grid's aspect. A finder outside the capture gets zero for both; it
// reports false when every finder is outside.
func headerRefs(v *geomLuma, m *geomModel) ([4][2]float32, bool) {
	var refs [4][2]float32
	const u = 1.0 / 126
	seen := false
	for k, fc := range headerFractions {
		c := m.at(fc)
		if !v.inside(c) {
			continue
		}
		seen = true
		_, ey := headerAxes(m, fc, 0, 0.3*u)
		ex := gpt{ey.Y, -ey.X} // a square footprint: ey turned by 90 degrees
		var dark, light float32
		for _, r := range []float64{-1, 0, 1} {
			dark += v.patch(m.at(fc.add(gpt{0, r * u})), ex, ey, 3) / 3
		}
		for _, r := range []float64{-2, 2} {
			light += v.patch(m.at(fc.add(gpt{0, r * u})), ex, ey, 3) / 2
		}
		if frame.Corner(k) == frame.BottomRight {
			dark, light = light, dark
		}
		refs[k] = [2]float32{dark, light}
	}
	return refs, seen
}
