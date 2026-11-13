package channel

import (
	"math"

	"github.com/platten/qrvideotransfer/internal/prng"
)

// Determinism. Simulator output must be bit-identical on every platform. Go may fuse x*y + z into
// one FMA instruction on arm64 and other architectures but not on amd64, so in this package every
// product that feeds a sum is wrapped in an explicit conversion, which the language specification
// says prevents fusion. Transcendental functions come from this file rather than package math,
// whose results may differ between architectures; only exactly rounded functions of package math
// (Sqrt, Floor, Ldexp, Frexp, Abs) are used directly.

const (
	ln2Hi  = 6.93147180369123816490e-01 // ln 2, high part
	ln2Lo  = 1.90821492927058770002e-10 // ln 2, low part
	log2e  = 1.44269504088896338700e+00 // 1 / ln 2
	piO2Hi = 1.57079632673412561417e+00 // pi / 2, high part
	piO2Lo = 6.07710050650619224932e-11 // pi / 2, low part
	twoOPi = 6.36619772367581382433e-01 // 2 / pi
)

// dexp returns e^x to within a few units in the last place.
func dexp(x float64) float64 {
	switch {
	case x != x:
		return x
	case x < -745:
		return 0
	case x > 709:
		return math.Inf(1)
	}
	k := math.Floor(float64(x*log2e) + 0.5)
	r := float64(x-float64(k*ln2Hi)) - float64(k*ln2Lo) // |r| <= 0.35
	s := 1.0
	for i := 14; i >= 1; i-- { // Horner form of the Taylor series
		s = float64(float64(s*r)/float64(i)) + 1
	}
	return math.Ldexp(s, int(k))
}

// dlog returns the natural logarithm of x > 0; it returns -Inf for 0 and NaN below.
func dlog(x float64) float64 {
	switch {
	case x != x || x < 0:
		return math.NaN()
	case x == 0:
		return math.Inf(-1)
	case math.IsInf(x, 1):
		return x
	}
	m, e := math.Frexp(x)
	if m < math.Sqrt2/2 {
		m *= 2
		e--
	}
	s := float64((m - 1) / (m + 1)) // |s| <= 0.172
	z := float64(s * s)
	p := 1.0 / 23
	for k := 21; k >= 1; k -= 2 { // atanh series: log m = 2 (s + s^3/3 + ...)
		p = float64(z*p) + float64(1/float64(k))
	}
	fe := float64(e)
	return float64(fe*ln2Hi) + (float64(2*float64(s*p)) + float64(fe*ln2Lo))
}

// dpow returns x^y for x >= 0.
func dpow(x, y float64) float64 {
	if x == 0 {
		return 0
	}
	return dexp(float64(y * dlog(x)))
}

// dsincos returns sin x and cos x for moderate |x| (below about 1e6).
func dsincos(x float64) (sin, cos float64) {
	k := math.Floor(float64(x*twoOPi) + 0.5)
	r := float64(x-float64(k*piO2Hi)) - float64(k*piO2Lo) // |r| <= pi/4
	z := float64(r * r)
	s, c := 1.0, 1.0
	for i := 18; i >= 2; i -= 2 { // Horner forms of the two Taylor series
		s = 1 - float64(float64(z*s)/float64(i*(i+1)))
		c = 1 - float64(float64(z*c)/float64(i*(i-1)))
	}
	s = float64(s * r)
	switch int64(k) & 3 {
	case 0:
		return s, c
	case 1:
		return c, -s
	case 2:
		return -s, -c
	default:
		return -c, s
	}
}

// uniform returns a uniform value in [0, 1) from 53 bits of g.
func uniform(g *prng.SplitMix64) float64 {
	return float64(float64(g.Next()>>11) * 0x1p-53)
}

// gaussScale is the reciprocal standard deviation of a sum of four uniform 16-bit integers.
var gaussScale = 1 / math.Sqrt(4*(65536.0*65536.0-1)/12)

// gauss returns an approximately standard normal value: the centred, scaled sum of the four 16-bit
// parts of one output of g (Irwin-Hall with four terms, bounded at about 3.46).
func gauss(g *prng.SplitMix64) float64 {
	v := g.Next()
	s := v&0xffff + v>>16&0xffff + v>>32&0xffff + v>>48
	return float64(float64(float64(s)-131070) * gaussScale)
}

// clamp limits v to [lo, hi].
func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// clampi limits v to [lo, hi].
func clampi(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// lerp returns a + (b - a) f.
func lerp(a, b, f float64) float64 {
	return a + float64((b-a)*f)
}
