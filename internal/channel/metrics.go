package channel

import (
	"fmt"
	"math"
	"math/bits"
)

// CleanThreshold is the weight a cell's code frame must reach for the cell to count (DESIGN.md
// section 7, rule 3).
const CleanThreshold = 0.92

// RawBER returns the share of bits that differ between two equally long sequences of symbols of
// the given width in bits.
func RawBER(want, got []uint8, width int) (float64, error) {
	if len(want) != len(got) || width < 1 || width > 8 {
		return 0, fmt.Errorf("channel: cannot compare %d with %d symbols of %d bits", len(want), len(got), width)
	}
	if len(want) == 0 {
		return 0, nil
	}
	mask := uint8(0xff) >> (8 - width)
	n := 0
	for i := range want {
		n += bits.OnesCount8((want[i] ^ got[i]) & mask)
	}
	return float64(n) / float64(len(want)*width), nil
}

// CleanFraction returns the share of the capture's cells whose centre is inside the capture and
// whose largest code-frame weight is at least min.
func CleanFraction(t *Truth, min float64) float64 {
	w, h := float64(t.rc.cam.w), float64(t.rc.cam.h)
	clean := 0
	for gy := range t.GridY {
		for gx := range t.GridX {
			x, y := t.Cell(gx, gy)
			if !(x >= 0 && x < w && y >= 0 && y < h) {
				continue
			}
			for _, m := range t.CellMix(gx, gy) {
				if m.W >= min {
					clean++
					break
				}
			}
		}
	}
	return float64(clean) / float64(t.GridX*t.GridY)
}

// CentreError returns the RMS distance, in cells, between estimated and true cell centres in the
// capture, over the cells for which est reports a position, and how many there were. A cell's
// size is the square root of the area its neighbours' centres span locally.
func CentreError(t *Truth, est func(gx, gy int) (x, y float64, ok bool)) (rms float64, n int) {
	sum := 0.0
	for gy := range t.GridY {
		for gx := range t.GridX {
			ex, ey, ok := est(gx, gy)
			if !ok {
				continue
			}
			x, y := t.Cell(gx, gy)
			x1, y1 := t.Cell(gx+1, gy)
			x2, y2 := t.Cell(gx, gy+1)
			area := math.Abs(float64((x1-x)*(y2-y)) - float64((x2-x)*(y1-y)))
			dx, dy := ex-x, ey-y
			sum += float64((float64(dx*dx) + float64(dy*dy)) / area)
			n++
		}
	}
	if n == 0 {
		return 0, 0
	}
	return math.Sqrt(sum / float64(n)), n
}
