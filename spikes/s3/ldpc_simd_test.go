//go:build goexperiment.simd && amd64

package s3

import (
	"bytes"
	"math/rand/v2"
	"testing"
	"time"
)

// TestSimdMatchesScalar checks that the vector decoder, whose arithmetic is
// the same as the scalar one, makes the same hard decision on every
// information bit of every frame, and the same posteriors.
func TestSimdMatchesScalar(t *testing.T) {
	c := normal()
	d, v := NewDecoder(c), NewSimdDecoder(c)
	rate := float64(c.Info) / float64(c.Sent)
	rng := rand.New(rand.NewPCG(11, 12))
	o1, o2 := make([]uint8, c.Info), make([]uint8, c.Info)
	fe := 0
	for f := 0; f < 1500; f++ {
		u := randomInfo(rng, c.Info)
		llr := channel(rng, c.SentBits(c.Encode(u)), 2.25, rate)
		d.Decode(llr, 10, o1)
		v.Decode(llr, 10, o2)
		if !bytes.Equal(o1, o2) {
			t.Fatalf("frame %d: decisions differ", f)
		}
		if d.L != v.L {
			t.Fatalf("frame %d: posteriors differ", f)
		}
		if !bytes.Equal(o1, u) {
			fe++
		}
	}
	t.Logf("Eb/N0 2.25 dB, 10 iterations, 1500 frames: identical decisions and posteriors; %d frame errors", fe)
}

func BenchmarkSimdDecode(b *testing.B) {
	c := normal()
	d := NewSimdDecoder(c)
	rng := rand.New(rand.NewPCG(7, 8))
	rate := float64(c.Info) / float64(c.Sent)
	var llrs [][]int8
	for k := 0; k < 64; k++ {
		llrs = append(llrs, channel(rng, c.SentBits(c.Encode(randomInfo(rng, c.Info))), 2.5, rate))
	}
	out := make([]uint8, c.Info)
	start := time.Now()
	for i := 0; b.Loop(); i++ {
		d.Decode(llrs[i%len(llrs)], 10, out)
	}
	b.ReportMetric(float64(b.N)*float64(c.Sent)/time.Since(start).Seconds()/1e6, "Mbit/s")
}
