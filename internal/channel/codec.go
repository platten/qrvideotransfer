package channel

import (
	"math"

	"github.com/platten/qrvideotransfer/internal/video"
)

// Codec is stage 6, the phone's recorder, applied to each capture in place. It is called from
// several goroutines at once, one capture each, so it must keep no state between calls; a stateful
// encoder (the real x264 path) belongs in the consumer of [Simulate]'s output instead.
type Codec func(*video.Frame) error

// dctBasis[u][i] is the orthonormal 8-point DCT-II basis.
var dctBasis = func() (b [8][8]float64) {
	for u := range 8 {
		c := 0.5
		if u == 0 {
			c = math.Sqrt(0.125)
		}
		for i := range 8 {
			_, cos := dsincos(float64(float64(math.Pi/16) * float64((2*i+1)*u)))
			b[u][i] = float64(c * cos)
		}
	}
	return b
}()

// Quantiser returns the pure-Go stand-in recorder: every plane is cut into 8 x 8 blocks (edge
// blocks padded by repetition), each block's orthonormal DCT coefficients are rounded to multiples
// of step, which is in 8-bit code values and scaled for 10-bit frames, and the result is
// transformed back, rounded and clipped. A step below 1e-3 leaves frames unchanged.
func Quantiser(step float64) Codec {
	return func(f *video.Frame) error {
		if step < 1e-3 {
			return nil
		}
		q := float64(step * float64(int(1)<<(f.Bits-8)))
		top := float64(int(1)<<f.Bits - 1)
		cw, ch := (f.W+1)/2, (f.H+1)/2
		quantPlane(f.Y, f.W, f.H, q, top)
		if f.Cb != nil {
			quantPlane(f.Cb, cw, ch, q, top)
			quantPlane(f.Cr, cw, ch, q, top)
		}
		return nil
	}
}

// QuantiserStep returns the stand-in's step for a recorder of c's bitrate: stepPerBPP divided by the
// bits per camera pixel per frame. It returns 0 for a lossless recorder.
func QuantiserStep(c Config) float64 {
	if c.Recorder.Bitrate == 0 {
		return 0
	}
	bpp := float64(float64(c.Recorder.Bitrate*1e6) / float64(c.Geometry.Width*c.Geometry.Height*c.Timing.FPS))
	return stepPerBPP / bpp
}

func quantPlane(p []uint16, w, h int, q, top float64) {
	var blk, tmp [8][8]float64
	inv := float64(1 / q)
	for by := 0; by < h; by += 8 {
		for bx := 0; bx < w; bx += 8 {
			for i := range 8 {
				row := p[min(by+i, h-1)*w:]
				for j := range 8 {
					blk[i][j] = float64(row[min(bx+j, w-1)])
				}
			}
			fdctPass(&tmp, &blk)
			fdctPass(&blk, &tmp)
			for i := range 8 {
				for j := range 8 {
					blk[i][j] = float64(math.Floor(float64(blk[i][j]*inv)+0.5) * q)
				}
			}
			idctPass(&tmp, &blk)
			idctPass(&blk, &tmp)
			for i := range min(8, h-by) {
				row := p[(by+i)*w+bx:]
				for j := range min(8, w-bx) {
					row[j] = uint16(clamp(math.Floor(blk[i][j]+0.5), 0, top))
				}
			}
		}
	}
}

// fdctPass transforms the rows of src and stores them transposed, so that two passes transform a
// block along both axes. It uses the basis's symmetry: even frequencies see the sums of mirrored
// samples, odd ones their differences.
func fdctPass(dst, src *[8][8]float64) {
	for i := range 8 {
		r := &src[i]
		var s, d [4]float64
		for k := range 4 {
			s[k], d[k] = r[k]+r[7-k], r[k]-r[7-k]
		}
		for u := range 8 {
			b, v := &dctBasis[u], &s
			if u&1 == 1 {
				v = &d
			}
			dst[u][i] = float64(b[0]*v[0]) + float64(b[1]*v[1]) + float64(b[2]*v[2]) + float64(b[3]*v[3])
		}
	}
}

// idctPass is the inverse of fdctPass. Rows of zeros, common after quantisation, are skipped.
func idctPass(dst, src *[8][8]float64) {
	for i := range 8 {
		r := &src[i]
		if *r == ([8]float64{}) {
			for k := range 8 {
				dst[k][i] = 0
			}
			continue
		}
		for k := range 4 {
			b := &dctBasis
			e := float64(b[0][k]*r[0]) + float64(b[2][k]*r[2]) + float64(b[4][k]*r[4]) + float64(b[6][k]*r[6])
			o := float64(b[1][k]*r[1]) + float64(b[3][k]*r[3]) + float64(b[5][k]*r[5]) + float64(b[7][k]*r[7])
			dst[k][i], dst[7-k][i] = e+o, e-o
		}
	}
}
