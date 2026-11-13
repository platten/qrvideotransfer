// Package conv is the header code of docs/FORMAT.md section 6.2: the rate-1/2, constraint-length-7
// convolutional code with generators 171 and 133 (octal), and a soft-input Viterbi decoder.
//
// Soft bits are log-likelihood ratios as int8: positive means the bit is more likely 0, negative
// more likely 1, zero means no information.
package conv

// Memory is the number of past input bits the encoder keeps; a terminated message ends with this
// many zero bits.
const Memory = 6

// Generators, as masks over the register u[t] u[t-1] ... u[t-6] (most significant first).
const (
	g0 = 0o171
	g1 = 0o133
)

// outputs[reg] holds the two coded bits (first in bit 1) for the register reg = u[t] << 6 | state,
// where state holds u[t-1] .. u[t-6], most significant first.
var outputs = func() (o [128]uint8) {
	for reg := range o {
		o[reg] = parity(reg&g0)<<1 | parity(reg&g1)
	}
	return o
}()

func parity(v int) uint8 {
	p := 0
	for ; v != 0; v &= v - 1 {
		p ^= 1
	}
	return uint8(p)
}

// Encode returns the 2*len(bits) coded bits of bits (one bit per byte, 0 or 1), starting from the
// all-zero state. For input u[t] it emits u[t]^u[t-1]^u[t-2]^u[t-3]^u[t-6], then
// u[t]^u[t-2]^u[t-3]^u[t-5]^u[t-6].
func Encode(bits []uint8) []uint8 {
	out := make([]uint8, 0, 2*len(bits))
	state := 0
	for _, u := range bits {
		reg := int(u&1)<<6 | state
		o := outputs[reg]
		out = append(out, o>>1, o&1)
		state = reg >> 1
	}
	return out
}

// Decode returns the maximum-likelihood input of len(llr)/2 bits for the soft coded bits llr,
// among inputs that start and end in the all-zero state (the last Memory input bits are zero).
// A trailing odd soft bit is ignored.
func Decode(llr []int8) []uint8 {
	const lost = -1 << 28 // metric of an unreachable state
	n := len(llr) / 2
	var pm, next [64]int32
	for s := range pm {
		pm[s] = lost
	}
	pm[0] = 0
	decisions := make([]uint64, n) // bit s: which predecessor won for state s
	for t := range n {
		l0, l1 := int32(llr[2*t]), int32(llr[2*t+1])
		bm := [4]int32{l0 + l1, l0 - l1, -l0 + l1, -l0 - l1} // indexed by the coded bit pair
		var d uint64
		for ns := range 64 {
			u, base := ns>>5, (ns<<1)&63
			m0 := pm[base] + bm[outputs[u<<6|base]]
			m1 := pm[base|1] + bm[outputs[u<<6|base|1]]
			if m1 > m0 {
				next[ns], d = m1, d|1<<ns
			} else {
				next[ns] = m0
			}
		}
		pm, decisions[t] = next, d
	}
	out := make([]uint8, n)
	s := 0
	for t := n - 1; t >= 0; t-- {
		out[t] = uint8(s >> 5)
		s = (s<<1)&63 | int(decisions[t]>>s&1)
	}
	return out
}
