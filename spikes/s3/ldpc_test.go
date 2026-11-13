package s3

import (
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/fec/ldpc"
)

// normal is the `normal` inner code of FORMAT.md section 8.
func normal() *Code { return NewCode(ldpc.Rate3_4, 1056) }

func randomInfo(rng *rand.Rand, n int) []uint8 {
	u := make([]uint8, n)
	for i := range u {
		u[i] = uint8(rng.IntN(2))
	}
	return u
}

func TestNormalShape(t *testing.T) {
	c := normal()
	if c.K != 1458 || c.Sent != 1542 || c.K-c.Info != 402 {
		t.Fatalf("K=%d sent=%d shortened=%d", c.K, c.Sent, c.K-c.Info)
	}
}

func TestEncodeSatisfiesChecks(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for _, r := range []ldpc.Rate{ldpc.Rate1_2, ldpc.Rate2_3, ldpc.Rate3_4, ldpc.Rate5_6} {
		b, _ := ldpc.Base1944(r)
		c := NewCode(r, b.K()) // unshortened
		for k := 0; k < 20; k++ {
			cw := c.Encode(randomInfo(rng, c.Info))
			if s := c.Syndrome(cw); s != 0 {
				t.Fatalf("rate %v: %d unsatisfied checks", r, s)
			}
		}
	}
	c := normal()
	for k := 0; k < 20; k++ {
		cw := c.Encode(randomInfo(rng, c.Info))
		if s := c.Syndrome(cw); s != 0 {
			t.Fatalf("normal: %d unsatisfied checks", s)
		}
		for i := c.Info; i < c.K; i++ {
			if cw[i] != 0 {
				t.Fatalf("shortened bit %d is 1", i)
			}
		}
	}
	// A single flipped bit must be detected.
	cw := c.Encode(randomInfo(rng, c.Info))
	cw[100] ^= 1
	if c.Syndrome(cw) == 0 {
		t.Fatal("flipped bit not detected")
	}
}

// channel returns quantised BPSK/AWGN LLRs for sent bits at Eb/N0 ebn0 dB,
// for a code of rate R = info/sent. Scale: 4 per unit of LLR, clamped to int8.
func channel(rng *rand.Rand, sent []uint8, ebn0, rate float64) []int8 {
	n0 := 1 / (rate * math.Pow(10, ebn0/10))
	sigma := math.Sqrt(n0 / 2)
	llr := make([]int8, len(sent))
	for i, b := range sent {
		y := 1 - 2*float64(b) + sigma*rng.NormFloat64()
		v := math.Round(4 * 2 * y / (sigma * sigma))
		llr[i] = int8(max(-127, min(127, v)))
	}
	return llr
}

func TestDecodeNoiseless(t *testing.T) {
	c := normal()
	d := NewDecoder(c)
	rng := rand.New(rand.NewPCG(3, 4))
	u := randomInfo(rng, c.Info)
	sent := c.SentBits(c.Encode(u))
	llr := make([]int8, len(sent))
	for i, b := range sent {
		llr[i] = int8(40 - 80*int(b))
	}
	// Erase 10% of the bits and flip 2%: still decodable.
	for i := range llr {
		switch x := rng.IntN(100); {
		case x < 10:
			llr[i] = 0
		case x < 12:
			llr[i] = -llr[i]
		}
	}
	out := make([]uint8, c.Info)
	d.Decode(llr, 10, out)
	for i := range u {
		if out[i] != u[i] {
			t.Fatalf("bit %d wrong", i)
		}
	}
}

// TestFER prints a frame-error-rate table; run with -v. It is a sanity
// check, not a threshold: S3_FER_FRAMES sets the frames per point.
func TestFER(t *testing.T) {
	frames := 300
	if s := os.Getenv("S3_FER_FRAMES"); s != "" {
		fmt.Sscan(s, &frames)
	}
	if testing.Short() {
		frames = 30
	}
	c := normal()
	d := NewDecoder(c)
	rate := float64(c.Info) / float64(c.Sent)
	rng := rand.New(rand.NewPCG(5, 6))
	out := make([]uint8, c.Info)
	for _, iters := range []int{10, 25} {
		for _, ebn0 := range []float64{1.5, 2.0, 2.25, 2.5, 2.75, 3.0} {
			fe, be := 0, 0
			for f := 0; f < frames; f++ {
				u := randomInfo(rng, c.Info)
				llr := channel(rng, c.SentBits(c.Encode(u)), ebn0, rate)
				d.Decode(llr, iters, out)
				n := 0
				for i := range u {
					if out[i] != u[i] {
						n++
					}
				}
				be += n
				if n > 0 {
					fe++
				}
			}
			t.Logf("iters=%d Eb/N0=%.2f dB: FER=%.4f (%d/%d) BER=%.2e", iters, ebn0, float64(fe)/float64(frames), fe, frames, float64(be)/float64(frames*c.Info))
		}
	}
}

// BenchmarkDecode measures single-goroutine throughput at exactly 10
// iterations, in sent bits per second, on LLRs at Eb/N0 = 2.5 dB (the waterfall).
func BenchmarkDecode(b *testing.B) {
	for _, iters := range []int{10, 8, 6} {
		b.Run(fmt.Sprintf("iters=%d", iters), func(b *testing.B) {
			c := normal()
			d := NewDecoder(c)
			rng := rand.New(rand.NewPCG(7, 8))
			rate := float64(c.Info) / float64(c.Sent)
			var llrs [][]int8
			for k := 0; k < 64; k++ {
				llrs = append(llrs, channel(rng, c.SentBits(c.Encode(randomInfo(rng, c.Info))), 2.5, rate))
			}
			out := make([]uint8, c.Info)
			b.ResetTimer()
			start := time.Now()
			for i := 0; b.Loop(); i++ {
				d.Decode(llrs[i%len(llrs)], iters, out)
			}
			el := time.Since(start).Seconds()
			b.ReportMetric(float64(b.N)*float64(c.Sent)/el/1e6, "Mbit/s")
			b.ReportMetric(float64(d.Edges()), "edges/iter")
		})
	}
}

// BenchmarkDecodeParallel runs one decoder per goroutine (GOMAXPROCS).
func BenchmarkDecodeParallel(b *testing.B) {
	c := normal()
	rng := rand.New(rand.NewPCG(9, 10))
	rate := float64(c.Info) / float64(c.Sent)
	var llrs [][]int8
	for k := 0; k < 64; k++ {
		llrs = append(llrs, channel(rng, c.SentBits(c.Encode(randomInfo(rng, c.Info))), 2.5, rate))
	}
	start := time.Now()
	b.RunParallel(func(pb *testing.PB) {
		d := NewDecoder(c)
		out := make([]uint8, c.Info)
		for i := 0; pb.Next(); i++ {
			d.Decode(llrs[i%len(llrs)], 10, out)
		}
	})
	el := time.Since(start).Seconds()
	b.ReportMetric(float64(b.N)*float64(c.Sent)/el/1e6, "Mbit/s")
}

// TestEarlyStop logs the mean iteration count with early stopping (at most
// 25, as DESIGN.md section 8) and the frame error rate; run with -v.
func TestEarlyStop(t *testing.T) {
	c := normal()
	d := NewDecoder(c)
	rate := float64(c.Info) / float64(c.Sent)
	rng := rand.New(rand.NewPCG(13, 14))
	out := make([]uint8, c.Info)
	frames := 300
	if testing.Short() {
		frames = 30
	}
	for _, ebn0 := range []float64{2.25, 2.5, 3.0, 3.5, 4.0} {
		its, fe := 0, 0
		for f := 0; f < frames; f++ {
			u := randomInfo(rng, c.Info)
			its += d.DecodeEarly(channel(rng, c.SentBits(c.Encode(u)), ebn0, rate), 25, out)
			for i := range u {
				if out[i] != u[i] {
					fe++
					break
				}
			}
		}
		t.Logf("early stop, max 25: Eb/N0=%.2f dB mean iterations %.2f FER %d/%d", ebn0, float64(its)/float64(frames), fe, frames)
	}
}

// BenchmarkDecodeEarly measures throughput with early stopping (max 25).
func BenchmarkDecodeEarly(b *testing.B) {
	for _, ebn0 := range []float64{2.5, 3.0} {
		b.Run(fmt.Sprintf("ebn0=%.1f", ebn0), func(b *testing.B) {
			c := normal()
			d := NewDecoder(c)
			rng := rand.New(rand.NewPCG(7, 8))
			rate := float64(c.Info) / float64(c.Sent)
			var llrs [][]int8
			for k := 0; k < 64; k++ {
				llrs = append(llrs, channel(rng, c.SentBits(c.Encode(randomInfo(rng, c.Info))), ebn0, rate))
			}
			out := make([]uint8, c.Info)
			start := time.Now()
			for i := 0; b.Loop(); i++ {
				d.DecodeEarly(llrs[i%len(llrs)], 25, out)
			}
			b.ReportMetric(float64(b.N)*float64(c.Sent)/time.Since(start).Seconds()/1e6, "Mbit/s")
		})
	}
}
