package ldpc

import (
	"fmt"
	"hash/crc32"
)

// MaxIterations is the most decoding iterations spent on one codeword (docs/SECURITY.md section 5).
const MaxIterations = 25

// Soft-value bounds of the decoder. Posteriors are clamped to +-lMax and check-to-variable
// magnitudes to rMax, so every variable-to-check message Q = L - R stays within +-qMax without
// saturation: |L - R| <= 8191 + 24573 = qMax. Known (shortened) bits send qMax.
const (
	lMax = 1<<13 - 1
	qMax = 32764
	rMax = qMax - lMax
)

// rule is the check-node update of the min-sum family: the two smallest input magnitudes m become
// min(max(floor(scale m / 16) - offset, 0), rMax), and soft bits enter the decoder multiplied by
// 1 << shift.
type rule struct {
	scale, offset int16
	shift         uint
}

// decoderRule is the shipped check-node update: normalised min-sum with factor 13/16, soft bits
// multiplied by 4 inside so that small messages keep their precision. It was chosen by measurement
// over offset min-sum and the factor 0.75 of M1 (DESIGN.md section 8, TestDecoderCandidates).
var decoderRule = rule{scale: 13, shift: 2}

// shapes maps an inner-code id to its mother rate and shard size (FORMAT.md section 8).
var shapes = [...]struct {
	rate  Rate
	shard int
}{1: {Rate5_6, 192}, 2: {Rate3_4, 128}, 3: {Rate2_3, 128}, 4: {Rate1_2, 64}}

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// edge is one non-empty block of a layer: a block column and its shift. known lists the rows q of
// the block whose variable, bit col*Z + (q+shift) mod Z, is a known zero; it is set only for the
// one block column that is partly shortened.
type edge struct {
	col, shift int
	known      []int
}

// Code is one shortened inner code of docs/FORMAT.md section 8. It implements fec.InnerCode.
//
// The sent bits are packed into bytes most significant bit first: sent bit t is bit 7 - t mod 8 of
// byte t div 8, and the unused low bits of the last byte are zero. Soft bits are log-likelihood
// ratios, positive meaning 0, as in frame.DecodeHeader.
//
// A Code holds its encoder's and decoder's working memory, so it is not safe for concurrent use:
// each worker makes its own with New.
type Code struct {
	id    uint8
	shard int // bytes; 0 for an unshortened mother code without CRC (tests only)
	rate  Rate
	base  BaseMatrix
	info  int // information bits carried, including the CRC
	k     int // information bits of the mother code
	kb, m int // information block columns, block rows
	rule  rule

	layers [][]edge
	rOff   []int // offset of each layer's messages in r

	cw  [N]uint8 // mother codeword, one bit per byte
	u   []uint8  // information bits
	lam [][Z]uint8
	l   [N]int16   // posteriors
	r   [][Z]int16 // check-to-variable messages, per layer and edge
	q   [][Z]int16 // variable-to-check messages of the current layer
}

// New returns the inner code with header id 1 to 4, or an error naming an unknown id.
func New(id uint8) (*Code, error) {
	if id < 1 || int(id) >= len(shapes) {
		return nil, fmt.Errorf("ldpc: unknown inner code %d", id)
	}
	s := shapes[id]
	c := newCode(s.rate, 8*s.shard+32)
	c.id, c.shard = id, s.shard
	return c, nil
}

// newCode returns the code of rate r carrying info bits: the mother code shortened by k - info bits.
func newCode(r Rate, info int) *Code {
	b, _ := Base1944(r)
	c := &Code{rate: r, base: b, info: info, k: b.K(), m: len(b.Shift), rule: decoderRule}
	c.kb = BaseCols - c.m
	c.u = make([]uint8, info)
	c.lam = make([][Z]uint8, c.m)
	first := (info + Z - 1) / Z // first block column that is entirely shortened
	maxDeg := 0
	for _, row := range b.Shift {
		var layer []edge
		for j, s := range row {
			// A known-zero variable never changes a check's sign and is never its smallest
			// magnitude, so entirely shortened block columns are left out.
			if s < 0 || j >= first && j < c.kb {
				continue
			}
			e := edge{col: j, shift: s}
			if j == info/Z && info%Z != 0 {
				for q := range Z {
					if (q+s)%Z >= info%Z {
						e.known = append(e.known, q)
					}
				}
			}
			layer = append(layer, e)
		}
		c.layers = append(c.layers, layer)
		c.rOff = append(c.rOff, len(c.r))
		c.r = append(c.r, make([][Z]int16, len(layer))...)
		maxDeg = max(maxDeg, len(layer))
	}
	c.q = make([][Z]int16, maxDeg)
	return c
}

// ID returns the inner-code id of the header.
func (c *Code) ID() uint8 { return c.id }

// ShardSize returns the shard size in bytes.
func (c *Code) ShardSize() int { return c.shard }

// N returns the number of sent bits per codeword.
func (c *Code) N() int { return c.info + N - c.k }

// Rate returns the rate of the mother code.
func (c *Code) Rate() Rate { return c.rate }

// Encode appends the CRC-32C to shard, encodes, drops the shortened bits and packs the N sent bits
// into dst. It panics unless shard is ShardSize bytes and dst holds at least (N+7)/8 bytes.
func (c *Code) Encode(dst, shard []byte) {
	n := (c.N() + 7) / 8
	if len(shard) != c.shard || len(dst) < n {
		panic(fmt.Sprintf("ldpc: Encode with a %d-byte shard into %d bytes, want %d into %d", len(shard), len(dst), c.shard, n))
	}
	crc := crc32.Checksum(shard, castagnoli)
	for i := range c.u {
		if i < 8*c.shard {
			c.u[i] = shard[i/8] >> (7 - i%8) & 1
		} else {
			c.u[i] = uint8(crc >> (31 - (i - 8*c.shard)) & 1)
		}
	}
	cw := c.encodeBits(c.u)
	clear(dst[:n])
	for t := range c.N() {
		v := t
		if t >= c.info {
			v += c.k - c.info
		}
		dst[t/8] |= cw[v] << (7 - t%8)
	}
}

// encodeBits returns the mother codeword of the info bits u: u, then k - info zeros, then the
// parity. The result is the Code's own buffer. Encoding is systematic through the dual-diagonal
// parity structure: the first parity block is the sum of the per-row syndromes of the information
// part, and each later one follows from the row before it.
func (c *Code) encodeBits(u []uint8) []uint8 {
	cw := c.cw[:]
	clear(cw)
	copy(cw, u[:c.info])
	for i := range c.lam {
		lam := &c.lam[i]
		clear(lam[:])
		for j := range c.kb {
			if s := c.base.Shift[i][j]; s >= 0 {
				rotXor(lam, cw[j*Z:(j+1)*Z], s)
			}
		}
	}
	p := func(j int) []uint8 { return cw[(c.kb+j)*Z : (c.kb+j+1)*Z] }
	// The dual diagonal cancels in the sum of all rows, and the first parity column (shifts 1, 0,
	// 1) sums to the identity.
	p0 := p(0)
	for i := range c.lam {
		for q := range Z {
			p0[q] ^= c.lam[i][q]
		}
	}
	for i := 0; i < c.m-1; i++ {
		next := p(i + 1)
		copy(next, c.lam[i][:])
		if s := c.base.Shift[i][c.kb]; s >= 0 {
			rotXor((*[Z]uint8)(next), p0, s)
		}
		if i >= 1 {
			for q := range Z {
				next[q] ^= p(i)[q]
			}
		}
	}
	return cw
}

// rotXor adds the block x shifted by s to dst: dst[q] ^= x[(q+s) mod Z].
func rotXor(dst *[Z]uint8, x []uint8, s int) {
	for q := range Z {
		dst[q] ^= x[(q+s)%Z]
	}
}

// Decode decodes N soft bits into shard and reports the iterations used and whether every parity
// check passed and the CRC matched. It returns 0 and false when shard is not ShardSize bytes or llr
// not N soft bits. On failure the contents of shard are unspecified.
func (c *Code) Decode(shard []byte, llr []int8) (iters int, ok bool) {
	if len(shard) != c.shard || len(llr) != c.N() {
		return 0, false
	}
	iters, ok = c.decodeBits(llr)
	clear(shard)
	for i := range 8 * c.shard {
		shard[i/8] |= c.bit(i) << (7 - i%8)
	}
	var crc uint32
	for i := 8 * c.shard; i < c.info; i++ {
		crc = crc<<1 | uint32(c.bit(i))
	}
	return iters, ok && crc == crc32.Checksum(shard, castagnoli)
}

// bit returns the hard decision on bit i of the mother codeword after decodeBits.
func (c *Code) bit(i int) uint8 {
	if c.l[i] < 0 {
		return 1
	}
	return 0
}

// decodeBits runs the layered min-sum decoder with c.rule on the N soft bits llr, stopping when the
// hard decisions satisfy every check (before the first iteration if they already do) or after
// MaxIterations. It reports the iterations run and whether the checks passed.
func (c *Code) decodeBits(llr []int8) (iters int, ok bool) {
	for i := range c.info {
		c.l[i] = int16(llr[i]) << c.rule.shift
	}
	for i := c.info; i < c.k; i++ {
		c.l[i] = lMax
	}
	for i := c.k; i < N; i++ {
		c.l[i] = int16(llr[c.info+i-c.k]) << c.rule.shift
	}
	clear(c.r)
	if c.checksPass() {
		return 0, true
	}
	for it := 1; it <= MaxIterations; it++ {
		for li, layer := range c.layers {
			c.layer(layer, c.r[c.rOff[li]:c.rOff[li]+len(layer)])
		}
		if c.checksPass() {
			return it, true
		}
	}
	return MaxIterations, false
}

// checksPass reports whether the hard decisions satisfy every check. XOR of int16 values gives the
// parity of their sign bits.
func (c *Code) checksPass() bool {
	var p [Z]int16
	for _, layer := range c.layers {
		clear(p[:])
		for _, e := range layer {
			blk := c.l[e.col*Z : (e.col+1)*Z]
			n1 := Z - e.shift
			for q := range n1 {
				p[q] ^= blk[q+e.shift]
			}
			for q := n1; q < Z; q++ {
				p[q] ^= blk[q-n1]
			}
		}
		for _, v := range p {
			if v < 0 {
				return false
			}
		}
	}
	return true
}

// layer processes one block row: Q = L - R, then new R from the two smallest |Q| mapped by c.rule
// and the sign product, then L = Q + R. The 81 rotated posteriors of each block are read as two
// contiguous runs.
func (c *Code) layer(layer []edge, r [][Z]int16) {
	var m1, m2, idx, sgn [Z]int16
	for q := range m1 {
		m1[q], m2[q] = 1<<15-1, 1<<15-1
	}
	qs := c.q[:len(layer)]
	for e, b := range layer {
		base, s := b.col*Z, b.shift
		Q, R := &qs[e], &r[e]
		lo, hi := c.l[base+s:base+Z], c.l[base:base+s]
		n1 := Z - s
		for q := range n1 {
			Q[q] = lo[q] - R[q]
		}
		for q := n1; q < Z; q++ {
			Q[q] = hi[q-n1] - R[q]
		}
		for _, q := range b.known {
			Q[q] = qMax
		}
		for q := range Z {
			v := Q[q]
			sv := v >> 15
			a := (v ^ sv) - sv
			sgn[q] ^= sv
			// Branch-free: the comparisons are unpredictable on noisy input.
			o1 := m1[q]
			m2[q] = min(m2[q], max(a, o1))
			m1[q] = min(o1, a)
			lt := (a - o1) >> 15 // -1 when a < o1
			idx[q] = idx[q]&^lt | int16(e)&lt
		}
	}
	for q := range Z {
		m1[q], m2[q] = c.rule.apply(m1[q]), c.rule.apply(m2[q])
	}
	for e, b := range layer {
		base, s := b.col*Z, b.shift
		Q, R := &qs[e], &r[e]
		for q := range Z {
			d := idx[q] ^ int16(e)
			eq := ^((d | -d) >> 15) // -1 when idx == e
			mag := m1[q] ^ (m1[q]^m2[q])&eq
			sg := sgn[q] ^ Q[q]>>15
			R[q] = (mag ^ sg) - sg
		}
		lo, hi := c.l[base+s:base+Z], c.l[base:base+s]
		n1 := Z - s
		for q := range n1 {
			lo[q] = clamp(int32(Q[q]) + int32(R[q]))
		}
		for q := n1; q < Z; q++ {
			hi[q-n1] = clamp(int32(Q[q]) + int32(R[q]))
		}
	}
}

// apply maps a smallest input magnitude m (0 to 32767) to the output magnitude of the rule.
func (r rule) apply(m int16) int16 {
	s := (m>>4)*r.scale + (m&15)*r.scale>>4 // floor(scale m / 16) without overflow
	return min(max(s-r.offset, 0), rMax)
}

func clamp(v int32) int16 { return int16(min(max(v, -lMax), lMax)) }
