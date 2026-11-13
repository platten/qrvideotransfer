// Package modem maps bits to cell appearances and cell observations back to soft bits: the
// constellations of docs/FORMAT.md section 7, their bit mapping, and log-likelihood ratios.
package modem

// Sample is the appearance of one cell: Y', Cb and Cr code values at a bit depth.
type Sample struct {
	Y, Cb, Cr uint16
	Bits      uint8 // 8 or 10
}

// PlaneMask says which image planes a modulation uses.
type PlaneMask uint8

// Planes.
const (
	PlaneY PlaneMask = 1 << iota
	PlaneCb
	PlaneCr
)

// Obs is one cell's observation: the mean value per plane, normalised to the 8-bit scale, and whether
// any sample was saturated.
type Obs struct {
	Y, Cb, Cr float32
	Saturated bool
}

// MaxLevels is the largest number of levels of any modulation.
const MaxLevels = 8

// LocalStats is a centre and a variance per level for one tile of cells.
type LocalStats struct {
	Levels   int                   // number of levels in use
	Centre   [MaxLevels][3]float32 // mean Y, Cb, Cr per level
	Variance [MaxLevels]float32    // pooled variance per level
}

// Modem is one modulation of docs/FORMAT.md section 7.
type Modem interface {
	// ID returns the modulation id of the header.
	ID() uint8
	// BitsPerCell returns b.
	BitsPerCell() int
	// Planes returns the planes the modulation uses.
	Planes() PlaneMask
	// Map returns the default appearance of a symbol of b bits.
	Map(symbol uint8) Sample
	// LLR writes b soft bits for the observation to dst (positive means 0), scaled by weight.
	LLR(dst []int8, obs Obs, st *LocalStats, weight float32)
}
