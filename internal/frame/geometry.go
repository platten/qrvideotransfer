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
	return image.Rectangle{}
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
	return PointF{}
}

// PalettePatch returns patch k of palette strip copy (a Corner) as fractions of the finder-centre
// rectangle (FORMAT.md section 4.3).
func PalettePatch(copy, k int) RectF {
	return RectF{}
}

// FinderModuleDark reports whether module (mx, my), each in 0..6, of the finder at corner c is dark.
// The bottom-right finder is inverted.
func FinderModuleDark(c Corner, mx, my int) bool {
	return false
}

// SeparatorDark reports whether the separator around the finder at corner c is dark; only the
// bottom-right one is.
func SeparatorDark(c Corner) bool {
	return false
}
