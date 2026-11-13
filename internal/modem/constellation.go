package modem

import "fmt"

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

// ByID returns the constellation of a modulation id, or an error naming an unknown id.
func ByID(id uint8) (*Constellation, error) {
	return nil, fmt.Errorf("modem: unknown modulation %d", id)
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
func (c *Constellation) Level(l int) Sample { return Sample{} }

// LevelOf returns the level number that shows a symbol; bits above b are ignored.
func (c *Constellation) LevelOf(symbol uint8) int { return 0 }

// SymbolOf returns the symbol that level l shows.
func (c *Constellation) SymbolOf(l int) uint8 { return 0 }

// Map returns the default appearance of a symbol; bits above b are ignored.
func (c *Constellation) Map(symbol uint8) Sample { return Sample{} }

// DefaultStats returns the default levels as LocalStats on the 8-bit scale of Obs, each with the given
// variance. A decoder may start calibration from it but must not rely on it (FORMAT.md section 7).
func (c *Constellation) DefaultStats(variance float32) LocalStats { return LocalStats{} }

// LLR writes b soft bits for one cell to dst, most significant bit first: positive means 0, in units
// of 1/LLRScale nat, scaled by weight and clipped to -127..127.
func (c *Constellation) LLR(dst []int8, obs Obs, st *LocalStats, weight float32) {}
