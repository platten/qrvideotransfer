package channel

import (
	"math"
)

// optics is stage 4 for one capture, in linear light: the specular glare patch and veiling glare,
// then defocus and motion blur, then vignetting.
func (rc *recording) optics(img *rgb, sh *shot) {
	o := &rc.cfg.Optics
	if o.Glare > 0 {
		rc.glare(img, sh)
	}
	if o.Veil > 0 {
		for c := range img.p {
			for i := range img.p[c] {
				img.p[c][i] += float32(o.Veil)
			}
		}
	}
	sx, sy := rc.blurSigma(sh)
	kx, ky := kernel(sx), kernel(sy)
	// Outside the screen the image is uniform and blurring changes nothing, so only the screen's
	// bounding box, widened by the kernels' reach, is filtered.
	x0, y0, x1, y1 := rc.screenBox(sh, max(len(kx), len(ky))/2+2)
	for c := range img.p {
		separable(img.p[c][y0*img.w+x0:], img.w, x1-x0, y1-y0, kx, ky)
	}
	if o.Vignette > 0 {
		vignette(img, o.Vignette)
	}
}

// blurSigma returns the blur along x and y: the defocus sigma widened by the motion during the
// exposure, a box whose variance is its length squared over 12.
func (rc *recording) blurSigma(sh *shot) (sx, sy float64) {
	a, b := sh.window(&rc.cfg, rc.cam.cy)
	_, v := rc.track.at(float64((a + b) / 2))
	e := b - a
	s2 := float64(rc.cfg.Optics.Blur * rc.cfg.Optics.Blur)
	mx, my := float64(v.dx*e), float64(v.dy*e)
	return math.Sqrt(float64(float64(mx*mx)/12) + s2), math.Sqrt(float64(float64(my*my)/12) + s2)
}

// kernel returns a normalised Gaussian of the given sigma with radius ceil(3 sigma), or nil for a
// sigma too small to matter.
func kernel(sigma float64) []float32 {
	if sigma < 0.05 {
		return nil
	}
	r := int(math.Ceil(3 * sigma))
	w := make([]float64, 2*r+1)
	sum := 0.0
	for i := range w {
		d := float64(i - r)
		w[i] = dexp(float64(-float64(d*d) / float64(2*float64(sigma*sigma))))
		sum += w[i]
	}
	k := make([]float32, len(w))
	for i := range w {
		k[i] = float32(w[i] / sum)
	}
	return k
}

// blur convolves a w x h plane with separable Gaussians, repeating the edge samples.
func blur(p []float32, w, h int, sx, sy float64) {
	separable(p, w, w, h, kernel(sx), kernel(sy))
}

// separable filters the w x h image p (rows stride apart) in place with kernel kx along rows and
// ky along columns, either of which may be nil, repeating the edge samples. Each output sample
// sums its products in kernel order.
func separable(p []float32, stride, w, h int, kx, ky []float32) {
	if kx != nil {
		r := len(kx) / 2
		pad := make([]float32, w+2*r)
		for y := range h {
			row := p[y*stride : y*stride+w]
			for i := range pad {
				pad[i] = row[clampi(i-r, 0, w-1)]
			}
			accumulate(row, kx, func(j int) []float32 { return pad[j : j+w] })
		}
	}
	if ky != nil {
		// Original rows y-r .. y+r live in a ring while row y is overwritten.
		r := len(ky) / 2
		n := 2*r + 1
		ring := make([]float32, n*w)
		next := 0
		for y := range h {
			for ; next <= min(y+r, h-1); next++ {
				copy(ring[next%n*w:][:w], p[next*stride:])
			}
			accumulate(p[y*stride:y*stride+w], ky, func(j int) []float32 { return ring[clampi(y+j-r, 0, h-1)%n*w:][:w] })
		}
	}
}

// accumulate sets dst to the sum over j, in order, of k[j] times the samples in(j), two terms
// per sweep.
func accumulate(dst []float32, k []float32, in func(j int) []float32) {
	clear(dst)
	j := 0
	for ; j+1 < len(k); j += 2 {
		k0, k1 := k[j], k[j+1]
		a, b := in(j)[:len(dst)], in(j + 1)[:len(dst)]
		for x := range dst {
			dst[x] = float32(dst[x]+float32(k0*a[x])) + float32(k1*b[x])
		}
	}
	if j < len(k) {
		k0, a := k[j], in(j)[:len(dst)]
		for x := range dst {
			dst[x] += float32(k0 * a[x])
		}
	}
}

// screenBox returns the capture rectangle [x0, x1) x [y0, y1) holding the screen and the glare
// patch, widened by margin pixels and clipped to the frame.
func (rc *recording) screenBox(sh *shot, margin int) (x0, y0, x1, y1 int) {
	const n = 32 // points per edge
	pw, ph := float64(rc.scr.pw), float64(rc.scr.ph)
	lx, ly, hx, hy := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	add := func(x, y float64) {
		lx, ly, hx, hy = math.Min(lx, x), math.Min(ly, y), math.Max(hx, x), math.Max(hy, y)
	}
	for k := range n + 1 {
		f := float64(float64(k) / n)
		for _, p := range [4][2]float64{{float64(f * pw), 0}, {float64(f * pw), ph}, {0, float64(f * ph)}, {pw, float64(f * ph)}} {
			add(rc.project(sh, p[0], p[1]))
		}
	}
	if rc.cfg.Optics.Glare > 0 {
		gx, gy := rc.project(sh, rc.glareAt[0], rc.glareAt[1])
		r := float64(float64(1.25*rc.glareR)*rc.scr.scale/rc.panelPerPixel(sh)) + 1
		add(gx-r, gy-r)
		add(gx+r, gy+r)
	}
	m := float64(margin)
	w, h := float64(rc.cam.w), float64(rc.cam.h)
	return int(clamp(lx-m, 0, w)), int(clamp(ly-m, 0, h)), int(math.Ceil(clamp(hx+m, 0, w))), int(math.Ceil(clamp(hy+m, 0, h)))
}

// vignette darkens the image by v times the squared distance from the centre, relative to the corner.
func vignette(img *rgb, v float64) {
	cx, cy := float64(float64(img.w)/2), float64(float64(img.h)/2)
	rc2 := float64(cx*cx) + float64(cy*cy)
	col := make([]float64, img.w)
	for x := range col {
		d := float64(x) + 0.5 - cx
		col[x] = float64(float64(v*float64(d*d)) / rc2)
	}
	for y := range img.h {
		d := float64(y) + 0.5 - cy
		rowGain := 1 - float64(float64(v*float64(d*d))/rc2)
		for x := range img.w {
			g := float32(rowGain - col[x])
			i := y*img.w + x
			img.p[0][i] *= g
			img.p[1][i] *= g
			img.p[2][i] *= g
		}
	}
}

// glare adds the specular patch: a disc covering p.Glare of the code area at a random place on
// the screen, at glarePeak times white, with a smooth edge out to 1.25 times its radius.
func (rc *recording) glare(img *rgb, sh *shot) {
	gx, gy := rc.project(sh, rc.glareAt[0], rc.glareAt[1])
	r0 := float64(float64(rc.glareR*rc.scr.scale) / rc.panelPerPixel(sh))
	r1 := float64(1.25 * r0)
	x0, x1 := clampi(int(gx-r1), 0, img.w-1), clampi(int(gx+r1)+1, 0, img.w-1)
	y0, y1 := clampi(int(gy-r1), 0, img.h-1), clampi(int(gy+r1)+1, 0, img.h-1)
	for y := y0; y <= y1; y++ {
		dy := float64(y) + 0.5 - gy
		for x := x0; x <= x1; x++ {
			dx := float64(x) + 0.5 - gx
			r := math.Sqrt(float64(dx*dx) + float64(dy*dy))
			s := clamp(float64((r1-r)/(r1-r0)), 0, 1)
			add := float32(glarePeak * float64(float64(s*s)*(3-float64(2*s))))
			i := y*img.w + x
			img.p[0][i] += add
			img.p[1][i] += add
			img.p[2][i] += add
		}
	}
}
