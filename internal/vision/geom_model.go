package vision

import "math"

// geomHom is a homography, row-major, mapping (x, y, 1) to (u w, v w, w).
type geomHom [9]float64

// apply maps p through h.
func (h *geomHom) apply(p gpt) gpt {
	w := h[6]*p.X + h[7]*p.Y + h[8]
	return gpt{(h[0]*p.X + h[1]*p.Y + h[2]) / w, (h[3]*p.X + h[4]*p.Y + h[5]) / w}
}

// geomNormaliser returns the centroid of ps and the scale that brings their mean distance from it
// to sqrt(2), which conditions the homography's equations.
func geomNormaliser(ps []gpt) (c gpt, s float64) {
	for _, p := range ps {
		c = c.add(p)
	}
	c = c.mul(1 / float64(len(ps)))
	d := 0.0
	for _, p := range ps {
		d += p.sub(c).norm()
	}
	d /= float64(len(ps))
	return c, math.Sqrt2 / d
}

// geomFitHom returns the least-squares homography mapping src[i] to dst[i] (at least four pairs;
// exact for four), or false when the points are degenerate.
func geomFitHom(src, dst []gpt) (geomHom, bool) {
	if len(src) < 4 || len(src) != len(dst) {
		return geomHom{}, false
	}
	cs, ss := geomNormaliser(src)
	cd, sd := geomNormaliser(dst)
	if !(ss > 0) || !(sd > 0) || math.IsInf(ss, 0) || math.IsInf(sd, 0) {
		return geomHom{}, false
	}
	var a [8][9]float64 // normal equations, right-hand side in column 8
	for i := range src {
		p, q := src[i].sub(cs).mul(ss), dst[i].sub(cd).mul(sd)
		rows := [2][9]float64{
			{p.X, p.Y, 1, 0, 0, 0, -q.X * p.X, -q.X * p.Y, q.X},
			{0, 0, 0, p.X, p.Y, 1, -q.Y * p.X, -q.Y * p.Y, q.Y},
		}
		for _, r := range rows {
			for j := range 8 {
				for k := range 9 {
					a[j][k] += r[j] * r[k]
				}
			}
		}
	}
	x, ok := geomSolve8(&a)
	if !ok {
		return geomHom{}, false
	}
	// h = Td^-1 Hn Ts, with Ts = [ss 0 -ss cs.x; 0 ss -ss cs.y; 0 0 1] and Td^-1 = [1/sd 0 cd.x; 0 1/sd cd.y; 0 0 1].
	hn := [9]float64{x[0], x[1], x[2], x[3], x[4], x[5], x[6], x[7], 1}
	ts := [9]float64{ss, 0, -ss * cs.X, 0, ss, -ss * cs.Y, 0, 0, 1}
	td := [9]float64{1 / sd, 0, cd.X, 0, 1 / sd, cd.Y, 0, 0, 1}
	h := geomHom(geomMul3(geomMul3(td, hn), ts))
	for _, v := range h {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return geomHom{}, false
		}
	}
	return h, true
}

// geomMul3 multiplies two 3 x 3 matrices.
func geomMul3(a, b [9]float64) [9]float64 {
	var c [9]float64
	for i := range 3 {
		for j := range 3 {
			for k := range 3 {
				c[3*i+j] += a[3*i+k] * b[3*k+j]
			}
		}
	}
	return c
}

// geomSolve8 solves the augmented 8 x 8 system a by Gaussian elimination with partial pivoting.
func geomSolve8(a *[8][9]float64) ([8]float64, bool) {
	var x [8]float64
	scale := 0.0
	for i := range 8 {
		scale = math.Max(scale, math.Abs(a[i][i]))
	}
	for c := range 8 {
		p := c
		for r := c + 1; r < 8; r++ {
			if math.Abs(a[r][c]) > math.Abs(a[p][c]) {
				p = r
			}
		}
		if !(math.Abs(a[p][c]) > 1e-12*scale) {
			return x, false
		}
		a[c], a[p] = a[p], a[c]
		for r := c + 1; r < 8; r++ {
			f := a[r][c] / a[c][c]
			for k := c; k < 9; k++ {
				a[r][k] -= f * a[c][k]
			}
		}
	}
	for c := 7; c >= 0; c-- {
		s := a[c][8]
		for k := c + 1; k < 8; k++ {
			s -= a[c][k] * x[k]
		}
		x[c] = s / a[c][c]
	}
	return x, true
}

// geomLens is radial distortion about the capture's centre: a capture point p, relative to the
// centre in units of half the capture's width, is seen by an ideal pinhole camera at
// p (1 + k1 |p|^2 + k2 |p|^4). Positive k1 is barrel; k1 and k2 of opposite signs give the
// "mustache" distortion that phone lenses keep after the camera's own correction.
type geomLens struct {
	cx, cy, unit, k1, k2 float64
}

func geomNewLens(w, h int, k1, k2 float64) geomLens {
	return geomLens{cx: float64(w) / 2, cy: float64(h) / 2, unit: float64(w) / 2, k1: k1, k2: k2}
}

// undistort maps a capture point to the pinhole point.
func (l geomLens) undistort(p gpt) gpt {
	d := gpt{p.X - l.cx, p.Y - l.cy}
	r2 := d.dot(d) / (l.unit * l.unit)
	return d.mul(1 + l.k1*r2 + l.k2*r2*r2).add(gpt{l.cx, l.cy})
}

// distort maps a pinhole point to the capture point, inverting undistort by Newton's method.
func (l geomLens) distort(p gpt) gpt {
	d := gpt{p.X - l.cx, p.Y - l.cy}
	ru := d.norm() / l.unit
	if (l.k1 == 0 && l.k2 == 0) || !(ru > 0) {
		return p
	}
	r := ru
	for range 8 {
		r2 := r * r
		den := 3*l.k1*r2 + 5*l.k2*r2*r2 + 1
		if !(den > 0.1) {
			break
		}
		step := (l.k1*r2*r + l.k2*r2*r2*r + r - ru) / den
		r -= step
		if math.Abs(step) <= 1e-12*ru {
			break
		}
	}
	return d.mul(r / ru).add(gpt{l.cx, l.cy})
}

// geomModel maps plane coordinates (cells, or finder-rectangle fractions) to the capture: a
// homography onto the pinhole image, then the lens.
type geomModel struct {
	h    geomHom
	lens geomLens
}

// at maps plane point p to the capture.
func (m *geomModel) at(p gpt) gpt { return m.lens.distort(m.h.apply(p)) }

// geomFitModel fits the homography of a model with the given lens to src -> dst and returns it
// with the RMS residual in capture pixels.
func geomFitModel(src, dst []gpt, lens geomLens) (geomModel, float64, bool) {
	und := make([]gpt, len(dst))
	for i, p := range dst {
		und[i] = lens.undistort(p)
	}
	h, ok := geomFitHom(src, und)
	if !ok {
		return geomModel{}, 0, false
	}
	m := geomModel{h, lens}
	s := 0.0
	for i, p := range src {
		e := m.at(p).sub(dst[i])
		s += e.dot(e)
	}
	rms := math.Sqrt(s / float64(len(src)))
	return m, rms, !math.IsNaN(rms)
}

// geomLensMax bounds |k1| and |k2| (geomLens units, the radius in half capture widths). It is a
// statement about consumer camera lenses, not a fit to any recording: a phone or webcam corrects
// its lens in the camera and leaves at most a few per cent of radial distortion at the frame's
// edge (r = 1, where k1 + k2 is the relative displacement). A bound of 0.1 on each term admits up
// to 10% from either alone, with room for the two to oppose (mustache distortion). Where the two
// together would fold the image near the corner (both near -0.1), distort stops its Newton steps.
const geomLensMax = 0.1

// geomLensK2Pairs is the fewest pairs from which geomFitModelLens also fits k2: below it, k2 is
// left at 0, since a handful of points cannot tell the r^4 term from the homography.
const geomLensK2Pairs = 12

// geomFitModelLens fits a model to src -> dst and returns it with the RMS residual in capture
// pixels. It needs more than four pairs to tell the lens apart. With fewer than geomLensK2Pairs
// pairs only k1 is fitted, by golden-section search on the RMS residual in [-geomLensMax,
// geomLensMax].
//
// With k2, the homography and both lens terms are first estimated together by linear least
// squares (geomLensLinear). The result is kept only when the extra term earns its place by the
// Bayesian information criterion: with N = 2 len(src) coordinates, N ln(rss1 / rss2) > ln N, rss
// being the sums of squared residuals with k1 alone and with both. Without that, k2 bends the
// model to the points' noise, and the model then extrapolates worse beyond them (to a finder left
// out of the leave-one-out check, say) than with k1 alone. Searching k1 and k2 one at a time does
// not work: over the points the two terms nearly cancel along k1 = -m k2, and the residual lies in
// a narrow valley where such a search stalls.
func geomFitModelLens(src, dst []gpt, w, h int) (geomModel, float64, bool) {
	m, r, ok := geomFitModelK1(src, dst, w, h)
	if len(src) < geomLensK2Pairs {
		return m, r, ok
	}
	if a, b, lok := geomLensLinear(src, dst, w, h); lok {
		lens := geomNewLens(w, h, max(-geomLensMax, min(geomLensMax, a)), max(-geomLensMax, min(geomLensMax, b)))
		n := float64(2 * len(src))
		if m2, r2, ok2 := geomFitModel(src, dst, lens); ok2 && (!ok || (r2 > 0 && n*math.Log(r*r/(r2*r2)) > math.Log(n))) {
			return m2, r2, true
		}
	}
	return m, r, ok
}

// geomFitModelK1 fits a model to src -> dst with k2 = 0 and k1 chosen by golden-section search on
// the RMS residual in [-geomLensMax, geomLensMax].
func geomFitModelK1(src, dst []gpt, w, h int) (geomModel, float64, bool) {
	f := func(k float64) float64 { return geomFitRMS(src, dst, geomNewLens(w, h, k, 0)) }
	k1 := geomGolden(f, -geomLensMax, geomLensMax, geomLensEvals)
	return geomFitModel(src, dst, geomNewLens(w, h, k1, 0))
}

// geomLensIters is the number of linear solves of geomLensLinear.
const geomLensIters = 4

// geomLensLinear estimates k1 and k2 jointly with the homography. In capture coordinates relative
// to the lens centre, in its unit, a point D is seen by the pinhole camera at U = D (1 + k1 rho +
// k2 rho^2), rho = |D|^2, and U = H(S) for the homography H of the (normalised) plane point S. With
// H's denominator w = h7 Sx + h8 Sy + 1 taken from the previous solve (1 at first), the equations
// Ux w = h1 Sx + h2 Sy + h3 and Uy w = h4 Sx + h5 Sy + h6 are linear in h1..h8, k1 and k2; they
// are solved geomLensIters times, each with the last w.
func geomLensLinear(src, dst []gpt, w, h int) (k1, k2 float64, ok bool) {
	unit := geomNewLens(w, h, 0, 0)
	cs, ss := geomNormaliser(src)
	if !(ss > 0) || math.IsInf(ss, 0) {
		return 0, 0, false
	}
	var x [10]float64
	for range geomLensIters {
		var a [10][11]float64
		for i := range src {
			s := src[i].sub(cs).mul(ss)
			d := gpt{(dst[i].X - unit.cx) / unit.unit, (dst[i].Y - unit.cy) / unit.unit}
			rho := d.dot(d)
			wp := x[6]*s.X + x[7]*s.Y + 1
			rows := [2][11]float64{
				{s.X, s.Y, 1, 0, 0, 0, -d.X * s.X, -d.X * s.Y, -d.X * rho * wp, -d.X * rho * rho * wp, d.X},
				{0, 0, 0, s.X, s.Y, 1, -d.Y * s.X, -d.Y * s.Y, -d.Y * rho * wp, -d.Y * rho * rho * wp, d.Y},
			}
			for _, r := range rows {
				for j := range 10 {
					for k := range 11 {
						a[j][k] += r[j] * r[k]
					}
				}
			}
		}
		sol, sok := geomSolve10(&a)
		if !sok {
			return 0, 0, false
		}
		x = sol
	}
	if math.IsNaN(x[8]) || math.IsNaN(x[9]) || math.IsInf(x[8], 0) || math.IsInf(x[9], 0) {
		return 0, 0, false
	}
	return x[8], x[9], true
}

// geomSolve10 solves the augmented 10 x 10 system a by Gaussian elimination with partial pivoting.
func geomSolve10(a *[10][11]float64) ([10]float64, bool) {
	var x [10]float64
	scale := 0.0
	for i := range 10 {
		scale = math.Max(scale, math.Abs(a[i][i]))
	}
	for c := range 10 {
		p := c
		for r := c + 1; r < 10; r++ {
			if math.Abs(a[r][c]) > math.Abs(a[p][c]) {
				p = r
			}
		}
		if !(math.Abs(a[p][c]) > 1e-12*scale) {
			return x, false
		}
		a[c], a[p] = a[p], a[c]
		for r := c + 1; r < 10; r++ {
			f := a[r][c] / a[c][c]
			for k := c; k < 11; k++ {
				a[r][k] -= f * a[c][k]
			}
		}
	}
	for c := 9; c >= 0; c-- {
		s := a[c][10]
		for k := c + 1; k < 10; k++ {
			s -= a[c][k] * x[k]
		}
		x[c] = s / a[c][c]
	}
	return x, true
}

// geomLensEvals is the golden-section search's evaluations per lens term: the bracket shrinks to
// 0.618^24 of 0.2, about 2e-6, far below what the anchors resolve.
const geomLensEvals = 24

// geomFitRMS returns the RMS residual of the model with lens fitted to src -> dst, or +Inf.
func geomFitRMS(src, dst []gpt, lens geomLens) float64 {
	_, r, ok := geomFitModel(src, dst, lens)
	if !ok {
		return math.Inf(1)
	}
	return r
}

// geomGolden minimises f over [a, b] by golden-section search with n evaluations.
func geomGolden(f func(float64) float64, a, b float64, n int) float64 {
	const g = 0.6180339887498949
	c, d := b-g*(b-a), a+g*(b-a)
	fc, fd := f(c), f(d)
	for range n {
		if fc < fd {
			b, d, fd = d, c, fc
			c = b - g*(b-a)
			fc = f(c)
		} else {
			a, c, fc = c, d, fd
			d = a + g*(b-a)
			fd = f(d)
		}
	}
	return (a + b) / 2
}
