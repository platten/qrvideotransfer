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

// zigR is where the ziggurat's tail begins, and zigV the area of each of its 128 layers
// (Marsaglia and Tsang, "The Ziggurat Method for Generating Random Variables", 2000).
const (
	zigR = 3.442619855899
	zigV = 9.91256303526217e-3
	zigM = 1 << 31
)

// zig holds the ziggurat's tables, computed with this file's deterministic functions: k bounds
// the fast path of each layer, w scales a 31-bit integer to x, f is the density at each layer's edge.
var zig = func() (z struct {
	k    [128]uint64
	w, f [128]float64
}) {
	half := func(x float64) float64 { return dexp(-float64(0.5 * float64(x*x))) }
	dn, tn := zigR, zigR
	q := zigV / half(dn)
	z.k[0] = uint64(float64(float64(dn/q) * zigM))
	z.w[0], z.w[127] = q/zigM, dn/zigM
	z.f[0], z.f[127] = 1, half(dn)
	for i := 126; i >= 1; i-- {
		dn = math.Sqrt(-float64(2 * dlog(zigV/dn+half(dn))))
		z.k[i+1] = uint64(float64(float64(dn/tn) * zigM))
		tn = dn
		z.f[i], z.w[i] = half(dn), dn/zigM
	}
	return z
}()

// gauss returns a standard normal value by the ziggurat method. One output of g usually
// suffices; the rest go to gaussSlow.
func gauss(g *prng.SplitMix64) float64 {
	v := g.Next()
	if i, mag := v&127, v>>32&0x7fffffff; mag < zig.k[i] {
		x := float64(float64(mag) * zig.w[i])
		if v>>63 == 1 {
			return -x
		}
		return x
	}
	return gaussSlow(g, v)
}

// gaussSlow finishes gauss for a draw v outside its layer's rectangle: the tail beyond zigR is
// sampled exactly, a wedge by rejection against the density, and otherwise gauss starts again.
func gaussSlow(g *prng.SplitMix64, v uint64) float64 {
	for {
		i, mag := v&127, v>>32&0x7fffffff
		sign := 1.0
		if v>>63 == 1 {
			sign = -1
		}
		x := float64(float64(mag) * zig.w[i])
		switch {
		case mag < zig.k[i]:
			return sign * x
		case i == 0:
			for {
				x = -dlog(uniform1(g)) / zigR
				if y := -dlog(uniform1(g)); float64(y+y) >= float64(x*x) {
					return sign * (zigR + x)
				}
			}
		case float64(float64(uniform(g)*(zig.f[i-1]-zig.f[i])))+zig.f[i] < dexp(-float64(0.5*float64(x*x))):
			return sign * x
		}
		v = g.Next()
	}
}

// uniform1 returns a uniform value in (0, 1].
func uniform1(g *prng.SplitMix64) float64 {
	return float64(float64(g.Next()>>11+1) * 0x1p-53)
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
