package channel

import (
	"math"
	"strconv"

	"github.com/platten/qrvideotransfer/internal/prng"
	"github.com/platten/qrvideotransfer/internal/video"
)

// toneSteps is the number of intervals of the tone curve's lookup table over [0, 1].
const toneSteps = 4096

// HLG constants of BT.2100.
const (
	hlgA = 0.17883277
	hlgB = 0.28466892
	hlgC = 0.55991073
)

// toneCurve returns the camera's transfer function: gamma 1/2.4, the inverse of the display's
// nominal BT.1886, or the BT.2100 hybrid log-gamma curve.
func toneCurve(hlg bool) *[toneSteps + 1]float32 {
	t := new([toneSteps + 1]float32)
	for i := range t {
		e := float64(float64(i) / toneSteps)
		switch {
		case !hlg:
			t[i] = float32(dpow(e, 1/panelGamma))
		case e <= 1.0/12:
			t[i] = float32(math.Sqrt(float64(3 * e)))
		default:
			t[i] = float32(float64(hlgA*dlog(float64(12*e)-hlgB)) + hlgC)
		}
	}
	return t
}

// fringe is a plane wave of interference fringes, precomputed per column and row.
type fringe struct {
	sx, cx, sy, cy []float32
}

func newFringe(w, h int, seed uint64) *fringe {
	g := prng.SplitMix64{State: prng.Seed("qrvt/channel/fringe", u64(seed))}
	period := fringeMin + float64((fringeMax-fringeMin)*uniform(&g))
	sa, ca := dsincos(float64(math.Pi * uniform(&g)))
	phase := float64(float64(2*math.Pi) * uniform(&g))
	kx, ky := float64(float64(2*math.Pi)*ca/period), float64(float64(2*math.Pi)*sa/period)
	f := &fringe{sx: make([]float32, w), cx: make([]float32, w), sy: make([]float32, h), cy: make([]float32, h)}
	for x := range w {
		s, c := dsincos(float64(kx * (float64(x) + 0.5)))
		f.sx[x], f.cx[x] = float32(s), float32(c)
	}
	for y := range h {
		s, c := dsincos(float64(ky*(float64(y)+0.5)) + phase)
		f.sy[y], f.cy[y] = float32(s), float32(c)
	}
	return f
}

// sensor is stage 5 for one capture: exposure gain and sensor saturation, white balance, colour
// matrix, tone curve, fringes and noise in code values, conversion to Y'CbCr, sharpening of
// brightness, 4:2:0 subsampling, colour noise reduction, and quantisation with clipping.
// Its brightness planes come from buf.
func (rc *recording) sensor(img *rgb, sh *shot, buf *scratch) *video.Frame {
	s := &rc.cfg.Sensor
	w, h := img.w, img.h
	gen := prng.SplitMix64{State: prng.Seed("qrvt/channel/noise", strconv.AppendInt(u64(rc.seed), int64(sh.slot), 10))}
	kr, kb := kr709, kb709
	if s.HLG {
		kr, kb = 0.2627, 0.0593
	}
	kr32, kg32, kb32 := float32(kr), float32(1-kr-kb), float32(kb)
	cbScale, crScale := float32(1/(2*(1-kb))), float32(1/(2*(1-kr)))
	gain := float32(s.Gain)
	wbR, wbB := float32(1+s.WhiteBalance), float32(1-s.WhiteBalance)
	sat, sat1 := float32(s.Saturation), float32(1+s.Saturation)
	noise := float64(s.Noise / 255)
	amp := float32(s.Fringe / 255)
	cw, ch := w/2, h/2
	yp, blurred := buf.planes(w * h)
	cb, cr := make([]float32, cw*ch), make([]float32, cw*ch)
	lut := rc.tone
	for y := range h {
		pr, pg, pb := img.p[0][y*w:(y+1)*w], img.p[1][y*w:(y+1)*w], img.p[2][y*w:(y+1)*w]
		py := yp[y*w : (y+1)*w]
		crow, rrow := cb[(y/2)*cw:(y/2+1)*cw], cr[(y/2)*cw:(y/2+1)*cw]
		for x := range py {
			r := float32(clip01(float32(pr[x]*gain)) * wbR)
			g := clip01(float32(pg[x] * gain))
			b := float32(clip01(float32(pb[x]*gain)) * wbB)
			if sat != 0 {
				m := float32((r + g + b) / 3)
				sm := float32(sat * m)
				r, g, b = float32(sat1*r)-sm, float32(sat1*g)-sm, float32(sat1*b)-sm
			}
			var fr float32
			if amp != 0 {
				f := rc.fringe
				fr = float32(amp * (float32(f.sx[x]*f.cy[y]) + float32(f.cx[x]*f.sy[y])))
			}
			r, g, b = tone(lut, clip01(r))+fr, tone(lut, clip01(g))+fr, tone(lut, clip01(b))+fr
			if noise != 0 {
				r += float32(noise * gauss(&gen))
				g += float32(noise * gauss(&gen))
				b += float32(noise * gauss(&gen))
			}
			yy := float32(float32(kr32*r)+float32(kg32*g)) + float32(kb32*b)
			py[x] = yy
			crow[x/2] += float32((b - yy) * cbScale)
			rrow[x/2] += float32((r - yy) * crScale)
		}
	}
	for j := range cb {
		cb[j] /= 4
		cr[j] /= 4
	}
	if s.Sharpen != 0 {
		copy(blurred, yp)
		binomial(blurred, w, h, []float32{0.25, 0.5, 0.25})
		a := float32(s.Sharpen)
		for i := range yp {
			yp[i] += float32(a * (yp[i] - blurred[i]))
		}
	}
	if s.ChromaNR != 0 {
		k := []float32{1.0 / 16, 4.0 / 16, 6.0 / 16, 4.0 / 16, 1.0 / 16}
		for _, p := range [][]float32{cb, cr} {
			b := append([]float32(nil), p...)
			binomial(b, cw, ch, k)
			nr := float32(s.ChromaNR)
			for i := range p {
				p[i] += float32(nr * (b[i] - p[i]))
			}
		}
	}
	f := &video.Frame{W: w, H: h, Bits: uint8(s.Bits), Y: make([]uint16, w*h), Cb: make([]uint16, cw*ch), Cr: make([]uint16, cw*ch)} //nolint:gosec // 8 or 10
	scale := float32(int(1) << (s.Bits - 8))
	top := float32(int(1)<<s.Bits - 1)
	for i, v := range yp {
		f.Y[i] = quantise(float32(float32(219*v)+16)*scale, top)
	}
	for i := range cb {
		f.Cb[i] = quantise(float32(float32(224*cb[i])+128)*scale, top)
		f.Cr[i] = quantise(float32(float32(224*cr[i])+128)*scale, top)
	}
	return f
}

// tone applies the tone curve to v in [0, 1] by linear interpolation in its table.
func tone(t *[toneSteps + 1]float32, v float32) float32 {
	u := float32(v * toneSteps)
	i := min(int(u), toneSteps-1)
	f := u - float32(i)
	return float32((t[i+1]-t[i])*f) + t[i]
}

func clip01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// quantise rounds v to the nearest integer and clips it to [0, top].
func quantise(v, top float32) uint16 {
	r := float32(math.Floor(float64(v) + 0.5))
	if r < 0 {
		return 0
	}
	if r > top {
		r = top
	}
	return uint16(r)
}

// binomial filters a w x h plane in place with a separable symmetric kernel, repeating edges.
func binomial(p []float32, w, h int, k []float32) {
	separable(p, w, w, h, k, k)
}
