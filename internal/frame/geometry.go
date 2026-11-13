// Package frame defines what a code frame contains, bit for bit: the structure geometry and grid
// contents of docs/FORMAT.md sections 4 and 5, the header of section 6, the modulation and
// inner-code tables of sections 7 and 8, the bits-to-cells mapping of section 9 and the stream
// descriptor of section 10.
//
// Layout arithmetic is exact. Structure coordinates are integers in units of 1/Denom cell, which
// represents every coordinate of section 4 exactly for every grid. No floating point is used for
// layout; PointF and RectF exist only for a decoder's homography.
package frame

import "image"

// Denom is the denominator of structure coordinates: a coordinate v means v/Denom cells.
// 62370 = 2 * 135 * 231 makes Uy, Ux, c0 and every offset of FORMAT.md section 4 integral.
const Denom = 62370

// RectQ is a half-open rectangle [X0, X1) x [Y0, Y1) in units of 1/Denom cell.
type RectQ struct {
	X0, Y0, X1, Y1 int64
}

// Pixels rasterises r for a cell pitch of pitch pixels: each edge is multiplied by the pitch and
// rounded to the nearest integer, halves up (FORMAT.md section 4.5).
func (r RectQ) Pixels(pitch int) image.Rectangle {
	// Structure coordinates are never negative, so integer division is the floor.
	px := func(v int64) int { return int((2*v*int64(pitch) + Denom) / (2 * Denom)) }
	return image.Rect(px(r.X0), px(r.Y0), px(r.X1), px(r.Y1))
}

// Corner names one of the four corners of the frame. It also numbers the header copies and palette
// strips.
type Corner uint8

// Corners.
const (
	TopLeft Corner = iota
	TopRight
	BottomLeft
	BottomRight
)

// PointF is a point as fractions of the finder-centre rectangle: (0, 0) is the top-left finder's
// centre and (1, 1) the bottom-right finder's.
type PointF struct {
	X, Y float64
}

// RectF is a rectangle [X0, X1) x [Y0, Y1) as fractions of the finder-centre rectangle.
type RectF struct {
	X0, Y0, X1, Y1 float64
}

// HeaderSlot returns the centre of slot (i, r) of header copy copy (a Corner) as fractions of the
// finder-centre rectangle (FORMAT.md section 4.2). It does not depend on the grid.
func HeaderSlot(copy, i, r int) PointF {
	return mirrorF(Corner(copy), PointF{float64(6+i) / 231, float64(r-3) / 126}) //nolint:gosec // copy is 0..3
}

// PalettePatch returns patch k of palette strip copy (a Corner) as fractions of the finder-centre
// rectangle (FORMAT.md section 4.3).
func PalettePatch(copy, k int) RectF {
	c := Corner(copy) //nolint:gosec // copy is 0..3
	a := mirrorF(c, PointF{(129.0/2 + float64(2*k)) / 231, -7.0 / 252})
	b := mirrorF(c, PointF{(133.0/2 + float64(2*k)) / 231, -3.0 / 252})
	return RectF{min(a.X, b.X), min(a.Y, b.Y), max(a.X, b.X), max(a.Y, b.Y)}
}

// mirrorF maps a top-left fraction to corner c.
func mirrorF(c Corner, p PointF) PointF {
	if c == TopRight || c == BottomRight {
		p.X = 1 - p.X
	}
	if c == BottomLeft || c == BottomRight {
		p.Y = 1 - p.Y
	}
	return p
}

// FinderModuleDark reports whether module (mx, my), each in 0..6, of the finder at corner c is dark.
// The bottom-right finder is inverted.
func FinderModuleDark(c Corner, mx, my int) bool {
	ring := max(abs(mx-3), abs(my-3))
	return (ring != 2) != SeparatorDark(c)
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// SeparatorDark reports whether the separator around the finder at corner c is dark; only the
// bottom-right one is.
func SeparatorDark(c Corner) bool {
	return c == BottomRight
}
