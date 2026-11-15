package vision

import (
	"math"
	"slices"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/video"
)

// Quad confirmation parameters.
const (
	// confirmMinPitch is the fewest camera pixels per cell a decoded grid may imply between the
	// finders. It rejects a header claiming a grid far finer than the capture can show before any
	// allocation is sized by the grid, so the work stays proportional to the image.
	confirmMinPitch = 1.5
	// confirmFinderTol is how far, in cells, a finder may lie from where the located anchors and
	// the other finders put it.
	confirmFinderTol = 1.0
	// confirmMinAnchors is the fewest anchors that must be located.
	confirmMinAnchors = 8
)

// confirmation is the state of a quad being confirmed: the finders in cell and capture
// coordinates, and the anchors located with the decoded grid.
type confirmation struct {
	src, dst []gpt // finder centres, cells and capture, by corner
	as, ad   []gpt // located anchors' saddles, cells and capture
	inside   int   // anchors the quad's model places inside the capture
	pitch    float64
}

// locate reads quad q's header and, with its grid, locates the anchors as Fit does, walking out
// from the finders listed in from. It reports false when the header does not decode, the grid
// implies fewer than confirmMinPitch pixels per cell, or a second layout would be built in this
// Detect.
func (d *FinderDetector) locate(f *video.Frame, v *geomLuma, q Quad, from []int) (*confirmation, bool) {
	hs, ok := SlotReader{}.Read(f, q)
	if !ok {
		return nil, false
	}
	gx, gy := int(hs[0].GridX), int(hs[0].GridY)
	c0 := 4.5 * float64(gy) / 135
	st := &confirmation{src: []gpt{{c0, c0}, {float64(gx) - c0, c0}, {c0, float64(gy) - c0}, {float64(gx) - c0, float64(gy) - c0}}}
	for _, c := range q.Centres {
		st.dst = append(st.dst, gpt(c))
	}
	src, dst := st.src, st.dst
	across := (dst[1].sub(dst[0]).norm() + dst[3].sub(dst[2]).norm()) / 2 / (src[1].X - src[0].X)
	down := (dst[2].sub(dst[0]).norm() + dst[3].sub(dst[1]).norm()) / 2 / (src[2].Y - src[0].Y)
	if !(across >= confirmMinPitch && down >= confirmMinPitch) {
		return nil, false
	}
	st.pitch = (across + down) / 2
	if d.layout == nil || d.layout.GridX() != gx || d.layout.GridY() != gy {
		if d.built {
			return nil, false
		}
		l, err := frame.NewLayout(gx, gy)
		if err != nil {
			return nil, false
		}
		d.layout, d.built = l, true
	}
	hm, ok := headerModel(v, q)
	if !ok {
		return nil, false
	}
	model, _, ok := geomFitModel(src, dst, hm.lens)
	refs, ok2 := headerRefs(v, &hm)
	if !ok || !ok2 {
		return nil, false
	}
	contrast := float32(0)
	for _, r := range refs {
		contrast = max(contrast, r[1]-r[0])
	}
	anchors := d.layout.Anchors()
	obs := make([]AnchorObs, len(anchors))
	cells := make([]gpt, len(anchors))
	for i, a := range anchors {
		obs[i].Anchor = a
		cells[i] = gpt{float64(a.X + 2), float64(a.Y + 2)}
	}
	var start []gpt
	for _, k := range from {
		start = append(start, src[k])
	}
	rectifyLocate(v, &model, start, cells, obs, contrast*rectifyMinSaddle)
	rectifyReject(&model, cells, obs)
	for i, c := range cells {
		if v.inside(model.at(c)) {
			st.inside++
		}
		if obs[i].Found {
			st.as, st.ad = append(st.as, c), append(st.ad, gpt(obs[i].Corner))
		}
	}
	return st, true
}

// enough reports whether enough anchors were located: confirmMinAnchors, and a quarter of those
// inside the capture.
func (st *confirmation) enough() bool {
	return len(st.as) >= confirmMinAnchors && 4*len(st.as) >= st.inside
}

// predict fits a model to the located anchors and the finders listed in with, and returns where
// it puts finder k, with the local cell size in pixels.
func (st *confirmation) predict(k int, with []int, w, h int) (gpt, float64, bool) {
	s, t := slices.Clone(st.as), slices.Clone(st.ad)
	for _, j := range with {
		s, t = append(s, st.src[j]), append(t, st.dst[j])
	}
	m, _, ok := geomFitModelK1(s, t, w, h)
	if !ok {
		return gpt{}, 0, false
	}
	c := st.src[k]
	ex := m.at(c.add(gpt{0.5, 0})).sub(m.at(c.sub(gpt{0.5, 0})))
	ey := m.at(c.add(gpt{0, 0.5})).sub(m.at(c.sub(gpt{0, 0.5})))
	p := m.at(c)
	return p, math.Sqrt(math.Abs(ex.cross(ey))), geomFinite(p)
}

// agrees reports whether each finder listed in check lies within confirmFinderTol cells of where
// the located anchors and the other finders listed in check put it.
func (st *confirmation) agrees(check []int, w, h int) bool {
	for _, k := range check {
		var others []int
		for _, j := range check {
			if j != k {
				others = append(others, j)
			}
		}
		p, cell, ok := st.predict(k, others, w, h)
		if !ok || !(p.sub(st.dst[k]).norm() <= confirmFinderTol*cell) {
			return false
		}
	}
	return true
}

// confirms reports whether quad q is a code's. Its header must decode, and the decoded grid must
// imply at least confirmMinPitch pixels per cell. With that grid the anchors are located as Fit
// locates them, from the finders outwards; at least confirmMinAnchors, and a quarter of those the
// quad places inside the capture, must be found. Then each finder in turn must lie within
// confirmFinderTol cells of where a model fitted to the located anchors and the other three
// finders puts it. One header copy can decode with one finder wrong, since a copy is placed mostly
// by the finders nearest it; the leave-one-out check catches that finder, and it does not depend
// on the anchors next to any one finder being readable. At most one layout is built per Detect.
func (d *FinderDetector) confirms(f *video.Frame, v *geomLuma, q Quad) bool {
	all := []int{0, 1, 2, 3}
	st, ok := d.locate(f, v, q, all)
	return ok && st.enough() && st.agrees(all, v.w, v.h)
}
