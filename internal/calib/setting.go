package calib

// The modulation ids of docs/FORMAT.md section 7 that calibration measures.
const (
	ModeBW    uint8 = 1 // black and white, `--mode bw`
	ModeGray4 uint8 = 2 // four greys, `--mode gray4`
	ModeColor uint8 = 3 // eight colours, `--mode color`
)

// The inner-code ids of docs/FORMAT.md section 8: the protection levels.
const (
	Fast   uint8 = 1 // `--protection fast`
	Normal uint8 = 2 // `--protection normal`
	Robust uint8 = 4 // `--protection robust`
)

// The ladder of DESIGN.md section 13: each list runs from the slowest, most robust value to the
// fastest. A setting's rank on each axis is its position in the list.
var (
	Rates       = []int{12, 30, 60}   // display frame rates
	Cells       = []int{16, 12, 8, 4} // cell sizes in video pixels
	Modes       = []uint8{ModeBW, ModeGray4, ModeColor}
	Protections = []uint8{Robust, Normal, Fast}
)

// Setting is one rung of the ladder: what `qrvt encode` is given besides the video size.
type Setting struct {
	Rate  int   // display frame rate, `--fps`
	Cell  int   // cell size in video pixels, `--cell`
	Mode  uint8 // modulation id, `--mode`
	Inner uint8 // inner-code id, `--protection`
}

// rank returns the position of v in list, or -1.
func rank[T comparable](list []T, v T) int {
	for i, x := range list {
		if x == v {
			return i
		}
	}
	return -1
}

// Ranks returns the setting's rank on each axis (rate, cell, mode, protection), and false when a
// value is not on the ladder.
func (s Setting) Ranks() ([4]int, bool) {
	r := [4]int{rank(Rates, s.Rate), rank(Cells, s.Cell), rank(Modes, s.Mode), rank(Protections, s.Inner)}
	for _, v := range r {
		if v < 0 {
			return r, false
		}
	}
	return r, true
}

// Distance is the exit test's distance of DESIGN.md section 13: the sum over rate, cell, mode and
// protection of the difference of the two settings' ranks. "Within one step" is a distance of at
// most 1. It is -1 when either setting is not on the ladder.
func Distance(a, b Setting) int {
	ra, oka := a.Ranks()
	rb, okb := b.Ranks()
	if !oka || !okb {
		return -1
	}
	d := 0
	for i := range ra {
		d += max(ra[i]-rb[i], rb[i]-ra[i])
	}
	return d
}
