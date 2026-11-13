// Package conv is the header code of docs/FORMAT.md section 6.2: the rate-1/2, constraint-length-7
// convolutional code with generators 171 and 133 (octal), and a soft-input Viterbi decoder.
//
// Soft bits are log-likelihood ratios as int8: positive means the bit is more likely 0, negative
// more likely 1, zero means no information.
package conv

// Memory is the number of past input bits the encoder keeps; a terminated message ends with this
// many zero bits.
const Memory = 6

// Encode returns the 2*len(bits) coded bits of bits (one bit per byte, 0 or 1), starting from the
// all-zero state. For input u[t] it emits u[t]^u[t-1]^u[t-2]^u[t-3]^u[t-6], then
// u[t]^u[t-2]^u[t-3]^u[t-5]^u[t-6].
func Encode(bits []uint8) []uint8 {
	return nil
}

// Decode returns the maximum-likelihood input of len(llr)/2 bits for the soft coded bits llr,
// among inputs that start and end in the all-zero state (the last Memory input bits are zero).
// len(llr) must be even.
func Decode(llr []int8) []uint8 {
	return nil
}
