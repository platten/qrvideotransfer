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
// p (1 + k1 |p|^2). Positive k1 is barrel.
type geomLens struct {
	cx, cy, unit, k1 float64
}

func geomNewLens(w, h int, k1 float64) geomLens {
	return geomLens{cx: float64(w) / 2, cy: float64(h) / 2, unit: float64(w) / 2, k1: k1}
}

// undistort maps a capture point to the pinhole point.
func (l geomLens) undistort(p gpt) gpt {
	d := gpt{p.X - l.cx, p.Y - l.cy}
	r2 := d.dot(d) / (l.unit * l.unit)
	return d.mul(1 + l.k1*r2).add(gpt{l.cx, l.cy})
}

// distort maps a pinhole point to the capture point, inverting undistort by Newton's method.
func (l geomLens) distort(p gpt) gpt {
	d := gpt{p.X - l.cx, p.Y - l.cy}
	ru := d.norm() / l.unit
	if l.k1 == 0 || !(ru > 0) {
		return p
	}
	r := ru
	for range 8 {
		r2 := r * r
		den := 3*l.k1*r2 + 1
		if !(den > 0.1) {
			break
		}
		r -= (l.k1*r2*r + r - ru) / den
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

// Lens search range for k1 (geomLens units). The simulator's presets reach 0.08; phones that
// correct distortion in the camera leave it near 0.
const (
	geomK1Min = -0.05
	geomK1Max = 0.2
)

// geomFitModelK1 fits a model to src -> dst with k1 chosen in [geomK1Min, geomK1Max] by
// golden-section search on the RMS residual. It needs more than four pairs to tell k1 apart.
func geomFitModelK1(src, dst []gpt, w, h int) (geomModel, float64, bool) {
	f := func(k float64) float64 {
		_, r, ok := geomFitModel(src, dst, geomNewLens(w, h, k))
		if !ok {
			return math.Inf(1)
		}
		return r
	}
	k := geomGolden(f, geomK1Min, geomK1Max, 40)
	return geomFitModel(src, dst, geomNewLens(w, h, k))
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
