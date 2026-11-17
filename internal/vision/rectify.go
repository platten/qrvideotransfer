package vision

import (
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/modem"
	"github.com/platten/qrvideotransfer/internal/video"
)

// Mesh fitting parameters (docs/DESIGN.md section 10, step 3).
const (
	rectifyStep      = 4    // cells between mesh nodes
	rectifySearch    = 1.5  // saddle search radius around the prediction, cells
	rectifyGrid      = 0.25 // coarse search step, cells
	rectifyWindow    = 1.3  // corner refinement radius, cells
	rectifyMaxPix    = 48.0 // largest corner refinement radius, pixels
	rectifyMinSaddle = 0.35 // smallest saddle contrast, as a share of the finders' contrast
	rectifyOutlier   = 0.35 // largest deviation of a corner from its neighbours' displacement, cells
	rectifyLatticeX  = 32   // anchor pitch, cells (FORMAT.md section 5.2)
	rectifyLatticeY  = 30
	// rectifyAnchorsOnly is the number of located anchors from which a finder must agree with the
	// anchors, within rectifyFinderKeep cells, to stay in the global model's fit.
	rectifyAnchorsOnly = 8
	rectifyFinderKeep  = 0.25
	// rectifyRefitFirst is the number of located anchors at which rectifyLocate first refits the
	// global model; it refits again each time the number doubles. It is geomLensK2Pairs, the
	// fewest from which the model's k2 is fitted.
	rectifyRefitFirst = geomLensK2Pairs
	// rectifyConsistent is how far, in cells, a saddle found from its neighbours on the lattice
	// may lie from the global model's prediction: twice the saddle search radius, the reach of a
	// local prediction plus the search around it.
	rectifyConsistent = 2 * rectifySearch
)

// MeshFitter is the Rectifier of docs/DESIGN.md section 10, step 3. The zero value is ready to use
// and safe for concurrent use.
type MeshFitter struct{}

var _ Rectifier = MeshFitter{}

// errFit is returned for inputs that cannot be fitted.
var errFit = errors.New("vision: cannot fit a mesh: invalid frame, layout or finders")

// Fit fits the cell-to-image mesh of the code with layout l whose finders are q.
//
// A global model (a homography and radial distortion about the capture's centre, its k1 first
// chosen by the header slots' contrast) is fitted to the finder centres. Anchors are then visited
// from the finders outwards: each saddle is predicted by the model plus the displacement of the
// anchors already located near it, found by the strongest saddle response within 1.5 cells, and
// refined to sub-pixel accuracy where the image gradient is perpendicular to the direction from the
// corner. The model, k1 included, is refitted to the corners and finders; a corner that departs
// from its neighbours' displacement by more than 0.35 cell is rejected. The mesh's nodes, every 4
// cells, are the model plus the located corners' residual displacement, interpolated over the
// anchor lattice.
//
// The observations are in l.Anchors() order. Found reports whether the saddle was located; Corner
// is then the located saddle, otherwise the mesh's position for it. Black, White and Phase are
// measured through the mesh for every anchor, found or not, over the central 60% of each 2 x 2
// quadrant.
func (MeshFitter) Fit(f *video.Frame, q Quad, l *frame.Layout) (*Mesh, []AnchorObs, error) {
	v, ok := geomView(f)
	if !ok || l == nil {
		return nil, nil, errFit
	}
	gx, gy := float64(l.GridX()), float64(l.GridY())
	c0 := 4.5 * gy / 135
	src := []gpt{{c0, c0}, {gx - c0, c0}, {c0, gy - c0}, {gx - c0, gy - c0}}
	dst := make([]gpt, 4)
	for k, c := range q.Centres {
		dst[k] = gpt(c)
	}
	hm, ok := headerModel(&v, q)
	if !ok {
		return nil, nil, errFit
	}
	model, _, ok := geomFitModel(src, dst, hm.lens)
	if !ok {
		return nil, nil, errFit
	}
	contrast := float32(0)
	if refs, ok := headerRefs(&v, &hm); ok {
		n := 0
		for _, r := range refs {
			if r[1] > r[0] {
				contrast += r[1] - r[0]
				n++
			}
		}
		contrast /= float32(max(n, 1))
	}
	anchors := l.Anchors()
	obs := make([]AnchorObs, len(anchors))
	cells := make([]gpt, len(anchors))
	for i, a := range anchors {
		obs[i].Anchor = a
		cells[i] = gpt{float64(a.X + 2), float64(a.Y + 2)}
	}
	rectifyLocate(&v, &model, src, cells, obs, contrast*rectifyMinSaddle, 0)

	// Refit with the corners, reject outliers, refit. Once rectifyAnchorsOnly anchors are located,
	// a finder stays in the refit only while it lies within rectifyFinderKeep cells of where the
	// anchors alone put it: a finder off by a fraction of a module would otherwise pull the mesh's
	// corner with the weight of an anchor, while an accurate one still steadies the corner.
	for range 2 {
		var s, d []gpt
		for i := range obs {
			if obs[i].Found {
				s, d = append(s, cells[i]), append(d, gpt(obs[i].Corner))
			}
		}
		if len(s) < rectifyAnchorsOnly {
			s, d = append(s, src...), append(d, dst...)
		} else if m0, _, ok := geomFitModelLens(s, d, v.w, v.h); ok {
			for k, c := range src {
				ex := m0.at(c.add(gpt{0.5, 0})).sub(m0.at(c.sub(gpt{0.5, 0})))
				ey := m0.at(c.add(gpt{0, 0.5})).sub(m0.at(c.sub(gpt{0, 0.5})))
				cell := math.Sqrt(math.Abs(ex.cross(ey)))
				if m0.at(c).sub(dst[k]).norm() <= rectifyFinderKeep*cell {
					s, d = append(s, c), append(d, dst[k])
				}
			}
		}
		if len(s) >= 8 {
			if m, _, ok := geomFitModelLens(s, d, v.w, v.h); ok {
				model = m
			}
		}
		rectifyReject(&model, cells, obs)
	}
	// At least a quarter of the anchors whose saddle the model puts inside the capture, and at least
	// one, must be located; otherwise the finders were not a code's or the capture is unusable.
	inside, found := 0, 0
	for i, c := range cells {
		if v.inside(model.at(c)) {
			inside++
			if obs[i].Found {
				found++
			}
		}
	}
	if found == 0 || 4*found < inside {
		return nil, nil, fmt.Errorf("vision: %d of %d anchors inside the capture located, under a quarter", found, inside)
	}
	// The finders that agree with the anchors as the confirmation requires (within
	// confirmFinderTolModule modules of where the anchors alone put them) are measured points of the
	// mesh too: the cells outside the first and the last anchor column and row are then
	// interpolated between finder and anchor rather than extrapolated.
	var measured [][2]gpt
	var s, d []gpt
	for i := range obs {
		if obs[i].Found {
			s, d = append(s, cells[i]), append(d, gpt(obs[i].Corner))
		}
	}
	if m0, _, ok := geomFitModelLens(s, d, v.w, v.h); ok && len(s) >= rectifyAnchorsOnly {
		uy := c0 / 4.5 // cells per finder module
		for k, c := range src {
			ex := m0.at(c.add(gpt{0.5, 0})).sub(m0.at(c.sub(gpt{0.5, 0})))
			ey := m0.at(c.add(gpt{0, 0.5})).sub(m0.at(c.sub(gpt{0, 0.5})))
			cell := math.Sqrt(math.Abs(ex.cross(ey)))
			if m0.at(c).sub(dst[k]).norm() <= confirmFinderTolModule*uy*cell {
				measured = append(measured, [2]gpt{c, dst[k]})
			}
		}
	}
	mesh := rectifyMesh(&model, l, cells, obs, measured)
	for i := range obs {
		if !obs[i].Found {
			obs[i].Corner = mesh.At(cells[i].X, cells[i].Y)
		}
		rectifyMeasure(&v, mesh, &obs[i])
	}
	return mesh, obs, nil
}

// rectifyLocate locates the anchors' saddles, nearest the finders first, and refits the global
// model to the located anchors as they accumulate (rectifyRefitFirst, then each time their number
// doubles), so that the model is fitted over most of the code by the time the anchors far from
// the finders are reached. With giveUp > 0 it stops once giveUp anchors have been tried and fewer
// than eight located: the quad is then not a code's, and the rest of a large hostile grid is not
// searched.
//
// Each saddle is predicted from its located neighbours on the anchor lattice where it has any
// (rectifyLocal): a local continuation of the lattice does not depend on the shape of the lens
// distortion, which the global model may not yet have caught near the code's edges. Without
// located neighbours it is predicted by the model plus the displacement of the anchors located
// near it. A saddle found from a local prediction must also lie within rectifyConsistent cells of
// the global model's prediction.
func rectifyLocate(v *geomLuma, model *geomModel, finders, cells []gpt, obs []AnchorObs, minContrast float32, giveUp int) {
	order := make([]int, len(cells))
	near := make([]float64, len(cells))
	for i, c := range cells {
		order[i] = i
		near[i] = math.Inf(1)
		for _, f := range finders {
			near[i] = math.Min(near[i], c.sub(f).norm())
		}
	}
	slices.SortStableFunc(order, func(a, b int) int {
		switch {
		case near[a] < near[b]:
			return -1
		case near[a] > near[b]:
			return 1
		}
		return 0
	})
	lat := newRectifyLattice(cells)
	var located []int
	refit := rectifyRefitFirst
	for tried, i := range order {
		if giveUp > 0 && tried >= giveUp && len(located) < 8 {
			return
		}
		c := cells[i]
		global := model.at(c)
		pred, local := lat.predict(i, obs)
		if !local {
			// Displacement of the located anchors within two lattice steps, weighted by inverse
			// distance.
			pred = global
			var sum gpt
			wsum := 0.0
			for _, j := range located {
				d := cells[j].sub(c).norm()
				if d > 2*rectifyLatticeX {
					continue
				}
				w := 1 / (d*d + 1)
				sum = sum.add(gpt(obs[j].Corner).sub(model.at(cells[j])).mul(w))
				wsum += w
			}
			if wsum > 0 {
				pred = pred.add(sum.mul(1 / wsum))
			}
		}
		ex := model.at(c.add(gpt{0.5, 0})).sub(model.at(c.sub(gpt{0.5, 0})))
		ey := model.at(c.add(gpt{0, 0.5})).sub(model.at(c.sub(gpt{0, 0.5})))
		p, ok := rectifySaddle(v, pred, ex, ey, minContrast)
		if ok && local && p.sub(global).norm() > rectifyConsistent*math.Sqrt(math.Abs(ex.cross(ey))) {
			ok = false
		}
		if !ok {
			continue
		}
		obs[i].Found, obs[i].Corner = true, Point(p)
		located = append(located, i)
		if len(located) == refit {
			refit *= 2
			var s, d []gpt
			for _, j := range located {
				s, d = append(s, cells[j]), append(d, gpt(obs[j].Corner))
			}
			if m, _, ok := geomFitModelLens(s, d, v.w, v.h); ok {
				*model = m
			}
		}
	}
}

// rectifyLattice indexes the anchors by their place on the anchor lattice (FORMAT.md section 5.2:
// every rectifyLatticeX columns from column 12, every rectifyLatticeY rows from row 13).
type rectifyLattice struct {
	at  map[[2]int]int // lattice place to anchor index
	pos [][2]int       // anchor index to lattice place
}

func newRectifyLattice(cells []gpt) *rectifyLattice {
	l := &rectifyLattice{at: make(map[[2]int]int, len(cells)), pos: make([][2]int, len(cells))}
	for i, c := range cells {
		k := [2]int{int(math.Round((c.X - 14) / rectifyLatticeX)), int(math.Round((c.Y - 15) / rectifyLatticeY))}
		l.pos[i], l.at[k] = k, i
	}
	return l
}

// predict returns the mean of the local continuations of the lattice at anchor i from its located
// neighbours: 2 P(k - d) - P(k - 2d) along each row and column direction d, and the parallelogram
// P(k + dx) + P(k + dy) - P(k + dx + dy) for each diagonal. It reports false when none applies.
func (l *rectifyLattice) predict(i int, obs []AnchorObs) (gpt, bool) {
	k := l.pos[i]
	found := func(dx, dy int) (gpt, bool) {
		j, ok := l.at[[2]int{k[0] + dx, k[1] + dy}]
		if !ok || !obs[j].Found {
			return gpt{}, false
		}
		return gpt(obs[j].Corner), true
	}
	var sum gpt
	n := 0
	for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
		a, ok1 := found(d[0], d[1])
		b, ok2 := found(2*d[0], 2*d[1])
		if ok1 && ok2 {
			sum, n = sum.add(a.mul(2).sub(b)), n+1
		}
	}
	for _, d := range [4][2]int{{1, 1}, {1, -1}, {-1, 1}, {-1, -1}} {
		a, ok1 := found(d[0], 0)
		b, ok2 := found(0, d[1])
		c, ok3 := found(d[0], d[1])
		if ok1 && ok2 && ok3 {
			sum, n = sum.add(a.add(b).sub(c)), n+1
		}
	}
	if n == 0 {
		return gpt{}, false
	}
	return sum.mul(1 / float64(n)), true
}

// rectifyResponse returns the saddle response at c, with ex and ey one cell along the grid: the
// mean of the light quadrants (top-right, bottom-left) minus that of the dark ones, each quadrant
// sampled at the centres of its four cells. It also returns the two differences within the dark
// and within the light quadrants.
func rectifyResponse(v *geomLuma, c, ex, ey gpt) (r, dd, dl float32) {
	var q [4]float32 // top-left, top-right, bottom-left, bottom-right
	for k, sg := range [4][2]float64{{-1, -1}, {1, -1}, {-1, 1}, {1, 1}} {
		for _, a := range [2]float64{0.5, 1.5} {
			for _, b := range [2]float64{0.5, 1.5} {
				p := c.add(ex.mul(sg[0] * a)).add(ey.mul(sg[1] * b))
				q[k] += v.bilinear(p.X, p.Y) / 4
			}
		}
	}
	return (q[1] + q[2] - q[0] - q[3]) / 2, float32(math.Abs(float64(q[0] - q[3]))), float32(math.Abs(float64(q[1] - q[2])))
}

// rectifySaddle finds the saddle near pred: the strongest response on a grid of quarter cells
// within rectifySearch cells, refined to sub-pixel accuracy, and checked.
func rectifySaddle(v *geomLuma, pred, ex, ey gpt, minContrast float32) (gpt, bool) {
	if !v.inside(pred) || !geomFinite(ex, ey) {
		return pred, false
	}
	best, bestR := pred, float32(math.Inf(-1))
	const n = int(rectifySearch / rectifyGrid)
	for j := -n; j <= n; j++ {
		for i := -n; i <= n; i++ {
			c := pred.add(ex.mul(float64(i) * rectifyGrid)).add(ey.mul(float64(j) * rectifyGrid))
			if r, _, _ := rectifyResponse(v, c, ex, ey); r > bestR {
				best, bestR = c, r
			}
		}
	}
	if !(bestR >= minContrast) || bestR <= 0 {
		return pred, false
	}
	cell := math.Sqrt(math.Abs(ex.cross(ey)))
	p, ok := rectifyCorner(v, best, math.Min(rectifyWindow*cell, rectifyMaxPix))
	if !ok || p.sub(best).norm() > 0.75*cell || !v.inside(p) {
		return pred, false
	}
	r, dd, dl := rectifyResponse(v, p, ex, ey)
	if !(r >= minContrast) || dd > r/2 || dl > r/2 {
		return pred, false
	}
	return p, true
}

// rectifyCorner refines a checker corner near c: the point that minimises the squared projections
// of (q - c) onto the image gradient at q, over the pixels q within rad (Gaussian weights), four
// times.
func rectifyCorner(v *geomLuma, c gpt, rad float64) (gpt, bool) {
	if !(rad >= 1) {
		return c, true
	}
	s2 := 2 * (rad / 2) * (rad / 2)
	for range 4 {
		var a11, a12, a22, b1, b2 float64
		x0, x1 := max(int(c.X-rad), 1), min(int(c.X+rad)+1, v.w-2)
		y0, y1 := max(int(c.Y-rad), 1), min(int(c.Y+rad)+1, v.h-2)
		for j := y0; j <= y1; j++ {
			for i := x0; i <= x1; i++ {
				q := gpt{float64(i) + 0.5, float64(j) + 0.5}
				d := q.sub(c)
				d2 := d.dot(d)
				if d2 > rad*rad {
					continue
				}
				gx := float64(v.at(i+1, j)-v.at(i-1, j)) / 2
				gy := float64(v.at(i, j+1)-v.at(i, j-1)) / 2
				w := math.Exp(-d2 / s2)
				xx, xy, yy := w*gx*gx, w*gx*gy, w*gy*gy
				a11, a12, a22 = a11+xx, a12+xy, a22+yy
				b1 += xx*q.X + xy*q.Y
				b2 += xy*q.X + yy*q.Y
			}
		}
		det := a11*a22 - a12*a12
		if !(det > 1e-9*(a11+a22)*(a11+a22)) || det == 0 {
			return c, false
		}
		n := gpt{(a22*b1 - a12*b2) / det, (a11*b2 - a12*b1) / det}
		moved := n.sub(c).norm()
		c = n
		if !(moved > 0.01) {
			break
		}
	}
	return c, geomFinite(c)
}

// rectifyReject marks as not found every corner whose residual from the model departs from the
// median residual of its located neighbours (within one lattice step in each direction) by more
// than rectifyOutlier cells; a corner without located neighbours is compared with zero, with
// twice the tolerance.
func rectifyReject(model *geomModel, cells []gpt, obs []AnchorObs) {
	res := make([]gpt, len(cells))
	size := make([]float64, len(cells))
	for i, c := range cells {
		if obs[i].Found {
			res[i] = gpt(obs[i].Corner).sub(model.at(c))
			ex := model.at(c.add(gpt{0.5, 0})).sub(model.at(c.sub(gpt{0.5, 0})))
			ey := model.at(c.add(gpt{0, 0.5})).sub(model.at(c.sub(gpt{0, 0.5})))
			size[i] = math.Sqrt(math.Abs(ex.cross(ey)))
		}
	}
	reject := make([]bool, len(cells))
	var xs, ys []float64
	for i, c := range cells {
		if !obs[i].Found {
			continue
		}
		xs, ys = xs[:0], ys[:0]
		for j, o := range cells {
			if j != i && obs[j].Found && math.Abs(o.X-c.X) <= 1.5*rectifyLatticeX && math.Abs(o.Y-c.Y) <= 1.5*rectifyLatticeY {
				xs, ys = append(xs, res[j].X), append(ys, res[j].Y)
			}
		}
		ref, tol := gpt{}, 2*rectifyOutlier
		if len(xs) > 0 {
			ref, tol = gpt{geomMedian(xs), geomMedian(ys)}, rectifyOutlier
		}
		if res[i].sub(ref).norm() > tol*size[i] || !(size[i] > 0) {
			reject[i] = true
		}
	}
	for i := range obs {
		if reject[i] {
			obs[i].Found = false
		}
	}
}

// geomMedian returns the median of xs, reordering them.
func geomMedian(xs []float64) float64 {
	slices.Sort(xs)
	n := len(xs)
	if n%2 == 1 {
		return xs[n/2]
	}
	return (xs[n/2-1] + xs[n/2]) / 2
}

// rectifyMesh builds the mesh: the model plus the located corners' residuals, filled in over the
// anchor lattice (one ring wider than the grid) by repeated averaging of neighbours, and
// interpolated bilinearly between lattice points.
func rectifyMesh(model *geomModel, l *frame.Layout, cells []gpt, obs []AnchorObs, finders [][2]gpt) *Mesh {
	gx, gy := l.GridX(), l.GridY()
	// Lattice point (a, b) is at cell (14 + 32 (a - 1), 15 + 30 (b - 1)).
	la, lb := (gx-14)/rectifyLatticeX+3, (gy-15)/rectifyLatticeY+3
	res := make([]gpt, la*lb)
	known := make([]bool, la*lb)
	for i, c := range cells {
		if !obs[i].Found {
			continue
		}
		a, b := (int(c.X)-14)/rectifyLatticeX+1, (int(c.Y)-15)/rectifyLatticeY+1
		res[b*la+a], known[b*la+a] = gpt(obs[i].Corner).sub(model.at(c)), true
	}
	// A measured finder sets the unknown nodes of the lattice square that holds it, together, so
	// that the square's bilinear interpolation gives the finder's residual at its centre.
	for _, f := range finders {
		u, v := (f[0].X-14)/rectifyLatticeX+1, (f[0].Y-15)/rectifyLatticeY+1
		a, b := int(math.Floor(u)), int(math.Floor(v))
		if a < 0 || b < 0 || a+1 >= la || b+1 >= lb {
			continue
		}
		fu, fv := u-float64(a), v-float64(b)
		ks := [4]int{b*la + a, b*la + a + 1, (b+1)*la + a, (b+1)*la + a + 1}
		ws := [4]float64{(1 - fu) * (1 - fv), fu * (1 - fv), (1 - fu) * fv, fu * fv}
		want := f[1].sub(model.at(f[0]))
		free := 0.0
		for j, k := range ks {
			if known[k] {
				want = want.sub(res[k].mul(ws[j]))
			} else {
				free += ws[j]
			}
		}
		if !(free > 0.05) {
			continue // the square's known corners already decide its interpolation there
		}
		for _, k := range ks {
			if !known[k] {
				res[k], known[k] = want.mul(1/free), true
			}
		}
	}
	next := make([]gpt, len(res))
	for range 4 * (la + lb) {
		for b := range lb {
			for a := range la {
				k := b*la + a
				if known[k] {
					next[k] = res[k]
					continue
				}
				var s gpt
				n := 0
				for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
					if x, y := a+d[0], b+d[1]; x >= 0 && y >= 0 && x < la && y < lb {
						s, n = s.add(res[y*la+x]), n+1
					}
				}
				next[k] = s.mul(1 / float64(n))
			}
		}
		res, next = next, res
	}
	lat := Mesh{Cols: la, Rows: lb, X0: 14 - rectifyLatticeX, Y0: 15 - rectifyLatticeY, StepX: rectifyLatticeX, StepY: rectifyLatticeY}
	lat.Nodes = make([]Point, len(res))
	for k, r := range res {
		lat.Nodes[k] = Point(r)
	}
	m := &Mesh{X0: -rectifyStep, Y0: -rectifyStep, StepX: rectifyStep, StepY: rectifyStep}
	m.Cols, m.Rows = (gx+rectifyStep-1)/rectifyStep+3, (gy+rectifyStep-1)/rectifyStep+3
	m.Nodes = make([]Point, m.Cols*m.Rows)
	for r := range m.Rows {
		for k := range m.Cols {
			c := gpt{m.X0 + float64(k)*m.StepX, m.Y0 + float64(r)*m.StepY}
			m.Nodes[r*m.Cols+k] = Point(model.at(c).add(gpt(lat.At(c.X, c.Y))))
		}
	}
	return m
}

// rectifyMeasure fills an anchor's Black, White and Phase through the mesh.
func rectifyMeasure(v *geomLuma, m *Mesh, o *AnchorObs) {
	quad := func(cx, cy float64) modem.Obs {
		x, y := float64(o.Anchor.X)+cx, float64(o.Anchor.Y)+cy
		const h = sampleShare // half of 60% of two cells
		c := gpt(m.At(x, y))
		hx := gpt(m.At(x+h, y)).sub(gpt(m.At(x-h, y))).mul(0.5)
		hy := gpt(m.At(x, y+h)).sub(gpt(m.At(x, y-h))).mul(0.5)
		o, _ := v.footprint(c, hx, hy)
		return o
	}
	avg := func(a, b modem.Obs) modem.Obs {
		return modem.Obs{Y: (a.Y + b.Y) / 2, Cb: (a.Cb + b.Cb) / 2, Cr: (a.Cr + b.Cr) / 2, Saturated: a.Saturated || b.Saturated}
	}
	o.Black = avg(quad(1, 1), quad(3, 3))
	o.White = avg(quad(3, 1), quad(1, 3))
	for k, c := range [4][2]float64{{5, 1}, {7, 1}, {7, 3}, {5, 3}} {
		o.Phase[k] = quad(c[0], c[1]).Y
	}
}
