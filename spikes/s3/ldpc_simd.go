//go:build goexperiment.simd && amd64

package s3

import (
	"simd/archsimd"

	"github.com/platten/qrvideotransfer/internal/fec/ldpc"
)

// zp is Z padded to a whole number of 16-lane vectors.
const zp = 96

// SimdDecoder is Decoder written with the experimental simd/archsimd
// package (GOEXPERIMENT=simd, AVX2): the Z = 81 checks of a layer are
// processed 16 at a time. Arithmetic differs from Decoder only in the
// clamping of Q to +-lMax.
type SimdDecoder struct {
	c      *Code
	layers [][]edge
	R      [][][zp]int16
	L      [ldpc.N]int16
	q      [][zp]int16
}

// NewSimdDecoder returns a vector decoder for c.
func NewSimdDecoder(c *Code) *SimdDecoder {
	sd := NewDecoder(c)
	d := &SimdDecoder{c: c, layers: sd.layers}
	maxDeg := 0
	for _, l := range d.layers {
		d.R = append(d.R, make([][zp]int16, len(l)))
		maxDeg = max(maxDeg, len(l))
	}
	d.q = make([][zp]int16, maxDeg)
	return d
}

// Decode is as Decoder.Decode.
func (d *SimdDecoder) Decode(llr []int8, iters int, out []uint8) {
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
	for it := 0; it < iters; it++ {
		for li, layer := range d.layers {
			d.layer(layer, d.R[li])
		}
	}
	for i := 0; i < c.Info; i++ {
		out[i] = uint8(uint16(d.L[i]) >> 15)
	}
}

const nv = zp / 16

func (d *SimdDecoder) layer(layer []edge, R [][zp]int16) {
	var m1, m2, idx, sgn [nv]archsimd.Int16x16
	hi := archsimd.BroadcastInt16x16(lMax)
	lo := archsimd.BroadcastInt16x16(-lMax)
	zero := archsimd.BroadcastInt16x16(0)
	three := archsimd.BroadcastInt16x16(3)
	for k := range m1 {
		m1[k] = archsimd.BroadcastInt16x16(1<<15 - 1)
		m2[k] = m1[k]
	}
	q := d.q[:len(layer)]
	for e, b := range layer {
		blk, s := d.L[b.col*Z:(b.col+1)*Z], b.shift
		buf, Re := &q[e], &R[e]
		ev := archsimd.BroadcastInt16x16(int16(e))
		for k := 0; k < nv; k++ {
			qv := loadRot(blk, s, k*16).SubSaturated(archsimd.LoadInt16x16(Re[k*16:]))
			qv.Store(buf[k*16:])
			a := qv.Abs()
			sgn[k] = sgn[k].Xor(qv)
			lt := a.Less(m1[k])
			m2[k] = m2[k].Min(a.Max(m1[k]))
			idx[k] = ev.IfElse(lt, idx[k])
			m1[k] = m1[k].Min(a)
		}
	}
	for k := 0; k < nv; k++ {
		m1[k] = m1[k].Sub(m1[k].Add(three).ShiftAllRight(2)) // floor(0.75 m)
		m2[k] = m2[k].Sub(m2[k].Add(three).ShiftAllRight(2))
	}
	for e, b := range layer {
		blk, s := d.L[b.col*Z:(b.col+1)*Z], b.shift
		buf, Re := &q[e], &R[e]
		ev := archsimd.BroadcastInt16x16(int16(e))
		for k := 0; k < nv; k++ {
			qv := archsimd.LoadInt16x16(buf[k*16:])
			mag := m2[k].IfElse(idx[k].Equal(ev), m1[k])
			neg := sgn[k].Xor(qv).Less(zero)
			r := mag.Neg().IfElse(neg, mag)
			r.Store(Re[k*16:])
			storeRot(blk, s, k*16, qv.Add(r).Min(hi).Max(lo))
		}
	}
}

// loadRot returns lanes r0..r0+15 of the block rotated by s: lane r holds
// blk[(r+s) mod Z]; lanes at or beyond Z are zero.
func loadRot(blk []int16, s, r0 int) archsimd.Int16x16 {
	if r0+15 < Z-s {
		return archsimd.LoadInt16x16(blk[r0+s:])
	}
	var t [16]int16
	for i := range t {
		if r := r0 + i; r < Z {
			t[i] = blk[(r+s)%Z]
		}
	}
	return archsimd.LoadInt16x16Array(&t)
}

// storeRot is the inverse of loadRot; lanes at or beyond Z are dropped.
func storeRot(blk []int16, s, r0 int, v archsimd.Int16x16) {
	if r0+15 < Z-s {
		v.Store(blk[r0+s:])
		return
	}
	var t [16]int16
	v.StoreArray(&t)
	for i := range t {
		if r := r0 + i; r < Z {
			blk[(r+s)%Z] = t[i]
		}
	}
}
