package channel

import (
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
	var z0 *[toneSteps + 1]float32
	return z0
}

// fringe is a plane wave of interference fringes, precomputed per column and row.
type fringe struct {
	sx, cx, sy, cy []float32
}

func newFringe(w, h int, seed uint64) *fringe {
	var z0 *fringe
	return z0
}

// sensor is stage 5 for one capture: exposure gain and sensor saturation, white balance, colour
// matrix, tone curve, fringes and noise in code values, conversion to Y'CbCr, sharpening of
// brightness, 4:2:0 subsampling, colour noise reduction, and quantisation with clipping.
func (rc *recording) sensor(img *rgb, sh *shot) *video.Frame {
	var z0 *video.Frame
	return z0
}

// tone applies the tone curve to v in [0, 1] by linear interpolation in its table.
func tone(t *[toneSteps + 1]float32, v float32) float32 {
	var z0 float32
	return z0
}

func clip01(v float32) float32 {
	var z0 float32
	return z0
}

// quantise rounds v to the nearest integer and clips it to [0, top].
func quantise(v, top float32) uint16 {
	var z0 uint16
	return z0
}

// binomial filters a w x h plane in place with a separable symmetric kernel, repeating edges.
func binomial(p []float32, w, h int, k []float32) {
}
