package modem

import (
	"fmt"

	"github.com/platten/qrvideotransfer/internal/frame"
)

// LLRScale is the number of soft-bit units per nat: a soft bit s stands for the log-likelihood ratio
// ln(P(bit 0) / P(bit 1)) = s / LLRScale. Soft bits are clipped to -127..127, so the largest
// confidence expressed is 31.75 nats. Package ldpc and the header decoder consume this scale.
const LLRScale = 4

// MinVariance is the smallest per-level variance LLR uses, in squared 8-bit code values; smaller or
// non-positive variances are raised to it. It is the variance of rounding to whole code values.
const MinVariance = 1.0 / 12

// Constellation is one modulation of docs/FORMAT.md section 7: its levels, their default appearance and
// the mapping between symbols and levels. It implements Modem and is immutable.
type Constellation struct {
	id     uint8
	name   string
	b      int
	planes PlaneMask
	levels []Sample // default appearance by level number
	toLvl  [MaxLevels]uint8
	toSym  [MaxLevels]uint8
}

func grey(ys ...uint16) []Sample {
	s := make([]Sample, len(ys))
	for i, y := range ys {
		s[i] = Sample{y, 128, 128, 8}
	}
	return s
}

// constellations holds modulations 1 to 4 with the default levels of FORMAT.md section 7.
var constellations = func() [4]*Constellation {
	cs := [4]*Constellation{
		{planes: PlaneY, levels: grey(16, 235)},
		{planes: PlaneY, levels: grey(16, 89, 162, 235)},
		{planes: PlaneY | PlaneCb | PlaneCr, levels: []Sample{
			{16, 128, 128, 8}, {32, 240, 118, 8}, {173, 42, 26, 8}, {188, 154, 16, 8},
			{63, 102, 240, 8}, {78, 214, 230, 8}, {219, 16, 138, 8}, {235, 128, 128, 8},
		}},
		{planes: PlaneY},
	}
	for _, y := range []uint16{64, 189, 314, 439, 565, 690, 815, 940} {
		cs[3].levels = append(cs[3].levels, Sample{y, 512, 512, 10})
	}
	for i, c := range cs {
		info, _ := frame.LookupModulation(uint8(i + 1))
		c.id, c.name, c.b = info.ID, info.Name, info.BitsPerCell
		for l := range len(c.levels) {
			s := uint8(l)
			if c.id == 2 || c.id == 4 {
				s ^= s >> 1 // Gray code: level l shows l ^ (l >> 1)
			}
			c.toSym[l], c.toLvl[s] = s, uint8(l)
		}
	}
	return cs
}()

// ByID returns the constellation of a modulation id, or an error naming an unknown id.
func ByID(id uint8) (*Constellation, error) {
	if id < 1 || int(id) > len(constellations) {
		return nil, fmt.Errorf("modem: unknown modulation %d", id)
	}
	return constellations[id-1], nil
}

// ID returns the modulation id of the header.
func (c *Constellation) ID() uint8 { return c.id }

// Name returns the modulation's name in the registry, for example "Gray4".
func (c *Constellation) Name() string { return c.name }

// BitsPerCell returns b.
func (c *Constellation) BitsPerCell() int { return c.b }

// Planes returns the planes the modulation uses.
func (c *Constellation) Planes() PlaneMask { return c.planes }

// Levels returns L, the number of levels.
func (c *Constellation) Levels() int { return len(c.levels) }

// Level returns the default appearance of level l, which must be in 0..L-1. Level 0 is dark and level
// L-1 light (FORMAT.md section 4.4).
func (c *Constellation) Level(l int) Sample { return c.levels[l] }

// LevelOf returns the level number that shows a symbol; bits above b are ignored.
func (c *Constellation) LevelOf(symbol uint8) int { return int(c.toLvl[symbol&c.mask()]) }

// SymbolOf returns the symbol that level l, in 0..L-1, shows.
func (c *Constellation) SymbolOf(l int) uint8 { return c.toSym[l] }

func (c *Constellation) mask() uint8 { return uint8(1)<<c.b - 1 }

// Map returns the default appearance of a symbol; bits above b are ignored.
func (c *Constellation) Map(symbol uint8) Sample { return c.levels[c.LevelOf(symbol)] }

// DefaultStats returns the default levels as LocalStats on the 8-bit scale of Obs, each with the given
// variance. A decoder may start calibration from it but must not rely on it (FORMAT.md section 7).
func (c *Constellation) DefaultStats(variance float32) LocalStats {
	st := LocalStats{Levels: len(c.levels)}
	for l, s := range c.levels {
		k := float32(int(1) << (s.Bits - 8))
		st.Centre[l] = [3]float32{float32(s.Y) / k, float32(s.Cb) / k, float32(s.Cr) / k}
		st.Variance[l] = variance
	}
	return st
}

// LLR writes b soft bits for one cell to dst, most significant bit first: positive means 0, in units
// of 1/LLRScale nat, scaled by weight and clipped to -127..127.
//
// It is the max-log approximation for Gaussian levels: with m(l) = |obs - centre(l)|^2 / (2 var(l)),
// summed over the planes the modulation uses, the soft bit of bit k is the smallest m among levels
// whose symbol has bit k set minus the smallest among those where it is clear. The log-variance term
// of the Gaussian is left out. A saturated or non-finite observation, a non-positive weight, or
// statistics with another number of levels give zeros.
func (c *Constellation) LLR(dst []int8, obs Obs, st *LocalStats, weight float32) {
	dst = dst[:c.b]
	n := len(c.levels)
	if obs.Saturated || st == nil || st.Levels != n || !(weight > 0) {
		clear(dst)
		return
	}
	var best [2][3]float32 // best[v][k]: smallest metric with bit k equal to v
	const inf = float32(3.4e38)
	for k := range c.b {
		best[0][k], best[1][k] = inf, inf
	}
	for l := range n {
		v := st.Variance[l]
		if !(v >= MinVariance) { // also catches NaN
			v = MinVariance
		}
		ce := &st.Centre[l]
		dy := obs.Y - ce[0]
		d := dy * dy
		if c.planes != PlaneY {
			db, dr := obs.Cb-ce[1], obs.Cr-ce[2]
			d += float32(db*db) + float32(dr*dr)
		}
		m := d / (2 * v)
		s := c.toSym[l]
		for k := range c.b {
			bit := s >> (c.b - 1 - k) & 1
			if m < best[bit][k] {
				best[bit][k] = m
			}
		}
	}
	for k := range dst {
		dst[k] = clip(LLRScale * weight * (best[1][k] - best[0][k]))
	}
}

// clip rounds v to the nearest integer, halves away from zero, within -127..127; NaN gives 0.
func clip(v float32) int8 {
	switch {
	case v >= 127:
		return 127
	case v <= -127:
		return -127
	case v >= 0:
		return int8(v + 0.5)
	case v < 0:
		return int8(v - 0.5)
	}
	return 0
}
