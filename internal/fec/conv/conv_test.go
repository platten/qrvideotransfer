package conv

import (
	"math/rand/v2"
	"os"
	"runtime"
	"slices"
	"sync"
	"testing"
)

// The header code: 112 bits, 6 zero tail bits, 236 coded bits.
const (
	msgBits   = 118
	codedBits = 2 * msgBits
)

func TestEncodeImpulse(t *testing.T) {
	// The impulse response is the generators interleaved: 171 = 1111001, 133 = 1011011.
	got := Encode([]uint8{1, 0, 0, 0, 0, 0, 0})
	want := []uint8{1, 1, 1, 0, 1, 1, 1, 1, 0, 0, 0, 1, 1, 1}
	if !slices.Equal(got, want) {
		t.Errorf("Encode(impulse) = %v, want %v", got, want)
	}
	if got := Encode(nil); len(got) != 0 {
		t.Errorf("Encode(nil) = %v", got)
	}
}

// Encode is linear: the code of a sum is the sum of the codes.
func TestEncodeLinear(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range 100 {
		a, b := randomMessage(r), randomMessage(r)
		sum := make([]uint8, msgBits)
		for i := range sum {
			sum[i] = a[i] ^ b[i]
		}
		ca, cb, cs := Encode(a), Encode(b), Encode(sum)
		for i := range cs {
			if cs[i] != ca[i]^cb[i] {
				t.Fatal("Encode is not linear")
			}
		}
	}
}

func randomMessage(r *rand.Rand) []uint8 {
	m := make([]uint8, msgBits)
	for i := range msgBits - Memory {
		m[i] = uint8(r.IntN(2))
	}
	return m
}

// llr turns coded bits into soft bits of magnitude 16, flipping the listed positions.
func llr(coded []uint8, flips ...int) []int8 {
	s := make([]int8, len(coded))
	for i, c := range coded {
		s[i] = 16
		if c == 1 {
			s[i] = -16
		}
	}
	for _, f := range flips {
		s[f] = -s[f]
	}
	return s
}

func TestDecodeClean(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for range 200 {
		m := randomMessage(r)
		if got := Decode(llr(Encode(m))); !slices.Equal(got, m) {
			t.Fatalf("clean decode failed")
		}
	}
	if got := Decode(nil); len(got) != 0 {
		t.Errorf("Decode(nil) = %v", got)
	}
	// Extreme soft values must not overflow.
	m := randomMessage(r)
	s := llr(Encode(m))
	for i := range s {
		if s[i] < 0 {
			s[i] = -128
		} else {
			s[i] = 127
		}
	}
	if got := Decode(s); !slices.Equal(got, m) {
		t.Error("decode with extreme soft values failed")
	}
}

// TestFreeDistance computes, independently of Decode, the minimum Hamming weight of a non-zero
// codeword of the terminated 118-bit code. It is the free distance, 10.
//
// This is the argument for the error-correction guarantee: two codewords differ in at least 10 of
// the 236 bits, so with at most 4 bits in error the transmitted codeword is at distance <= 4 and
// every other at distance >= 6. A maximum-likelihood decoder therefore always returns the
// transmitted message, and Viterbi decoding of the terminated trellis is maximum-likelihood. The
// tests below check the decoder's side of the argument: exhaustively up to 2 errors (3 with
// QRVT_LONG_TESTS=1), and by sampling and bursts for 3 and 4.
func TestFreeDistance(t *testing.T) {
	const inf = 1 << 30
	w := make([]int, 64) // min weight to reach each state, having left state 0
	for s := range w {
		w[s] = inf
	}
	out := func(state, u int) int { // weight of the two output bits; state holds u[t-1..t-6], msb first
		reg := u<<6 | state
		a := (reg>>6 ^ reg>>5 ^ reg>>4 ^ reg>>3 ^ reg) & 1
		b := (reg>>6 ^ reg>>4 ^ reg>>3 ^ reg>>1 ^ reg) & 1
		return a + b
	}
	w[32] = out(0, 1) // the first step must leave state 0
	best := inf
	for step := 1; step < msgBits; step++ {
		next := make([]int, 64)
		for s := range next {
			next[s] = inf
		}
		for s, ws := range w {
			if ws == inf {
				continue
			}
			for u := range 2 {
				ns := u<<5 | s>>1
				if ns == 0 {
					best = min(best, ws+out(s, u))
					continue
				}
				next[ns] = min(next[ns], ws+out(s, u))
			}
		}
		w = next
	}
	if best != 10 {
		t.Errorf("free distance = %d, want 10", best)
	}
}

func checkFlips(t *testing.T, m []uint8, coded []uint8, flips ...int) bool {
	t.Helper()
	if got := Decode(llr(coded, flips...)); !slices.Equal(got, m) {
		t.Errorf("errors at %v not corrected", flips)
		return false
	}
	return true
}

func TestCorrectsTwoErrorsExhaustive(t *testing.T) {
	m := make([]uint8, msgBits)
	c := Encode(m)
	for i := range codedBits {
		checkFlips(t, m, c, i)
		for j := i + 1; j < codedBits; j++ {
			if !checkFlips(t, m, c, i, j) {
				return
			}
		}
	}
}

// TestCorrectsThreeErrors checks all C(236, 3) patterns on the all-zero message with
// QRVT_LONG_TESTS=1, and 20,000 random patterns on random messages otherwise. By linearity the
// all-zero message is representative.
func TestCorrectsThreeErrors(t *testing.T) {
	if os.Getenv("QRVT_LONG_TESTS") == "" {
		randomErrors(t, 3, 20000)
		return
	}
	m := make([]uint8, msgBits)
	c := Encode(m)
	var wg sync.WaitGroup
	work := make(chan int)
	var mu sync.Mutex
	var failed [][3]int
	for range runtime.GOMAXPROCS(0) {
		wg.Go(func() {
			for i := range work {
				for j := i + 1; j < codedBits; j++ {
					for k := j + 1; k < codedBits; k++ {
						if !slices.Equal(Decode(llr(c, i, j, k)), m) {
							mu.Lock()
							failed = append(failed, [3]int{i, j, k})
							mu.Unlock()
						}
					}
				}
			}
		})
	}
	for i := range codedBits {
		work <- i
	}
	close(work)
	wg.Wait()
	if len(failed) > 0 {
		t.Errorf("%d patterns not corrected, first %v", len(failed), failed[0])
	}
}

func TestCorrectsFourErrorsSampled(t *testing.T) {
	randomErrors(t, 4, 50000)
}

func randomErrors(t *testing.T, n, trials int) {
	r := rand.New(rand.NewPCG(uint64(n), 99))
	for range trials {
		m := randomMessage(r)
		flips := r.Perm(codedBits)[:n]
		if !checkFlips(t, m, Encode(m), flips...) {
			return
		}
	}
}

// Every burst of up to 4 adjacent errors, and every pattern of 4 errors within a window of 8, at
// every position.
func TestCorrectsBursts(t *testing.T) {
	m := make([]uint8, msgBits)
	c := Encode(m)
	for start := range codedBits {
		for n := 1; n <= 4 && start+n <= codedBits; n++ {
			flips := make([]int, n)
			for i := range flips {
				flips[i] = start + i
			}
			checkFlips(t, m, c, flips...)
		}
		for mask := range 1 << 7 { // start plus 3 more of the next 7
			flips := []int{start}
			for b := range 7 {
				if mask>>b&1 == 1 && start+1+b < codedBits {
					flips = append(flips, start+1+b)
				}
			}
			if len(flips) == 4 && !checkFlips(t, m, c, flips...) {
				return
			}
		}
	}
}

func BenchmarkDecode(b *testing.B) {
	s := llr(Encode(make([]uint8, msgBits)), 3, 50, 100)
	for b.Loop() {
		Decode(s)
	}
}
