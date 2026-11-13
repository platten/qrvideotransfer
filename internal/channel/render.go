package channel

import "math"

// rgb is a linear-light image at camera resolution, white 1.
type rgb struct {
	w, h int
	p    [3][]float32
}

func newRGB(w, h int) *rgb {
	return &rgb{w: w, h: h, p: [3][]float32{make([]float32, w*h), make([]float32, w*h), make([]float32, w*h)}}
}

// render is stages 2 and 3 for one capture. Each capture pixel sees the panel point that the camera
// geometry maps it to at the time its row is exposed; its light is the exposure-weighted mix of the
// frames that point shows (scan-out and pixel response included), times the backlight's mean
// level. occs lists the showings that can be visible, with panels[k] the panel of occs[k].
func (rc *recording) render(sh *shot, occs []occ, panels []*panel) *rgb {
	c, tl := rc.cam, rc.tl
	img := newRGB(c.w, c.h)
	kx, ky := rc.sharpness(sh)
	pw, ph := float64(rc.scr.pw), float64(rc.scr.ph)
	sub := float64(rc.scr.sub)
	half := float64(seconds(rc.cfg.Timing.Exposure) / 2)
	lo, hi := 0, 0
	for y := range c.h {
		yc := float64(y) + 0.5
		a, b := sh.window(&rc.cfg, yc)
		q, _ := rc.track.at(a + half)
		hm := c.hom(q)
		inv := invert(&hm)
		fl := float32(tl.flicker(a, b))
		for lo < len(occs) && occs[lo].e+tl.scan+tl.resp < a {
			lo++
		}
		for hi < len(occs) && occs[hi].s <= b {
			hi++
		}
		row := y * c.w
		for x := range c.w {
			i := row + x
			xu, yu := c.undistort(float64(x)+0.5, yc)
			px, py := apply(&inv, xu, yu)
			if !(px >= 0 && px < pw && py >= 0 && py < ph) {
				img.p[0][i], img.p[1][i], img.p[2][i] = surround, surround, surround
				continue
			}
			yd := py / ph
			var r, g, bl float32
			for k := lo; k < hi; k++ {
				w := float32(tl.weight(occs[k], a, b, yd))
				if w <= 0 {
					continue
				}
				sr, sg, sb := panels[k].sample(float64(px*sub), py, kx, ky)
				r += float32(w * sr)
				g += float32(w * sg)
				bl += float32(w * sb)
			}
			img.p[0][i], img.p[1][i], img.p[2][i] = float32(r*fl), float32(g*fl), float32(bl*fl)
		}
	}
	return img
}

// sharpness returns how much the panel's sample-grid interpolation is sharpened along x and y:
// the camera pixels per sample, at least 1. Bilinear interpolation sharpened this way averages the
// panel's square pixels over one camera pixel, as the sensor's pixel aperture does, instead of
// blurring their edges over a whole panel pixel.
func (rc *recording) sharpness(sh *shot) (kx, ky float64) {
	s := rc.panelPerPixel(sh)
	return math.Max(1, 1/float64(s*float64(rc.scr.sub))), math.Max(1, 1/s)
}

// panelPerPixel returns the panel pixels per camera pixel at the frame's centre, mid-capture.
func (rc *recording) panelPerPixel(sh *shot) float64 {
	c := rc.cam
	a, b := sh.window(&rc.cfg, c.cy)
	q, _ := rc.track.at(float64((a + b) / 2))
	hm := c.hom(q)
	inv := invert(&hm)
	x0, y0 := apply(&inv, c.cx, c.cy)
	x1, y1 := apply(&inv, c.cx+1, c.cy)
	x2, y2 := apply(&inv, c.cx, c.cy+1)
	det := float64((x1-x0)*(y2-y0)) - float64((x2-x0)*(y1-y0))
	return math.Sqrt(math.Abs(det))
}

// sample interpolates the panel at sample-grid position (x, y), sharpened by kx and ky.
func (p *panel) sample(x, y, kx, ky float64) (r, g, b float32) {
	x0, x1, fx := sharp(x, kx, p.w)
	y0, y1, fy := sharp(y, ky, p.h)
	i00, i01, i10, i11 := y0*p.w+x0, y0*p.w+x1, y1*p.w+x0, y1*p.w+x1
	f := func(v []float32) float32 {
		top := float32((v[i01]-v[i00])*fx) + v[i00]
		bot := float32((v[i11]-v[i10])*fx) + v[i10]
		return float32((bot-top)*fy) + top
	}
	return f(p.r), f(p.g), f(p.b)
}

// sharp returns the two samples around position x of an axis with n samples (centres at
// half-integers) and the sharpened weight of the second.
func sharp(x, k float64, n int) (i0, i1 int, f float32) {
	t := x - 0.5
	t0 := math.Floor(t)
	i := int(t0)
	return clampi(i, 0, n-1), clampi(i+1, 0, n-1), float32(clamp(float64((t-t0-0.5)*k)+0.5, 0, 1))
}
