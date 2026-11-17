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
	// confirmFinderTolModule is how far, in finder modules, a finder may lie from where the
	// located anchors and the other finders put it (docs/DESIGN.md section 10, step 1). The check
	// is not there to catch a real finder slightly misplaced: the detector locates a finder's
	// centre to about a pixel. It is there to catch a different, finder-like pattern standing in
	// for it, and two finder-like patterns cannot both have their centres within half a module of
	// the same point. In cells it is looser on fine grids (1 cell at 1080p and a cell of 4 pixels,
	// 1.85 cells on an 800 x 500 grid) and tighter on coarse ones; the S2 recordings, whose corner
	// finders lie beyond the anchors where the lens model extrapolates, motivated the change from
	// the smaller of 1 cell and half a module.
	confirmFinderTolModule = 0.5
	// confirmMaxLayouts is the most layouts built per Detect: two, so that a header copy decoding
	// a conflicting grid cannot hide the true one, while the work stays a small multiple of one
	// layout bounded by the image (confirmMinPitch).
	confirmMaxLayouts = 2
	// confirmGiveUp is how many anchors the confirmation tries before giving up when fewer than
	// eight are found: a real code's anchors are found from the first ones tried, next to the
	// finders, while a header claiming a large grid has thousands to search.
	confirmGiveUp = 64
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
	// k1Only makes predict fit the lens's k1 alone. The three-finder path sets it: with one finder
	// only predicted, k2's extra freedom lets a decoy among the three visible finders bend the
	// model enough that a real finder beside it disagrees too, and is then left out of the
	// candidates that follow (FinderDetector.three).
	k1Only bool
}

// locate reads quad q's headers and, for each distinct grid they give (those decoded by more
// single copies first), locates the anchors as Fit does, walking out from the finders listed in
// from. A header with a conflicting grid (a false CRC pass, or crafted content) therefore cannot
// hide the true one. It returns one confirmation per grid that could be tried.
func (d *FinderDetector) locate(f *video.Frame, v *geomLuma, q Quad, from []int) []*confirmation {
	hs, ok := SlotReader{}.Read(f, q)
	if !ok {
		return nil
	}
	type grid struct{ x, y, support int }
	var grids []grid
	for _, h := range hs {
		if !slices.ContainsFunc(grids, func(g grid) bool { return g.x == int(h.GridX) && g.y == int(h.GridY) }) {
			grids = append(grids, grid{int(h.GridX), int(h.GridY), 0})
		}
	}
	if len(grids) > 1 {
		if soft, ok := copySoft(f, q); ok {
			for c := range soft {
				if h, ok := frame.DecodeHeader(soft[c][:]); ok {
					for i := range grids {
						if grids[i].x == int(h.GridX) && grids[i].y == int(h.GridY) {
							grids[i].support++
						}
					}
				}
			}
		}
		slices.SortStableFunc(grids, func(a, b grid) int { return b.support - a.support })
	}
	var out []*confirmation
	for _, g := range grids {
		if st, ok := d.locateGrid(v, q, from, g.x, g.y); ok {
			out = append(out, st)
		}
	}
	return out
}

// locateGrid locates the anchors of quad q for a gx x gy grid (see locate). It reports false when
// the grid implies fewer than confirmMinPitch pixels per cell (and records that the gate failed) or
// when layoutFor refuses the grid.
func (d *FinderDetector) locateGrid(v *geomLuma, q Quad, from []int, gx, gy int) (*confirmation, bool) {
	c0 := 4.5 * float64(gy) / 135
	st := &confirmation{src: []gpt{{c0, c0}, {float64(gx) - c0, c0}, {c0, float64(gy) - c0}, {float64(gx) - c0, float64(gy) - c0}}}
	for _, c := range q.Centres {
		st.dst = append(st.dst, gpt(c))
	}
	src, dst := st.src, st.dst
	across := (dst[1].sub(dst[0]).norm() + dst[3].sub(dst[2]).norm()) / 2 / (src[1].X - src[0].X)
	down := (dst[2].sub(dst[0]).norm() + dst[3].sub(dst[1]).norm()) / 2 / (src[2].Y - src[0].Y)
	if !(across >= confirmMinPitch && down >= confirmMinPitch) {
		d.gateFailed = true
		return nil, false
	}
	st.pitch = (across + down) / 2
	layout, ok := d.layoutFor(gx, gy)
	if !ok {
		return nil, false
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
	anchors := layout.Anchors()
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
	rectifyLocate(v, &model, start, cells, obs, contrast*rectifyMinSaddle, confirmGiveUp)
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
	var m geomModel
	var ok bool
	if st.k1Only {
		m, _, ok = geomFitModelK1(s, t, w, h)
	} else {
		m, _, ok = geomFitModelLens(s, t, w, h)
	}
	if !ok {
		return gpt{}, 0, false
	}
	c := st.src[k]
	ex := m.at(c.add(gpt{0.5, 0})).sub(m.at(c.sub(gpt{0.5, 0})))
	ey := m.at(c.add(gpt{0, 0.5})).sub(m.at(c.sub(gpt{0, 0.5})))
	p := m.at(c)
	return p, math.Sqrt(math.Abs(ex.cross(ey))), geomFinite(p)
}

// disagree returns the finders listed in check that do not agree: a finder agrees when it lies
// within confirmFinderTolModule modules of where the located anchors and the other finders listed
// in check put it.
func (st *confirmation) disagree(check []int, w, h int) []int {
	var out []int
	for _, k := range check {
		var others []int
		for _, j := range check {
			if j != k {
				others = append(others, j)
			}
		}
		p, cell, ok := st.predict(k, others, w, h)
		uy := st.src[0].Y / 4.5 // cells per finder module: c0 = 4.5 Uy
		if !ok || !(p.sub(st.dst[k]).norm() <= confirmFinderTolModule*uy*cell) {
			out = append(out, k)
		}
	}
	return out
}

// confirms reports whether quad q is a code's. Its header must decode, and the decoded grid must
// imply at least confirmMinPitch pixels per cell. With that grid the anchors are located as Fit
// locates them, from the finders outwards; at least confirmMinAnchors, and a quarter of those the
// quad places inside the capture, must be found. Then each finder in turn must lie within the
// smaller of one cell and half a module of where a model fitted to the located anchors and the
// other three finders puts it. One header copy can decode with one finder wrong, since a copy is
// placed mostly by the finders nearest it; the leave-one-out check catches that finder, and it does
// not depend on the anchors next to any one finder being readable. Each distinct decoded grid is
// tried (locate).
//
// When it reports false it leaves in d.gateFailed whether a decoded grid failed the gate, so that
// the three-finder path can leave out the finders of that quad.
func (d *FinderDetector) confirms(f *video.Frame, v *geomLuma, q Quad) bool {
	all := []int{0, 1, 2, 3}
	d.gateFailed = false
	for _, st := range d.locate(f, v, q, all) {
		if !st.enough() {
			continue
		}
		if len(st.disagree(all, v.w, v.h)) == 0 {
			return true
		}
	}
	return false
}

// firstEnough returns the first confirmation with enough anchors located.
func firstEnough(sts []*confirmation) (*confirmation, bool) {
	for _, st := range sts {
		if st.enough() {
			return st, true
		}
	}
	return nil, false
}

// layoutFor returns the layout of a gx x gy grid, cached by grid (the confirmMaxLayouts most recent
// kept across Detects). At most confirmMaxLayouts distinct grids are used per Detect; a further one
// is refused, so that the work stays bounded whatever the header copies claim.
func (d *FinderDetector) layoutFor(gx, gy int) (*frame.Layout, bool) {
	g := [2]int{gx, gy}
	if !slices.Contains(d.grids, g) {
		if len(d.grids) >= confirmMaxLayouts {
			return nil, false
		}
		d.grids = append(d.grids, g)
	}
	for _, l := range d.layouts {
		if l.GridX() == gx && l.GridY() == gy {
			return l, true
		}
	}
	l, err := frame.NewLayout(gx, gy)
	if err != nil {
		return nil, false
	}
	if len(d.layouts) >= confirmMaxLayouts {
		d.layouts = d.layouts[1:]
	}
	d.layouts = append(d.layouts, l)
	return l, true
}
