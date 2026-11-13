package ldpc

import (
	"math"
	"math/rand/v2"
	"runtime"
	"sync"
	"testing"
)

// spDecoder is a test-only reference: layered floating-point sum-product (exact tanh/atanh rule),
// up to spIterations iterations with early stopping, or the same schedule with a floating-point
// normalised or offset min-sum update. It exists to tell how much of a code's distance from capacity
// is the shipped decoder's (DESIGN.md section 8, TestMinSumVariants); it is never a shipping path.
type spDecoder struct {
	c      *Code
	checks [][]int // variable indices of each check, shortened variables included
	l      [N]float64
	r      [][]float64
}

const spIterations = 50

func newSPDecoder(c *Code) *spDecoder {
	d := &spDecoder{c: c}
	for _, row := range c.base.Shift {
		for q := range Z {
			var vs []int
			for j, s := range row {
				if s >= 0 {
					vs = append(vs, j*Z+(q+s)%Z)
				}
			}
			d.checks = append(d.checks, vs)
			d.r = append(d.r, make([]float64, len(vs)))
		}
	}
	return d
}

// decode decodes the float LLRs of the sent bits and reports whether every check passed; the hard
// decisions are left in d.l (negative means 1).
func (d *spDecoder) decode(llr []float64) bool { return d.run(llr, spIterations, -1, 0) }

// run decodes with sum-product when alpha < 0, otherwise with min-sum.
func (d *spDecoder) run(llr []float64, iters int, alpha, beta float64) bool {
	c := d.c
	for i := range N {
		switch {
		case i < c.info:
			d.l[i] = llr[i]
		case i < c.k:
			d.l[i] = math.Inf(1) // known zero
		default:
			d.l[i] = llr[c.info+i-c.k]
		}
	}
	for _, r := range d.r {
		clear(r)
	}
	q := make([]float64, 32)
	for range iters {
		if d.checksPass() {
			return true
		}
		for ci, vs := range d.checks {
			r := d.r[ci]
			if alpha >= 0 {
				m1, m2, at, sgn := math.Inf(1), math.Inf(1), -1, false
				for e, v := range vs {
					q[e] = d.l[v] - r[e]
					a := math.Abs(q[e])
					sgn = sgn != (q[e] < 0)
					switch {
					case a < m1:
						m1, m2, at = a, m1, e
					case a < m2:
						m2 = a
					}
				}
				for e, v := range vs {
					m := m1
					if e == at {
						m = m2
					}
					m = math.Max(alpha*m-beta, 0)
					if sgn != (q[e] < 0) {
						m = -m
					}
					r[e] = m
					d.l[v] = q[e] + r[e]
				}
				continue
			}
			prod := 1.0
			zeros, zeroAt := 0, -1
			for e, v := range vs {
				q[e] = d.l[v] - r[e]
				t := math.Tanh(q[e] / 2)
				if t == 0 {
					zeros++
					zeroAt = e
					continue
				}
				prod *= t
			}
			for e, v := range vs {
				var p float64
				switch {
				case zeros > 1 || zeros == 1 && e != zeroAt:
					p = 0
				case zeros == 1:
					p = prod
				default:
					p = prod / math.Tanh(q[e]/2)
				}
				p = math.Max(-1+1e-15, math.Min(1-1e-15, p))
				r[e] = 2 * math.Atanh(p)
				d.l[v] = q[e] + r[e]
			}
		}
	}
	return d.checksPass()
}

func (d *spDecoder) checksPass() bool {
	for _, vs := range d.checks {
		p := false
		for _, v := range vs {
			p = p != (d.l[v] < 0)
		}
		if p {
			return false
		}
	}
	return true
}

func TestSumProductDecodes(t *testing.T) {
	r := rand.New(rand.NewPCG(21, 22))
	for _, id := range ids {
		c := mustNew(t, id)
		d := newSPDecoder(c)
		info := randomBits(r, c.info)
		cw := c.encodeBits(info)
		sent := append(append([]uint8(nil), cw[:c.info]...), cw[c.k:]...)
		llr := make([]float64, len(sent))
		for i, b := range sent {
			llr[i] = 4 * float64(1-2*int(b))
			if i%41 == 0 {
				llr[i] = -llr[i] / 2 // some wrong soft bits
			}
		}
		if !d.decode(llr) {
			t.Fatalf("code %d: sum-product failed on a lightly damaged codeword", id)
		}
		for i, v := range info {
			if (d.l[i] < 0) != (v == 1) {
				t.Fatalf("code %d: bit %d wrong", id, i)
			}
		}
	}
}

// variant is one decoder of the min-sum comparison.
type variant struct {
	name        string
	iters       int
	alpha, beta float64 // alpha < 0: sum-product
	unquantised bool    // decode the float LLRs instead of the quantised soft bits / llrScale
}

var variants = []variant{
	{"sum-product 50 it, float LLRs", 50, -1, 0, true},
	{"sum-product 50 it, quantised", 50, -1, 0, false},
	{"min-sum x0.75, 25 it (float model of shipped)", 25, 0.75, 0, false},
	{"min-sum x0.75, 50 it", 50, 0.75, 0, false},
	{"min-sum x0.8125, 25 it", 25, 0.8125, 0, false},
	{"min-sum x0.875, 25 it", 25, 0.875, 0, false},
	{"offset min-sum 0.25, 25 it", 25, 1, 0.25, false},
	{"offset min-sum 0.5, 25 it", 25, 1, 0.5, false},
}

// variantErrors returns the frame errors of the shipped decoder and of each variant on the same frames.
func variantErrors(id uint8, ebn0 float64, frames int, seed uint64) (shipped int, errs []int) {
	errs = make([]int, len(variants))
	workers := runtime.GOMAXPROCS(0)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			c, _ := New(id)
			d := newSPDecoder(c)
			n := c.N()
			rate := float64(c.info) / float64(n)
			sigma := math.Sqrt(1 / (rate * math.Pow(10, ebn0/10)) / 2)
			fl, fq := make([]float64, n), make([]float64, n)
			q := make([]int8, n)
			s, e := 0, make([]int, len(variants))
			wrong := func(info []uint8, dec func(i int) bool) bool {
				for i, v := range info {
					if dec(i) != (v == 1) {
						return true
					}
				}
				return false
			}
			for f := w; f < frames; f += workers {
				r := rand.New(rand.NewPCG(seed, uint64(f)))
				info := randomBits(r, c.info)
				cw := c.encodeBits(info)
				sent := append(append([]uint8(nil), cw[:c.info]...), cw[c.k:]...)
				for i, bit := range sent {
					y := float64(1-2*int(bit)) + sigma*r.NormFloat64()
					fl[i] = 2 * y / (sigma * sigma)
					q[i] = int8(max(-127, min(127, math.Round(llrScale*fl[i]))))
					fq[i] = float64(q[i]) / llrScale
				}
				c.decodeBits(q)
				if wrong(info, func(i int) bool { return c.bit(i) == 1 }) {
					s++
				}
				for k, v := range variants {
					in := fq
					if v.unquantised {
						in = fl
					}
					d.run(in, v.iters, v.alpha, v.beta)
					if wrong(info, func(i int) bool { return d.l[i] < 0 }) {
						e[k]++
					}
				}
			}
			mu.Lock()
			shipped += s
			for k := range errs {
				errs[k] += e[k]
			}
			mu.Unlock()
		})
	}
	wg.Wait()
	return shipped, errs
}

// TestMinSumVariants sweeps Eb/N0 in 0.1 dB steps and logs where the shipped decoder and each
// variant cross FER 1e-2 on the same frames (log-linear interpolation between the bracketing
// points), and the capacity gap. Long tests only.
func TestMinSumVariants(t *testing.T) {
	if !long() {
		t.Skip("set QRVT_LONG_TESTS=1")
	}
	const frames = 4000
	ranges := map[uint8][2]float64{4: {0.3, 2.5}, 2: {1.9, 2.7}, 3: {1.5, 2.3}, 1: {3.0, 3.8}}
	for _, id := range []uint8{4, 2, 3, 1} {
		c := mustNew(t, id)
		limit := capacityLimit(float64(c.info) / float64(c.N()))
		names := append([]string{"shipped decoder (int16, x0.75 as floor(3m/4), 25 it)"}, make([]string, len(variants))...)
		for k, v := range variants {
			names[k+1] = v.name
		}
		prev := make([]float64, len(names))
		cross := make([]float64, len(names))
		for k := range prev {
			prev[k] = 1
		}
		for i := 0; ; i++ {
			e := ranges[id][0] + 0.1*float64(i)
			if e > ranges[id][1]+1e-9 {
				break
			}
			s, errs := variantErrors(id, e, frames, 9)
			fers := []float64{float64(s) / frames}
			for _, n := range errs {
				fers = append(fers, float64(n)/frames)
			}
			for k, f := range fers {
				if cross[k] == 0 && f <= 1e-2 {
					cross[k] = e
					if f > 0 && prev[k] > 1e-2 && i > 0 {
						cross[k] = e - 0.1 + 0.1*math.Log(prev[k]/1e-2)/math.Log(prev[k]/f)
					}
				}
				prev[k] = f
			}
		}
		for k, n := range names {
			if cross[k] == 0 {
				t.Logf("code %d: %-52s FER 1e-2 not reached by %.1f dB", id, n, ranges[id][1])
				continue
			}
			t.Logf("code %d: %-52s FER 1e-2 at %.2f dB, %.2f dB from capacity (%.2f dB)", id, n, cross[k], cross[k]-limit, limit)
		}
	}
}
