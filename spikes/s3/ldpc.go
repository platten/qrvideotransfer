// Package s3 is the throwaway M0 spike S3: a layered normalised min-sum
// decoder for the shortened 802.11n LDPC code used by the `normal`
// protection level (FORMAT.md section 8), to measure its speed.
// See docs/spikes/S3.md.
package s3

import (
	"github.com/platten/qrvideotransfer/internal/fec/ldpc"
)

// Z is the lifting size.
const Z = ldpc.Z

// Code is a shortened 802.11n LDPC code of length 1944.
type Code struct {
	Base     ldpc.BaseMatrix
	K        int // mother information bits
	Info     int // information bits actually carried
	Sent     int // Info + parity bits
	kb, m    int // information block columns, block rows
	shortBlk int // first block column that is entirely shortened
}

// NewCode returns the code of rate r carrying info bits.
func NewCode(r ldpc.Rate, info int) *Code {
	b, _ := ldpc.Base1944(r)
	m := len(b.Shift)
	c := &Code{Base: b, K: b.K(), Info: info, kb: ldpc.BaseCols - m, m: m}
	c.Sent = info + ldpc.N - c.K
	c.shortBlk = (info + Z - 1) / Z
	return c
}

// rot returns P^s x: (P^s x)[r] = x[(r+s) mod Z].
func rot(dst, x []uint8, s int) {
	for r := 0; r < Z; r++ {
		dst[r] ^= x[(r+s)%Z]
	}
}

// Encode returns the 1944-bit mother codeword (one bit per byte) for the
// Info information bits u: u, then K-Info zeros, then the parity bits.
func (c *Code) Encode(u []uint8) []uint8 {
	cw := make([]uint8, ldpc.N)
	copy(cw, u[:c.Info])
	lam := make([][]uint8, c.m)
	for i := range lam {
		lam[i] = make([]uint8, Z)
		for j := 0; j < c.kb; j++ {
			if s := c.Base.Shift[i][j]; s >= 0 {
				rot(lam[i], cw[j*Z:(j+1)*Z], s)
			}
		}
	}
	q := func(j int) []uint8 { return cw[(c.kb+j)*Z : (c.kb+j+1)*Z] }
	// q0 is the sum of all lambda: the dual diagonal cancels and the first
	// parity column (shifts 1, 0, 1) sums to the identity.
	for i := 0; i < c.m; i++ {
		for r := 0; r < Z; r++ {
			q(0)[r] ^= lam[i][r]
		}
	}
	for i := 0; i < c.m-1; i++ {
		next := q(i + 1)
		copy(next, lam[i])
		if s := c.Base.Shift[i][c.kb]; s >= 0 {
			rot(next, q(0), s)
		}
		if i >= 1 {
			for r := 0; r < Z; r++ {
				next[r] ^= q(i)[r]
			}
		}
	}
	return cw
}

// Syndrome returns the number of unsatisfied parity checks of cw.
func (c *Code) Syndrome(cw []uint8) int {
	bad := 0
	for i := 0; i < c.m; i++ {
		for r := 0; r < Z; r++ {
			var p uint8
			for j := 0; j < ldpc.BaseCols; j++ {
				if s := c.Base.Shift[i][j]; s >= 0 {
					p ^= cw[j*Z+(r+s)%Z]
				}
			}
			bad += int(p)
		}
	}
	return bad
}

// Sent returns the sent bits of a mother codeword: info then parity.
func (c *Code) SentBits(cw []uint8) []uint8 {
	return append(append([]uint8(nil), cw[:c.Info]...), cw[c.K:]...)
}

// edge is one non-empty block of a layer.
type edge struct {
	col, shift int
}

// Decoder is a layered normalised min-sum decoder (scaling 0.75) with
// int16 arithmetic. Block columns that are entirely shortened are known
// to be zero and are left out of the schedule: a known-zero variable
// never changes a check's sign and is never its minimum.
type Decoder struct {
	c      *Code
	layers [][]edge
	R      [][][Z]int16 // check-to-variable messages per layer and edge
	L      [ldpc.N]int16
	q      [][Z]int16
}

const lMax = 1<<14 - 1 // posterior clamp; keeps Q = L - R within int16

// NewDecoder returns a decoder for c.
func NewDecoder(c *Code) *Decoder {
	d := &Decoder{c: c}
	maxDeg := 0
	for i := 0; i < c.m; i++ {
		var l []edge
		for j := 0; j < ldpc.BaseCols; j++ {
			if s := c.Base.Shift[i][j]; s >= 0 && !(j >= c.shortBlk && j < c.kb) {
				l = append(l, edge{j, s})
			}
		}
		d.layers = append(d.layers, l)
		d.R = append(d.R, make([][Z]int16, len(l)))
		maxDeg = max(maxDeg, len(l))
	}
	d.q = make([][Z]int16, maxDeg)
	return d
}

// Edges returns the number of edges (in bits) the decoder visits per iteration.
func (d *Decoder) Edges() int {
	n := 0
	for _, l := range d.layers {
		n += len(l) * Z
	}
	return n
}

// Decode runs exactly iters iterations on the sent-bit LLRs llr (positive
// means 0) and writes the hard decisions of the Info bits to out.
func (d *Decoder) Decode(llr []int8, iters int, out []uint8) {
	c := d.c
	for i := 0; i < c.Info; i++ {
		d.L[i] = int16(llr[i])
	}
	for i := c.Info; i < c.K; i++ {
		d.L[i] = lMax
	}
	for i := c.K; i < ldpc.N; i++ {
		d.L[i] = int16(llr[c.Info+i-c.K])
	}
	for li := range d.R {
		clear(d.R[li])
	}
	d.run(iters, false)
	for i := 0; i < c.Info; i++ {
		out[i] = uint8(uint16(d.L[i]) >> 15)
	}
}

// DecodeEarly is Decode with at most iters iterations, stopping as soon as
// the hard decisions satisfy every check. It returns the iterations run.
func (d *Decoder) DecodeEarly(llr []int8, iters int, out []uint8) int {
	c := d.c
	for i := 0; i < c.Info; i++ {
		d.L[i] = int16(llr[i])
	}
	for i := c.Info; i < c.K; i++ {
		d.L[i] = lMax
	}
	for i := c.K; i < ldpc.N; i++ {
		d.L[i] = int16(llr[c.Info+i-c.K])
	}
	for li := range d.R {
		clear(d.R[li])
	}
	n := d.run(iters, true)
	for i := 0; i < c.Info; i++ {
		out[i] = uint8(uint16(d.L[i]) >> 15)
	}
	return n
}

func (d *Decoder) run(iters int, early bool) int {
	for it := 0; it < iters; it++ {
		for li, layer := range d.layers {
			d.layer(layer, d.R[li])
		}
		if early && d.checksPass() {
			return it + 1
		}
	}
	return iters
}

// checksPass reports whether the hard decisions of L satisfy every check.
func (d *Decoder) checksPass() bool {
	var p [Z]int16
	for _, layer := range d.layers {
		clear(p[:])
		for _, b := range layer {
			blk := d.L[b.col*Z : (b.col+1)*Z]
			for r := 0; r < Z; r++ {
				p[r] ^= blk[(r+b.shift)%Z]
			}
		}
		for r := 0; r < Z; r++ {
			if p[r] < 0 {
				return false
			}
		}
	}
	return true
}

func (d *Decoder) layer(layer []edge, R [][Z]int16) {
	var m1, m2 [Z]int16
	var idx [Z]int16
	var sgn [Z]int16
	for r := range m1 {
		m1[r], m2[r] = 1<<15-1, 1<<15-1
	}
	q := d.q[:len(layer)]
	// Pass 1: variable-to-check messages and the two smallest magnitudes.
	for e, b := range layer {
		base, s := b.col*Z, b.shift
		Q, Re := &q[e], &R[e]
		lo := d.L[base+s : base+Z]
		hi := d.L[base : base+s]
		n1 := Z - s
		for r := 0; r < n1; r++ {
			Q[r] = lo[r] - Re[r]
		}
		for r := n1; r < Z; r++ {
			Q[r] = hi[r-n1] - Re[r]
		}
		for r := 0; r < Z; r++ {
			v := Q[r]
			sv := v >> 15
			a := (v ^ sv) - sv
			sgn[r] ^= sv
			// Branch-free: the comparisons are unpredictable on noisy input.
			o1 := m1[r]
			m2[r] = min(m2[r], max(a, o1))
			m1[r] = min(o1, a)
			lt := (a - o1) >> 15 // -1 when a < o1
			idx[r] = idx[r]&^lt | int16(e)&lt
		}
	}
	// Pass 2: new check-to-variable messages and posteriors.
	for r := 0; r < Z; r++ {
		m1[r] -= (m1[r] + 3) >> 2 // floor(0.75 m)
		m2[r] -= (m2[r] + 3) >> 2
	}
	for e, b := range layer {
		base, s := b.col*Z, b.shift
		Q, Re := &q[e], &R[e]
		for r := 0; r < Z; r++ {
			d := idx[r] ^ int16(e)
			eq := ^((d | -d) >> 15) // -1 when idx == e
			mag := m1[r] ^ (m1[r]^m2[r])&eq
			sg := sgn[r] ^ (Q[r] >> 15)
			Re[r] = (mag ^ sg) - sg
		}
		lo := d.L[base+s : base+Z]
		hi := d.L[base : base+s]
		n1 := Z - s
		for r := 0; r < n1; r++ {
			lo[r] = clamp(int32(Q[r]) + int32(Re[r]))
		}
		for r := n1; r < Z; r++ {
			hi[r-n1] = clamp(int32(Q[r]) + int32(Re[r]))
		}
	}
}

func clamp(v int32) int16 { return int16(min(max(v, -lMax), lMax)) }
