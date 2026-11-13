package channel

import (
	"math"
	"testing"

	"github.com/platten/qrvideotransfer/internal/prng"
)

func near(a, b, tol float64) bool {
	return math.Abs(a-b) <= tol*math.Max(1, math.Abs(b))
}

func TestExpLog(t *testing.T) {
	for x := -30.0; x <= 30; x += 0.0137 {
		if got, want := dexp(x), math.Exp(x); !near(got, want, 1e-14) {
			t.Fatalf("dexp(%v) = %v, want %v", x, got, want)
		}
	}
	for x := 1e-9; x <= 1e6; x *= 1.013 {
		if got, want := dlog(x), math.Log(x); !near(got, want, 1e-14) {
			t.Fatalf("dlog(%v) = %v, want %v", x, got, want)
		}
	}
	for x := 0.0; x <= 1; x += 0.001 {
		if got, want := dpow(x, 2.4), math.Pow(x, 2.4); !near(got, want, 1e-13) {
			t.Fatalf("dpow(%v, 2.4) = %v, want %v", x, got, want)
		}
	}
	if dexp(-800) != 0 || !math.IsInf(dexp(800), 1) || !math.IsNaN(dexp(math.NaN())) {
		t.Error("dexp edge cases")
	}
	if !math.IsInf(dlog(0), -1) || !math.IsNaN(dlog(-1)) || !math.IsNaN(dlog(math.NaN())) || !math.IsInf(dlog(math.Inf(1)), 1) {
		t.Error("dlog edge cases")
	}
}

func TestSinCos(t *testing.T) {
	for x := -100.0; x <= 100; x += 0.0071 {
		s, c := dsincos(x)
		if !near(s, math.Sin(x), 1e-14) || !near(c, math.Cos(x), 1e-14) {
			t.Fatalf("dsincos(%v) = %v, %v; want %v, %v", x, s, c, math.Sin(x), math.Cos(x))
		}
	}
}

// Noise statistics: the approximate normal has mean 0, variance 1 and the kurtosis of a sum of four
// uniforms (3 - 1.2/4).
func TestGauss(t *testing.T) {
	g := prng.SplitMix64{State: 7}
	const n = 400000
	var m1, m2, m4 float64
	for range n {
		v := gauss(&g)
		if math.Abs(v) > 3.47 {
			t.Fatalf("gauss = %v, beyond its bound", v)
		}
		m1 += v
		m2 += v * v
		m4 += v * v * v * v
	}
	m1, m2, m4 = m1/n, m2/n, m4/n
	if math.Abs(m1) > 0.01 || math.Abs(m2-1) > 0.01 || math.Abs(m4/(m2*m2)-2.7) > 0.03 {
		t.Errorf("mean %v, variance %v, kurtosis %v", m1, m2, m4/(m2*m2))
	}
	u := uniform(&g)
	if u < 0 || u >= 1 {
		t.Errorf("uniform = %v", u)
	}
}

func TestClamp(t *testing.T) {
	if clamp(-1, 0, 1) != 0 || clamp(2, 0, 1) != 1 || clamp(0.5, 0, 1) != 0.5 {
		t.Error("clamp")
	}
	if clampi(-1, 0, 3) != 0 || clampi(5, 0, 3) != 3 || clampi(2, 0, 3) != 2 {
		t.Error("clampi")
	}
	if lerp(2, 4, 0.25) != 2.5 {
		t.Error("lerp")
	}
}
