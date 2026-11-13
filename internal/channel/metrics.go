package channel

// CleanThreshold is the weight a cell's code frame must reach for the cell to count (DESIGN.md
// section 7, rule 3).
const CleanThreshold = 0.92

// RawBER returns the share of bits that differ between two equally long sequences of symbols of
// the given width in bits.
func RawBER(want, got []uint8, width int) (float64, error) {
	var z0 float64
	var z1 error
	return z0, z1
}

// CleanFraction returns the share of the capture's cells whose centre is inside the capture and
// whose largest code-frame weight is at least min.
func CleanFraction(t *Truth, min float64) float64 {
	var z0 float64
	return z0
}

// CentreError returns the RMS distance, in cells, between estimated and true cell centres in the
// capture, over the cells for which est reports a position, and how many there were. A cell's
// size is the square root of the area its neighbours' centres span locally.
func CentreError(t *Truth, est func(gx, gy int) (x, y float64, ok bool)) (rms float64, n int) {
	var z0 float64
	var z1 int
	return z0, z1
}
