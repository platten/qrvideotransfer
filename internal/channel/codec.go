package channel

import (
	"github.com/platten/qrvideotransfer/internal/video"
)

// Codec is stage 6, the phone's recorder, applied to each capture in place. It is called from
// several goroutines at once, one capture each, so it must keep no state between calls; a stateful
// encoder (the real x264 path) belongs in the consumer of [Simulate]'s output instead.
type Codec func(*video.Frame) error

// dctBasis[u][i] is the orthonormal 8-point DCT-II basis.
var dctBasis = [8][8]float64{}

// Quantiser returns the pure-Go stand-in recorder: every plane is cut into 8 x 8 blocks (edge
// blocks padded by repetition), each block's orthonormal DCT coefficients are rounded to multiples
// of step, which is in 8-bit code values and scaled for 10-bit frames, and the result is
// transformed back, rounded and clipped. A step below 1e-3 leaves frames unchanged.
func Quantiser(step float64) Codec {
	var z0 Codec
	return z0
}

// QuantiserStep returns the stand-in's step for a recorder of c's bitrate: stepPerBPP divided by the
// bits per camera pixel per frame. It returns 0 for a lossless recorder.
func QuantiserStep(c Config) float64 {
	var z0 float64
	return z0
}

func quantPlane(p []uint16, w, h int, q, top float64) {
}

// fdctPass transforms the rows of src and stores them transposed, so that two passes transform a
// block along both axes. It uses the basis's symmetry: even frequencies see the sums of mirrored
// samples, odd ones their differences.
func fdctPass(dst, src *[8][8]float64) {
}

// idctPass is the inverse of fdctPass. Rows of zeros, common after quantisation, are skipped.
func idctPass(dst, src *[8][8]float64) {
}
