package ldpc

import (
	"math"
	"math/rand/v2"
	"os"
	"runtime"
	"sync"
	"testing"
)

// The error-rate test (IMPLEMENTATION_PROMPT.md, M1; DESIGN.md section 8).
//
// A transcription error in the base matrices still gives a valid, self-consistent code: every test of
// H c = 0, encoding and decoding would pass, but the code would not be the 802.11n code, would not
// interoperate and would likely be weaker. This test is the guard. It decodes the unshortened mother
// code of each rate, which is the code the published curves describe, over BPSK and AWGN, and
// requires frame error rate 1e-2 within 0.5 dB of the published Eb/N0 for the same code.
//
// References (Eb/N0 at FER = 1e-2, log-linear interpolation between the two bracketing points of the
// published curve; read from the vector data of the PDF for [A], from marker centroids of the image
// for [B]):
//
//	[A] A. Balatsoukas-Stimming, P. Giard, A. Burg, "Comparison of Polar Decoders with Existing
//	    Low-Density Parity-Check and Turbo Decoders", arXiv:1702.04707v2, 2017, Fig. 4,
//	    "LDPC (N = 1944): IEEE 802.11n (I = 12)": layered offset min-sum (beta 0.5), at most 12
//	    iterations, BPSK, AWGN, Eb/N0. Rate 1/2: 1.81 dB (points 1.75 dB 2.75e-2, 2.00 dB 5.37e-4).
//	    Rate 5/6: 3.70 dB (3.50 dB 6.70e-2, 3.75 dB 6.10e-3).
//	[B] S. Tavildar, github.com/tavildar/LDPC, results/wifi_ldpc_qpsk.png, curves "N = 1944":
//	    flooding sum-product, at most 20 iterations (LdpcC/main.cpp), one bit per real dimension,
//	    Eb/N0. Rate 2/3: 2.32 dB (2.25 dB 2.10e-2, 2.50 dB 1.62e-3). Rate 3/4: 2.84 dB (2.75 dB
//	    2.32e-2, 3.00 dB 2.25e-3). [B] is the only source found for rates 2/3 and 3/4, and it is
//	    not peer-reviewed.
//
// A cross-check, not used as the threshold: C. Roth, P. Meinerzhagen, C. Studer, A. Burg, "A 15.8
// pJ/bit/iter Quasi-Cyclic LDPC Decoder for IEEE 802.11n in 90 nm CMOS", IEEE ASSCC 2010, Fig. 5,
// Z = 81, layered offset min-sum with 5-bit messages, 10 iterations: 2.16 dB (rate 1/2) and 3.71 dB
// (rate 5/6).
var references = []struct {
	rate Rate
	ebn0 float64 // published Eb/N0 in dB at FER 1e-2
}{
	{Rate1_2, 1.81}, {Rate2_3, 2.32}, {Rate3_4, 2.84}, {Rate5_6, 3.70},
}

// margin is the allowed distance from the published curve, in dB.
const margin = 0.5

// llrScale is the soft-bit scale of the simulated channel: one unit of LLR is 4 soft-bit steps.
const llrScale = 4

func long() bool { return os.Getenv("QRVT_LONG_TESTS") != "" }

// channel returns quantised BPSK/AWGN soft bits for sent bits at Eb/N0 ebn0 dB for a code of rate
// rate (information bits per sent bit), positive meaning 0.
func channel(r *rand.Rand, sent []uint8, ebn0, rate float64, llr []int8) {
	n0 := 1 / (rate * math.Pow(10, ebn0/10))
	sigma := math.Sqrt(n0 / 2)
	for i, b := range sent {
		y := float64(1-2*int(b)) + sigma*r.NormFloat64()
		v := math.Round(llrScale * 2 * y / (sigma * sigma))
		llr[i] = int8(max(-127, min(127, v)))
	}
}

// frameErrors decodes frames codewords of the code newC returns at Eb/N0 ebn0 and counts the frames
// whose information bits are not all recovered. Frame i uses its own seed, so the count does not
// depend on scheduling.
func frameErrors(newC func() *Code, ebn0 float64, frames int, seed uint64) int {
	workers := runtime.GOMAXPROCS(0)
	var mu sync.Mutex
	errs := 0
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			c := newC()
			b, _ := Base1944(c.rate)
			rate := float64(c.info) / float64(c.info+N-b.K())
			llr := make([]int8, c.info+N-b.K())
			n := 0
			for f := w; f < frames; f += workers {
				r := rand.New(rand.NewPCG(seed, uint64(f)))
				info := randomBits(r, c.info)
				cw := c.encodeBits(info)
				sent := append(append([]uint8(nil), cw[:c.info]...), cw[b.K():]...)
				channel(r, sent, ebn0, rate, llr)
				c.decodeBits(llr)
				for i, v := range info {
					if c.bit(i) != v {
						n++
						break
					}
				}
			}
			mu.Lock()
			errs += n
			mu.Unlock()
		})
	}
	wg.Wait()
	return errs
}

// maxErrors returns the largest error count k for which observing at most k errors in n frames
// rejects FER >= p at confidence 1 - alpha: P(X <= k | n, p) <= alpha.
func maxErrors(n int, p, alpha float64) int {
	pmf := math.Pow(1-p, float64(n))
	cdf := pmf
	if cdf > alpha {
		return -1
	}
	k := 0
	for {
		pmf *= float64(n-k) / float64(k+1) * p / (1 - p)
		if cdf+pmf > alpha {
			return k
		}
		cdf += pmf
		k++
	}
}

func TestMaxErrors(t *testing.T) {
	for _, c := range []struct{ n, want int }{{100, -1}, {300, 0}, {1000, 4}} {
		if got := maxErrors(c.n, 0.01, 0.05); got != c.want {
			t.Errorf("maxErrors(%d) = %d, want %d", c.n, got, c.want)
		}
	}
}

// TestErrorRate requires FER below 1e-2 at the published Eb/N0 plus 0.5 dB, with 95% confidence.
// 1,000 frames per rate by default; 20,000 with QRVT_LONG_TESTS=1.
func TestErrorRate(t *testing.T) {
	frames := 1000
	if long() {
		frames = 20000
	}
	limit := maxErrors(frames, 1e-2, 0.05)
	for _, ref := range references {
		b, _ := Base1944(ref.rate)
		ebn0 := ref.ebn0 + margin
		n := frameErrors(func() *Code { return newCode(ref.rate, b.K()) }, ebn0, frames, 1)
		t.Logf("rate %v: %d frame errors in %d at Eb/N0 %.2f dB (published FER 1e-2 at %.2f dB)", ref.rate, n, frames, ebn0, ref.ebn0)
		if n > limit {
			t.Errorf("rate %v: %d frame errors in %d at Eb/N0 %.2f dB, more than %d: FER 1e-2 is not reached within %.1f dB of the published %.2f dB",
				ref.rate, n, frames, ebn0, limit, margin, ref.ebn0)
		}
	}
}

// TestErrorRateCurves logs where each mother code and each shortened inner code crosses FER 1e-2,
// for DESIGN.md section 8. Long tests only.
func TestErrorRateCurves(t *testing.T) {
	if !long() {
		t.Skip("set QRVT_LONG_TESTS=1")
	}
	const frames = 4000
	for _, ref := range references {
		b, _ := Base1944(ref.rate)
		logCrossing(t, "mother "+ref.rate.String(), func() *Code { return newCode(ref.rate, b.K()) }, ref.ebn0-0.5, frames)
	}
	for _, id := range ids {
		logCrossing(t, "inner code "+string('0'+rune(id)), func() *Code { return mustNew(t, id) }, 1.0, frames)
	}
}

func logCrossing(t *testing.T, name string, newC func() *Code, from float64, frames int) {
	prevE, prevF := 0.0, 1.0
	for e := from; e < from+4; e += 0.1 {
		fer := float64(frameErrors(newC, e, frames, 2)) / float64(frames)
		t.Logf("%s: Eb/N0 %.2f dB FER %.4f", name, e, fer)
		if fer <= 1e-2 {
			x := e
			if fer > 0 && prevF > 1e-2 {
				x = prevE + 0.1*math.Log(prevF/1e-2)/math.Log(prevF/fer)
			}
			t.Logf("%s: FER 1e-2 at about %.2f dB", name, x)
			return
		}
		prevE, prevF = e, fer
	}
}
