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

// Noise statistics: gauss is a true standard normal, so beyond the moments its tails have the
// normal's probabilities: P(|Z| > 3) = 2.700e-3, P(|Z| > 3.5) = 4.653e-4, P(|Z| > 4) = 6.334e-5.
func TestGauss(t *testing.T) {
	g := prng.SplitMix64{State: 7}
	const n = 4000000
	var m1, m2, m4 float64
	var over [3]int
	limits := [3]float64{3, 3.5, 4}
	for range n {
		v := gauss(&g)
		m1 += v
		m2 += v * v
		m4 += v * v * v * v
		for k, l := range limits {
			if math.Abs(v) > l {
				over[k]++
			}
		}
	}
	m1, m2, m4 = m1/n, m2/n, m4/n
	if math.Abs(m1) > 0.003 || math.Abs(m2-1) > 0.003 || math.Abs(m4/(m2*m2)-3) > 0.03 {
		t.Errorf("mean %v, variance %v, kurtosis %v", m1, m2, m4/(m2*m2))
	}
	for k, p := range [3]float64{2.6998e-3, 4.6525e-4, 6.3342e-5} {
		want := p * n
		if d := math.Abs(float64(over[k]) - want); d > 4*math.Sqrt(want) {
			t.Errorf("%d values beyond %v sigma, want %.0f", over[k], limits[k], want)
		}
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

func BenchmarkGauss(b *testing.B) {
	g := prng.SplitMix64{State: 1}
	s := 0.0
	for range b.N {
		s += gauss(&g)
	}
	_ = s
}

// fillGauss draws exactly what gauss would.
func TestFillGauss(t *testing.T) {
	a, b := prng.SplitMix64{State: 3}, prng.SplitMix64{State: 3}
	dst := make([]float32, 100000)
	fillGauss(dst, 2, &a)
	for i, v := range dst {
		if want := float32(2 * gauss(&b)); v != want {
			t.Fatalf("sample %d = %v, want %v", i, v, want)
		}
	}
}
