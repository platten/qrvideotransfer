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
	// the other three finders put it.
	confirmFinderTol = 1.0
)

// confirms reports whether quad q is a code's. Its header must decode, and the decoded grid must
// imply at least confirmMinPitch pixels per cell. With that grid the anchors are located as Fit
// locates them, from the finders outwards; at least eight, and a quarter of those the quad places
// inside the capture, must be found. Then each finder in turn must lie within confirmFinderTol
// cells of where a model fitted to the located anchors and the other three finders puts it. One
// header copy can decode with one finder wrong, since a copy is placed mostly by the finders
// nearest it; the leave-one-out check catches that finder, and it does not depend on the anchors
// next to any one finder being readable. At most one layout is built per Detect.
func (d *FinderDetector) confirms(f *video.Frame, v *geomLuma, q Quad) bool {
	hs, ok := SlotReader{}.Read(f, q)
	if !ok {
		return false
	}
	gx, gy := int(hs[0].GridX), int(hs[0].GridY)
	c0 := 4.5 * float64(gy) / 135
	src := []gpt{{c0, c0}, {float64(gx) - c0, c0}, {c0, float64(gy) - c0}, {float64(gx) - c0, float64(gy) - c0}}
	dst := make([]gpt, 4)
	for k, c := range q.Centres {
		dst[k] = gpt(c)
	}
	across := (dst[1].sub(dst[0]).norm() + dst[3].sub(dst[2]).norm()) / 2 / (src[1].X - src[0].X)
	down := (dst[2].sub(dst[0]).norm() + dst[3].sub(dst[1]).norm()) / 2 / (src[2].Y - src[0].Y)
	if !(across >= confirmMinPitch && down >= confirmMinPitch) {
		return false
	}
	if d.layout == nil || d.layout.GridX() != gx || d.layout.GridY() != gy {
		if d.built {
			return false
		}
		l, err := frame.NewLayout(gx, gy)
		if err != nil {
			return false
		}
		d.layout, d.built = l, true
	}
	hm, ok := headerModel(v, q)
	if !ok {
		return false
	}
	model, _, ok := geomFitModel(src, dst, hm.lens)
	refs, ok2 := headerRefs(v, &hm)
	if !ok || !ok2 {
		return false
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
	rectifyLocate(v, &model, src, cells, obs, contrast*rectifyMinSaddle)
	rectifyReject(&model, cells, obs)
	var as, ad []gpt
	inside := 0
	for i, c := range cells {
		if v.inside(model.at(c)) {
			inside++
		}
		if obs[i].Found {
			as, ad = append(as, c), append(ad, gpt(obs[i].Corner))
		}
	}
	if len(as) < 8 || 4*len(as) < inside {
		return false
	}
	for k := range 4 {
		s, t := slices.Clone(as), slices.Clone(ad)
		for j := range 4 {
			if j != k {
				s, t = append(s, src[j]), append(t, dst[j])
			}
		}
		m, _, ok := geomFitModelK1(s, t, v.w, v.h)
		if !ok {
			return false
		}
		p := m.at(src[k])
		ex := m.at(src[k].add(gpt{0.5, 0})).sub(m.at(src[k].sub(gpt{0.5, 0})))
		ey := m.at(src[k].add(gpt{0, 0.5})).sub(m.at(src[k].sub(gpt{0, 0.5})))
		cell := math.Sqrt(math.Abs(ex.cross(ey)))
		if !(p.sub(dst[k]).norm() <= confirmFinderTol*cell) {
			return false
		}
	}
	return true
}
