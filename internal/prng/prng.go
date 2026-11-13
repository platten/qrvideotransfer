// Package prng holds the deterministic pseudo-random primitives of docs/FORMAT.md section 1:
// SplitMix64, Shuffle and Seed. Every permutation and scrambling sequence of the format derives
// from them, so they are frozen with the format.
package prng

// Golden is the SplitMix64 state increment.
const Golden = 0x9E3779B97F4A7C15

// SplitMix64 is the generator of FORMAT.md section 1. The zero value has state 0.
type SplitMix64 struct {
	State uint64
}

// Next advances the state and returns the next output.
func (g *SplitMix64) Next() uint64 {
	return 0
}

// At returns output number k (counting from 0) of a generator started in state seed, without
// generating the outputs before it. SplitMix64 is a counter-based generator, so this is O(1).
func At(seed uint64, k uint64) uint64 {
	return 0
}

// Shuffle returns the permutation Shuffle(n, seed) of FORMAT.md section 1. It returns nil for
// n <= 0. n must not exceed 2^32.
func Shuffle(n int, seed uint64) []uint32 {
	return nil
}

// Seed returns Seed(label, b): the first 8 bytes of SHA-256(label | b) as a big-endian integer.
func Seed(label string, b []byte) uint64 {
	return 0
}
