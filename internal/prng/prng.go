// Package prng holds the deterministic pseudo-random primitives of docs/FORMAT.md section 1:
// SplitMix64, Shuffle and Seed. Every permutation and scrambling sequence of the format derives
// from them, so they are frozen with the format.
package prng

import (
	"crypto/sha256"
	"encoding/binary"
	"math/bits"
)

// Golden is the SplitMix64 state increment.
const Golden = 0x9E3779B97F4A7C15

// SplitMix64 is the generator of FORMAT.md section 1. The zero value has state 0.
type SplitMix64 struct {
	State uint64
}

// Next advances the state and returns the next output.
func (g *SplitMix64) Next() uint64 {
	g.State += Golden
	return mix(g.State)
}

func mix(z uint64) uint64 {
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// At returns output number k (counting from 0) of a generator started in state seed, without
// generating the outputs before it. SplitMix64 is a counter-based generator, so this is O(1).
func At(seed uint64, k uint64) uint64 {
	return mix(seed + (k+1)*Golden)
}

// Shuffle returns the permutation Shuffle(n, seed) of FORMAT.md section 1. It returns nil for
// n <= 0. n must not exceed 2^32.
func Shuffle(n int, seed uint64) []uint32 {
	if n <= 0 {
		return nil
	}
	a := make([]uint32, n)
	for i := range a {
		a[i] = uint32(i)
	}
	g := SplitMix64{State: seed}
	for i := n - 1; i > 0; i-- {
		j, _ := bits.Mul64(g.Next(), uint64(i)+1) // floor(next * (i+1) / 2^64)
		a[i], a[j] = a[j], a[i]
	}
	return a
}

// Seed returns Seed(label, b): the first 8 bytes of SHA-256(label | b) as a big-endian integer.
func Seed(label string, b []byte) uint64 {
	h := sha256.New()
	h.Write([]byte(label))
	h.Write(b)
	return binary.BigEndian.Uint64(h.Sum(nil))
}
