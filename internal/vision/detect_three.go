package vision

import (
	"math"
	"slices"

	"github.com/platten/qrvideotransfer/internal/video"
)

// Locating from three finders (docs/DESIGN.md section 10, step 1).
const (
	// threeMinQuadrant is the fewest anchors that must be located in the quarter of the grid
	// nearest a hidden finder, so that its position is measured from the lattice around it, not
	// extrapolated.
	threeMinQuadrant = 4
	// threeSearchRadius bounds the search for a hidden centre around its prediction, as a share of
	// the quad's diagonal; threeSearchSteps grid steps lie on each side. The prediction from module
	// sizes was within 50 pixels of a 3,600-pixel diagonal on handheld poses tilted up to 25
	// degrees.
	threeSearchRadius = 0.025
	threeSearchSteps  = 5
	// threeCandidates is how many three-finder quads are tried: each costs a search of up to 121
	// header decodes, and a hostile frame can offer many.
	threeCandidates = 3
)

// threeCandidate is a quad of three finders found and a fourth predicted.
type threeCandidate struct {
	q       [4]*detectCluster // the missing corner holds a synthetic cluster at the prediction
	missing int
	score   float64
}

// three tries to locate the code from three finders when no four confirm (a finder under glare or
// an overlay). For each choice of the hidden corner, any of the four, the fourth centre is
// predicted (threePredict); candidates are scored and checked as four-finder quads are
// (detectScore), the best threeCandidates first. A candidate is located only if confirmThree confirms
// it; a quad is never accepted on three finders alone.
func (d *FinderDetector) three(f *video.Frame, v *geomLuma, normal, inverted []*detectCluster) (Quad, bool) {
	var top []threeCandidate
	consider := func(c [4]*detectCluster, k int) {
		var at [4]gpt
		var mod [4]float64
		var fm, chord float64
		for j, cl := range c {
			if j != k {
				at[j], mod[j] = cl.at, cl.chord
				fm, chord = fm+cl.fm/3, chord+cl.chord/3
			}
		}
		c[k] = &detectCluster{at: threePredict(at, mod, k), fm: fm, chord: chord, inv: k == 3}
		sc, ok := detectScore(c)
		if !ok || (len(top) == threeCandidates && sc <= top[len(top)-1].score) {
			return
		}
		i, _ := slices.BinarySearchFunc(top, sc, func(a threeCandidate, t float64) int {
			if a.score >= t {
				return -1
			}
			return 1
		})
		top = slices.Insert(top, i, threeCandidate{c, k, sc})
		top = top[:min(len(top), threeCandidates)]
	}
	sized := func(cs ...*detectCluster) bool {
		for i, a := range cs {
			for _, b := range cs[i+1:] {
				if a == b || !detectSizes(a, b) {
					return false
				}
			}
		}
		return true
	}
	for _, a := range normal {
		for _, b := range normal {
			for _, c := range normal {
				if sized(a, b, c) {
					consider([4]*detectCluster{a, b, c, nil}, 3)
				}
			}
			for _, br := range inverted {
				if !sized(a, b, br) {
					continue
				}
				consider([4]*detectCluster{nil, a, b, br}, 0)
				consider([4]*detectCluster{a, nil, b, br}, 1)
				consider([4]*detectCluster{a, b, nil, br}, 2)
			}
		}
	}
	for _, t := range top {
		var q Quad
		for j, c := range t.q {
			q.Centres[j] = Point(c.at)
		}
		q.Module = t.q[t.missing].chord
		if q, ok := d.confirmThree(f, v, q, t.missing); ok {
			d.hidden = t.missing
			return q, true
		}
	}
	return Quad{}, false
}

// threePredict predicts the centre of hidden corner k from the other three centres and their
// module sizes. Under a homography a point is N(u, v) / w(u, v) with N and w affine in the plane's
// coordinates, and lengths scale as w^(-3/2) (the Jacobian's determinant as w^-3), so w at each
// visible finder follows from its module size up to a common factor, w and N at the fourth from
// the parallelogram rule on those affine functions, and the centre from their quotient. With equal
// modules it is the parallelogram's prediction; under perspective the parallelogram was up to 385
// pixels off at 4K.
func threePredict(at [4]gpt, mod [4]float64, k int) gpt {
	var w [4]float64
	var n [4]gpt
	for j := range 4 {
		if j != k {
			w[j] = 1 / math.Cbrt(mod[j]*mod[j])
			n[j] = at[j].mul(w[j])
		}
	}
	// For affine f, f(TL) + f(BR) = f(TR) + f(BL): corners 0 + 3 = 1 + 2.
	opp := map[int][3]int{0: {1, 2, 3}, 1: {0, 3, 2}, 2: {0, 3, 1}, 3: {1, 2, 0}}[k]
	wk := w[opp[0]] + w[opp[1]] - w[opp[2]]
	if !(wk > 0) {
		return at[opp[0]].add(at[opp[1]]).sub(at[opp[2]])
	}
	return n[opp[0]].add(n[opp[1]]).sub(n[opp[2]]).mul(1 / wk)
}

// threeSearch returns q with its hidden corner k moved to the nearest point, on a grid of
// threeSearchSteps steps each way within threeSearchRadius of the diagonal around the prediction,
// at which a header copy other than k's decodes alone, or false when none does.
func threeSearch(v *geomLuma, q Quad, k int) (Quad, bool) {
	dst := make([]gpt, 4)
	for j, c := range q.Centres {
		dst[j] = gpt(c)
	}
	centre := dst[k]
	k1 := 0.0
	if m, ok := headerModel(v, q); ok {
		k1 = m.lens.k1
	}
	step := threeSearchRadius * dst[3].sub(dst[0]).norm() / threeSearchSteps
	type offset struct{ i, j int }
	var grid []offset
	for j := -threeSearchSteps; j <= threeSearchSteps; j++ {
		for i := -threeSearchSteps; i <= threeSearchSteps; i++ {
			grid = append(grid, offset{i, j})
		}
	}
	slices.SortStableFunc(grid, func(a, b offset) int { return a.i*a.i + a.j*a.j - b.i*b.i - b.j*b.j })
	for _, o := range grid {
		dst[k] = centre.add(gpt{float64(o.i) * step, float64(o.j) * step})
		if headerDecodesAt(v, dst, k1, k) {
			q.Centres[k] = Point(dst[k])
			return q, true
		}
	}
	return q, false
}

// confirmThree confirms a quad whose finder k is hidden and only predicted. A header copy must
// decode with the prediction or a point near it (threeSearch). The anchors are then located from
// the three visible finders, and the hidden centre is moved to where a model fitted to the located
// anchors and the three finders puts it, twice. Finally the header must decode with the moved
// quad, enough anchors must be located (confirmation.enough), at least threeMinQuadrant of them in
// the quarter of the grid nearest the hidden finder, and each visible finder must agree with the
// anchors and the other two (confirmation.agrees).
func (d *FinderDetector) confirmThree(f *video.Frame, v *geomLuma, q Quad, k int) (Quad, bool) {
	q, ok := threeSearch(v, q, k)
	if !ok {
		return Quad{}, false
	}
	var found []int
	for j := range 4 {
		if j != k {
			found = append(found, j)
		}
	}
	for range 2 {
		st, ok := firstEnough(d.locate(f, v, q, found))
		if !ok {
			return Quad{}, false
		}
		p, _, ok := st.predict(k, found, v.w, v.h)
		if !ok {
			return Quad{}, false
		}
		q.Centres[k] = Point(p)
	}
	st, ok := firstEnough(d.locate(f, v, q, found))
	if !ok || !st.agrees(found, v.w, v.h) {
		return Quad{}, false
	}
	mid := st.src[0].add(st.src[3]).mul(0.5)
	near := 0
	for _, c := range st.as {
		if (c.X < mid.X) == (st.src[k].X < mid.X) && (c.Y < mid.Y) == (st.src[k].Y < mid.Y) {
			near++
		}
	}
	if near < threeMinQuadrant {
		return Quad{}, false
	}
	return q, true
}
