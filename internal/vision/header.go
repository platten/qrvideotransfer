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
	// headerLensStep is the spacing of the lens candidates, for k1 and for k2, over
	// [-geomLensMax, geomLensMax]; when no candidate reads the header, k1 is refined to a quarter
	// of it around the best.
	headerLensStep = 0.02
)

// SlotReader is the HeaderReader of docs/DESIGN.md section 10, step 2. The zero value is ready to
// use and safe for concurrent use.
type SlotReader struct{}

var _ HeaderReader = SlotReader{}

// Read reads the four header copies of the code whose finders are q. It decodes each copy alone,
// then the sums of pairs of copies that did not decode, then, if nothing decoded, the sum of all
// four, and returns each distinct header once, in the order found: normally one per frame index,
// but two copies decoding the same index with different fields (a false CRC pass, or crafted
// content) are both returned, so that a conflicting header cannot hide the true one.
func (SlotReader) Read(f *video.Frame, q Quad) ([]frame.Header, bool) {
	soft, ok := copySoft(f, q)
	if !ok {
		return nil, false
	}
	out := decodeCopies(&soft, nil)
	return out, len(out) > 0
}

// ReadReason is Read, returning the strongest refusal instead of a flag. Stub.
func (SlotReader) ReadReason(f *video.Frame, q Quad) ([]frame.Header, frame.HeaderError) {
	return nil, frame.HeaderError{}
}

// decodeCopies decodes the copies' soft bits as Read describes.
func decodeCopies(soft *[4][frame.HeaderSlots]int8, rej *frame.HeaderError) []frame.Header {
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
		if ok && !slices.Contains(out, h) {
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
	return out
}

// copySoft returns the soft bits of the four header copies, indexed by copy and slot number. Each
// slot is the mean of a 3 x 3 grid over the central half of its footprint, compared with the black
// and white of the copy's own finder. The lens is chosen by the slots' total contrast (headerModel).
func copySoft(f *video.Frame, q Quad) ([4][frame.HeaderSlots]int8, bool) {
	var soft [4][frame.HeaderSlots]int8
	v, ok := geomView(f)
	if !ok {
		return soft, false
	}
	_, soft, ok = headerModelLens(&v, q, true)
	return soft, ok
}

// modelSoft returns the soft bits of the four copies sampled through model m against the finders'
// black and white refs (see copySoft).
func modelSoft(v *geomLuma, m *geomModel, refs *[4][2]float32, skip int) [4][frame.HeaderSlots]int8 {
	var soft [4][frame.HeaderSlots]int8
	var raw [4][frame.HeaderSlots]float32
	headerSample(v, m, &raw, skip)
	for c := range 4 {
		black, white := refs[c][0], refs[c][1]
		if c == skip || white-black < headerMinContrast {
			continue
		}
		for s, val := range raw[c] {
			t := (val - black) / (white - black)
			soft[c][s] = int8(math.Round(float64(max(-127, min(127, headerSoftScale*(1-2*t))))))
		}
	}
	return soft
}

// headerDecodesAt reports whether any copy other than skip decodes alone through the model of the
// finder centres dst with the given lens, against the finders' black and white refs: a cheap
// test, one lens and no pairs.
func headerDecodesAt(v *geomLuma, dst []gpt, lens geomLens, refs *[4][2]float32, skip int) bool {
	m, _, ok := geomFitModel(headerFractions[:], dst, lens)
	if !ok {
		return false
	}
	soft := modelSoft(v, &m, refs, skip)
	for c := range 4 {
		if c != skip {
			if _, ok := frame.DecodeHeader(soft[c][:]); ok {
				return true
			}
		}
	}
	return false
}

// headerFractions are the finder centres in finder-rectangle fractions, by corner.
var headerFractions = [4]gpt{{0, 0}, {1, 0}, {0, 1}, {1, 1}}

// headerModel fits the fraction-to-capture model of q, choosing the lens by the contrast of the
// header slots. Not a full grid over (k1, k2): k1 is chosen among the candidates with k2 = 0, then
// k2 with that k1, then k1 again within one step either side, at half the spacing, with that k2.
func headerModel(v *geomLuma, q Quad) (geomModel, bool) {
	m, _, ok := headerModelLens(v, q, true)
	return m, ok
}

// headerModelLens is headerModel, with the k2 stages only when withK2. Without them it costs about
// half as much: the three-finder search (threeSearch) uses it for its many trial quads, which on a
// hostile frame never decode and so try every candidate. It also returns the copies' soft bits
// through the chosen model (modelSoft).
func headerModelLens(v *geomLuma, q Quad, withK2 bool) (geomModel, [4][frame.HeaderSlots]int8, bool) {
	var bestSoft [4][frame.HeaderSlots]int8
	dst := make([]gpt, 4)
	for k, c := range q.Centres {
		dst[k] = gpt(c)
	}
	if !geomFinite(dst...) || !(q.Module > 0) {
		return geomModel{}, bestSoft, false
	}
	refs, ok := headerFinderRefs(v, dst)
	if !ok {
		return geomModel{}, bestSoft, false
	}
	var best geomModel
	bestScore, found := -1.0, false
	k1, k2 := 0.0, 0.0
	// try scores the lens (a, b); it reports whether the header decodes through it, and keeps the
	// best-scoring lens so far.
	try := func(a, b float64) (decoded, better bool) {
		m, score, soft, ok := headerScore(v, dst, geomNewLens(v.w, v.h, a, b), &refs, -1)
		if !ok {
			return false, false
		}
		if score > bestScore {
			best, bestSoft, bestScore, found, better = m, soft, score, true, true
		}
		if len(decodeCopies(&soft, nil)) > 0 {
			best, bestSoft, found = m, soft, true
			return true, better
		}
		return false, better
	}
	n := int(math.Round(geomLensMax / headerLensStep))
	// k1 outwards from 0 with k2 = 0, then k2 outwards from 0 with the best k1, then k1 within one
	// step of it, at half the spacing: the first lens through which the header decodes is taken,
	// so the smallest distortion that reads the header wins over a larger one that only raises the
	// contrast (a lens that bends the copies' slots into data cells can).
	for i := range 2*n + 1 {
		a := float64((i+1)/2) * headerLensStep
		if i%2 == 1 {
			a = -a
		}
		decoded, better := try(a, 0)
		if decoded {
			return best, bestSoft, true
		}
		if better {
			k1 = a
		}
	}
	if !withK2 {
		return best, bestSoft, found
	}
	for i := range 2 * n {
		b := float64(i/2+1) * headerLensStep
		if i%2 == 1 {
			b = -b
		}
		decoded, better := try(k1, b)
		if decoded {
			return best, bestSoft, true
		}
		if better {
			k2 = b
		}
	}
	for _, d := range []float64{-0.5, 0.5, -0.25, 0.25} {
		if a := k1 + d*headerLensStep; math.Abs(a) <= geomLensMax {
			if decoded, _ := try(a, k2); decoded {
				return best, bestSoft, true
			}
		}
	}
	return best, bestSoft, found
}

// headerFinderRefs returns the black and white of the finders at dst (headerRefs), measured through
// the model of dst without a lens. At the finders the homography alone is exact, and a lens that
// bends the model near the corners must not move the references: a lens candidate could otherwise
// raise its own score (headerScore) by sampling a finder's ring in its separator.
func headerFinderRefs(v *geomLuma, dst []gpt) ([4][2]float32, bool) {
	m0, _, ok := geomFitModel(headerFractions[:], dst, geomNewLens(v.w, v.h, 0, 0))
	if !ok {
		return [4][2]float32{}, false
	}
	return headerRefs(v, &m0)
}

// headerScore fits the fraction-to-capture model of the finder centres dst with the given lens and
// returns it with the slots' total contrast and the copies' soft bits (as modelSoft): the
// contrast is the sum, over the slots of every copy other than skip whose finder shows its black
// and white in refs, of |2t - 1| capped at 1, t being the slot's level between them.
func headerScore(v *geomLuma, dst []gpt, lens geomLens, refs *[4][2]float32, skip int) (m geomModel, score float64, soft [4][frame.HeaderSlots]int8, ok bool) {
	m, _, ok = geomFitModel(headerFractions[:], dst, lens)
	if !ok {
		return m, 0, soft, false
	}
	var raw [4][frame.HeaderSlots]float32
	headerSample(v, &m, &raw, skip)
	for c := range 4 {
		black, white := refs[c][0], refs[c][1]
		if c == skip || white-black < headerMinContrast {
			continue
		}
		for s, val := range raw[c] {
			t := (val - black) / (white - black)
			score += math.Min(1, math.Abs(float64(2*t-1)))
			soft[c][s] = int8(math.Round(float64(max(-127, min(127, headerSoftScale*(1-2*t))))))
		}
	}
	return m, score, soft, true
}

// headerAxes returns the half-axes of a footprint of fx by fy fractions at fraction p.
func headerAxes(m *geomModel, p gpt, fx, fy float64) (gpt, gpt) {
	ex := m.at(p.add(gpt{fx, 0})).sub(m.at(p.sub(gpt{fx, 0}))).mul(0.5)
	ey := m.at(p.add(gpt{0, fy})).sub(m.at(p.sub(gpt{0, fy}))).mul(0.5)
	return ex, ey
}

// headerSample samples every slot of every copy other than skip into raw.
func headerSample(v *geomLuma, m *geomModel, raw *[4][frame.HeaderSlots]float32, skip int) {
	for c := range 4 {
		if c == skip {
			continue
		}
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
