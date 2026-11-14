package ldpc

import (
	"fmt"
	"math"
	"math/rand/v2"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// m1Rule is the check-node update of M1 (PR #8): x0.75 as floor(3m/4), soft bits as given.
var m1Rule = rule{scale: 12}

// TestRule checks every input magnitude: floor(scale m / 16) - offset, at least 0 and at most rMax;
// M1's rule is floor(3m/4) as computed in M1; the codes ship the rule chosen in the follow-up.
func TestRule(t *testing.T) {
	for _, r := range []rule{m1Rule, {scale: 13, shift: 2}, {scale: 16, offset: 2}, {scale: 16}} {
		for m := range int32(1 << 15) {
			want := min(max(m*int32(r.scale)/16-int32(r.offset), 0), rMax)
			if got := r.apply(int16(m)); int32(got) != want {
				t.Fatalf("%+v.apply(%d) = %d, want %d", r, m, got, want)
			}
		}
	}
	for m := range int16(rMax) {
		if got := m1Rule.apply(m); got != m-(m+3)>>2 {
			t.Fatalf("M1 rule of %d = %d, want floor(3m/4) = %d", m, got, m-(m+3)>>2)
		}
	}
	for _, id := range ids {
		if c := mustNew(t, id); c.rule != (rule{scale: 13, shift: 2}) {
			t.Errorf("code %d decodes with %+v", id, c.rule)
		}
	}
	// apply's int16 arithmetic needs 0 <= scale <= 16 (and offset >= 0); shift at most 3 keeps
	// -128 << shift within the posterior clamp.
	for _, r := range append([]rule{decoderRule, m1Rule}, func() (rs []rule) {
		for _, c := range candidates {
			rs = append(rs, c.rule)
		}
		return rs
	}()...) {
		if r.scale < 0 || r.scale > 16 || r.offset < 0 || r.shift > 3 {
			t.Errorf("rule %+v outside the range apply and decodeBits support", r)
		}
	}
}

// TestPairedM1B decodes the same frames with the M1 decoder and the shipped decoder (B) near FER 1e-3
// and logs McNemar's z over the frames only one of them gets wrong (positive: B worse). Long tests
// only; 400,000 frames per point.
func TestPairedM1B(t *testing.T) {
	if !long() {
		t.Skip("set QRVT_LONG_TESTS=1")
	}
	points := []struct {
		id   uint8
		ebn0 float64
	}{{4, 1.7}, {4, 1.9}, {3, 2.2}, {3, 2.3}, {2, 2.6}, {2, 2.7}, {1, 3.6}, {1, 3.7}}
	const frames = 400000
	for _, p := range points {
		var mu sync.Mutex
		var onlyM1, onlyB, both int
		workers := runtime.GOMAXPROCS(0)
		var wg sync.WaitGroup
		for w := range workers {
			wg.Go(func() {
				m1, _ := New(p.id)
				m1.rule = m1Rule
				b, _ := New(p.id)
				n := m1.N()
				sigma := math.Sqrt(1 / (float64(m1.info) / float64(n) * math.Pow(10, p.ebn0/10)) / 2)
				q := make([]int8, n)
				x, y, xy := 0, 0, 0
				for f := w; f < frames; f += workers {
					r := rand.New(rand.NewPCG(777, uint64(f)))
					info := randomBits(r, m1.info)
					cw := m1.encodeBits(info)
					sent := append(append([]uint8(nil), cw[:m1.info]...), cw[m1.k:]...)
					for i, bit := range sent {
						fl := 2 * (float64(1-2*int(bit)) + sigma*r.NormFloat64()) / (sigma * sigma)
						q[i] = int8(max(-127, min(127, math.Round(llrScale*fl))))
					}
					bad := func(c *Code) bool {
						c.decodeBits(q)
						for i, v := range info {
							if c.bit(i) != v {
								return true
							}
						}
						return false
					}
					switch e1, e2 := bad(m1), bad(b); {
					case e1 && e2:
						xy++
					case e1:
						x++
					case e2:
						y++
					}
				}
				mu.Lock()
				onlyM1, onlyB, both = onlyM1+x, onlyB+y, both+xy
				mu.Unlock()
			})
		}
		wg.Wait()
		z := 0.0
		if onlyM1+onlyB > 0 {
			z = float64(onlyB-onlyM1) / math.Sqrt(float64(onlyB+onlyM1))
		}
		t.Logf("code %d, Eb/N0 %.1f dB, %d frames: M1 FER %.2e, B FER %.2e; only M1 wrong %d, only B wrong %d, McNemar z %+.1f",
			p.id, p.ebn0, frames, float64(onlyM1+both)/frames, float64(onlyB+both)/frames, onlyM1, onlyB, z)
	}
}

// candidate is an integer check-node rule measured by TestDecoderCandidates.
type candidate struct {
	name string
	rule rule
}

var candidates = []candidate{
	{"M1: normalised x0.75 (floor(3m/4))", m1Rule},
	{"A1: offset 1", rule{scale: 16, offset: 1}},
	{"A2: offset 2", rule{scale: 16, offset: 2}},
	{"A3: offset 3", rule{scale: 16, offset: 3}},
	{"A2, soft bits x4 inside (offset 8)", rule{scale: 16, offset: 8, shift: 2}},
	{"B: normalised x13/16, soft bits x4 inside", rule{scale: 13, shift: 2}},
}

// softScales multiply the soft bits handed to the decoder relative to their true LLRs, before the
// 8-bit quantisation and clipping.
var softScales = []float64{0.5, 1, 2}

// sweep holds per Eb/N0 point the frame errors of each decoder and the frames that converged to a
// wrong codeword (every check satisfied, information bits wrong; the CRC-32C catches them).
type sweep struct {
	ebn0         []float64
	errs, wrong  [][]int // [decoder][point]; the last decoder is sum-product
	frames, spFr int
}

// crossing returns the Eb/N0 at which errs/frames falls to target, interpolated log-linearly, or
// NaN when it does not within the sweep.
func crossing(ebn0 []float64, errs []int, frames int, target float64) float64 {
	prev := 1.0
	for i, n := range errs {
		f := max(float64(n), 0.5) / float64(frames)
		if f <= target {
			if i == 0 || prev <= target {
				return ebn0[i]
			}
			return ebn0[i-1] + (ebn0[i]-ebn0[i-1])*math.Log(prev/target)/math.Log(prev/f)
		}
		prev = f
	}
	return math.NaN()
}

// runSweep decodes the same frames with every candidate and with sum-product (on the first spFrames
// frames, unquantised) at each soft-bit scale.
func runSweep(id uint8, from, to float64, frames, spFrames int) []sweep {
	out := make([]sweep, len(softScales))
	for s := range out {
		out[s] = sweep{frames: frames, spFr: spFrames, errs: make([][]int, len(candidates)+1), wrong: make([][]int, len(candidates)+1)}
	}
	for e := from; e <= to+1e-9; e += 0.1 {
		var mu sync.Mutex
		errs := make([][]int, len(softScales))
		wrong := make([][]int, len(softScales))
		for s := range errs {
			errs[s], wrong[s] = make([]int, len(candidates)+1), make([]int, len(candidates)+1)
		}
		workers := runtime.GOMAXPROCS(0)
		var wg sync.WaitGroup
		for w := range workers {
			wg.Go(func() {
				codes := make([]*Code, len(candidates))
				for k, cand := range candidates {
					codes[k], _ = New(id)
					codes[k].rule = cand.rule
				}
				c := codes[0]
				sp := newSPDecoder(c)
				n := c.N()
				sigma := math.Sqrt(1 / (float64(c.info) / float64(n) * math.Pow(10, e/10)) / 2)
				fl, fs := make([]float64, n), make([]float64, n)
				q := make([]int8, n)
				le, lw := make([][]int, len(softScales)), make([][]int, len(softScales))
				for s := range le {
					le[s], lw[s] = make([]int, len(candidates)+1), make([]int, len(candidates)+1)
				}
				for f := w; f < frames; f += workers {
					r := rand.New(rand.NewPCG(11, uint64(f)))
					info := randomBits(r, c.info)
					cw := c.encodeBits(info)
					sent := append(append([]uint8(nil), cw[:c.info]...), cw[c.k:]...)
					for i, bit := range sent {
						fl[i] = 2 * (float64(1-2*int(bit)) + sigma*r.NormFloat64()) / (sigma * sigma)
					}
					for s, scale := range softScales {
						for i := range q {
							q[i] = int8(max(-127, min(127, math.Round(scale*llrScale*fl[i]))))
						}
						for k, d := range codes {
							_, ok := d.decodeBits(q)
							for i, v := range info {
								if d.bit(i) != v {
									le[s][k]++
									if ok {
										lw[s][k]++
									}
									break
								}
							}
						}
						if f < spFrames {
							for i := range fs {
								fs[i] = scale * fl[i]
							}
							ok := sp.decode(fs)
							k := len(candidates)
							for i, v := range info {
								if (sp.l[i] < 0) != (v == 1) {
									le[s][k]++
									if ok {
										lw[s][k]++
									}
									break
								}
							}
						}
					}
				}
				mu.Lock()
				for s := range errs {
					for k := range errs[s] {
						errs[s][k] += le[s][k]
						wrong[s][k] += lw[s][k]
					}
				}
				mu.Unlock()
			})
		}
		wg.Wait()
		for s := range out {
			out[s].ebn0 = append(out[s].ebn0, e)
			for k := range out[s].errs {
				out[s].errs[k] = append(out[s].errs[k], errs[s][k])
				out[s].wrong[k] = append(out[s].wrong[k], wrong[s][k])
			}
		}
	}
	return out
}

// BenchmarkDecodeCandidates is BenchmarkDecode for each candidate rule, for alternating runs.
func BenchmarkDecodeCandidates(b *testing.B) {
	for _, ebn0 := range []float64{2.5, 3.0} {
		for k, cand := range candidates {
			b.Run(fmt.Sprintf("ebn0=%.1f/%d", ebn0, k), func(b *testing.B) {
				c := mustNew(b, 2)
				c.rule = cand.rule
				r := rand.New(rand.NewPCG(1, 2))
				dst := make([]byte, (c.N()+7)/8)
				llrs := make([][]int8, 64)
				for i := range llrs {
					c.Encode(dst, randomBytes(r, c.ShardSize()))
					llrs[i] = make([]int8, c.N())
					channel(r, unpack(dst, c.N()), ebn0, float64(c.info)/float64(c.N()), llrs[i])
				}
				shard := make([]byte, c.ShardSize())
				iters, i := 0, 0
				for b.Loop() {
					n, _ := c.Decode(shard, llrs[i%len(llrs)])
					iters += n
					i++
				}
				b.ReportMetric(float64(iters)/float64(i), "iters/op")
				b.ReportMetric(float64(c.N())*float64(i)/b.Elapsed().Seconds()/1e6, "Mbit/s")
			})
		}
	}
}

// TestDecoderCandidates measures the check-node rules of the decoder follow-up to PR #8 on the same
// frames: Eb/N0 at FER 1e-2 and 1e-3 for each code and soft-bit scale, and the frames that converged
// to a wrong codeword. Long tests only.
func TestDecoderCandidates(t *testing.T) {
	if !long() {
		t.Skip("set QRVT_LONG_TESTS=1")
	}
	ranges := map[uint8][2]float64{4: {0.6, 3.4}, 3: {1.5, 3.1}, 2: {1.9, 3.5}, 1: {2.9, 4.5}}
	const frames, spFrames = 20000, 4000
	for _, id := range []uint8{4, 3, 2, 1} {
		sw := runSweep(id, ranges[id][0], ranges[id][1], frames, spFrames)
		for s, scale := range softScales {
			var b strings.Builder
			for k := range len(candidates) + 1 {
				name, fr := "sum-product, float, 50 it", spFrames
				if k < len(candidates) {
					name, fr = candidates[k].name, frames
				}
				wrong := 0
				for _, n := range sw[s].wrong[k] {
					wrong += n
				}
				fmt.Fprintf(&b, "\n  %-44s FER 1e-2 %.2f dB, FER 1e-3 %.2f dB, wrong codewords %d",
					name, crossing(sw[s].ebn0, sw[s].errs[k], fr, 1e-2), crossing(sw[s].ebn0, sw[s].errs[k], fr, 1e-3), wrong)
			}
			t.Logf("code %d, soft bits x%g:%s", id, scale, b.String())
		}
	}
}
