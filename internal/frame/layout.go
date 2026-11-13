package frame

import "fmt"

// Grid limits (FORMAT.md section 3). The smallest GridY follows from H >= 480 and the largest cell
// pitch, 16 pixels.
const (
	MaxGrid  = 2047
	MinGridY = 30
)

// AnchorW and AnchorH are an anchor's size in cells.
const (
	AnchorW = 8
	AnchorH = 4
)

// Cell is the grid coordinates of a cell.
type Cell struct {
	X, Y uint16
}

// Anchor is the grid coordinates of an anchor's top-left cell.
type Anchor struct {
	X, Y int
}

// Layout is the structure and grid contents of a GridX x GridY grid (FORMAT.md sections 4 and 5).
// It is immutable and safe for concurrent use.
type Layout struct {
	gridX, gridY int
	uy, ux, c0   int64 // structure units and finder-centre offset, in 1/Denom cell
	reserved     []bool
	anchors      []Anchor
	cells        []Cell
}

// CheckGrid reports whether a grid is within the limits of FORMAT.md section 3: GridX and GridY at
// most 2047, GridY at least 30, and 15 GridX > GridY so that the finder centres are distinct
// (Dx > 0).
func CheckGrid(gridX, gridY int) error {
	if gridX < 1 || gridX > MaxGrid || gridY < MinGridY || gridY > MaxGrid || 15*gridX <= gridY {
		return fmt.Errorf("frame: grid %d x %d outside the limits", gridX, gridY)
	}
	return nil
}

// NewLayout computes the layout of a gridX x gridY grid. It rejects grids outside the limits of
// FORMAT.md section 3 (see CheckGrid).
func NewLayout(gridX, gridY int) (*Layout, error) {
	if err := CheckGrid(gridX, gridY); err != nil {
		return nil, err
	}
	gx, gy := int64(gridX), int64(gridY)
	l := &Layout{
		gridX: gridX, gridY: gridY,
		uy:       gy * (Denom / 135),     // GridY / 135
		c0:       gy * (Denom / 270) * 9, // 9/2 Uy
		ux:       270*gx - 18*gy,         // (GridX - 9 Uy) / 231
		reserved: make([]bool, gridX*gridY),
	}
	for _, r := range l.structure() {
		l.reserve(r)
	}
	inAnchor := make([]bool, gridX*gridY)
	for y := 13; y+AnchorH <= gridY; y += 30 {
		for x := 12; x+AnchorW <= gridX; x += 32 {
			if l.anyReserved(x, y) {
				continue
			}
			l.anchors = append(l.anchors, Anchor{x, y})
			for dy := range AnchorH {
				for dx := range AnchorW {
					inAnchor[(y+dy)*gridX+x+dx] = true
				}
			}
		}
	}
	n := 0
	for i, r := range l.reserved {
		if !r && !inAnchor[i] {
			n++
		}
	}
	l.cells = make([]Cell, 0, n)
	for i, r := range l.reserved {
		if !r && !inAnchor[i] {
			l.cells = append(l.cells, Cell{uint16(i % gridX), uint16(i / gridX)}) //nolint:gosec // grid <= 2047
		}
	}
	return l, nil
}

// structure returns every reserved rectangle of section 5.1, unrounded.
func (l *Layout) structure() []RectQ {
	b := l.Border()
	rs := b[:]
	for c := range Corner(4) {
		rs = append(rs, l.Separator(c),
			l.mirror(c, RectQ{l.c0 + 11*l.ux/2, l.c0 - 7*l.uy/2, l.c0 + 129*l.ux/2, l.c0 + l.uy/2}),    // header copy
			l.mirror(c, RectQ{l.c0 + 129*l.ux/2, l.c0 - 7*l.uy/2, l.c0 + 161*l.ux/2, l.c0 - 3*l.uy/2}), // palette strip
		)
	}
	return rs
}

// reserve marks every cell that intersects r with positive area.
func (l *Layout) reserve(r RectQ) {
	x0, x1 := max(0, r.X0/Denom), min(int64(l.gridX), (r.X1+Denom-1)/Denom)
	y0, y1 := max(0, r.Y0/Denom), min(int64(l.gridY), (r.Y1+Denom-1)/Denom)
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			l.reserved[y*int64(l.gridX)+x] = true
		}
	}
}

func (l *Layout) anyReserved(x, y int) bool {
	for dy := range AnchorH {
		for dx := range AnchorW {
			if l.reserved[(y+dy)*l.gridX+x+dx] {
				return true
			}
		}
	}
	return false
}

// GridX returns the grid width in cells.
func (l *Layout) GridX() int { return l.gridX }

// GridY returns the grid height in cells.
func (l *Layout) GridY() int { return l.gridY }

// DataCells returns the data cells in data-cell order (row-major). The slice must not be modified.
func (l *Layout) DataCells() []Cell { return l.cells }

// Anchors returns the anchors in row-major order. The slice must not be modified.
func (l *Layout) Anchors() []Anchor { return l.anchors }

// Reserved reports whether cell (x, y) is reserved for structure. Cells outside the grid count as
// reserved.
func (l *Layout) Reserved(x, y int) bool {
	if x < 0 || y < 0 || x >= l.gridX || y >= l.gridY {
		return true
	}
	return l.reserved[y*l.gridX+x]
}

// mirror maps a top-left rectangle to corner c.
func (l *Layout) mirror(c Corner, r RectQ) RectQ {
	if c == TopRight || c == BottomRight {
		w := int64(l.gridX) * Denom
		r.X0, r.X1 = w-r.X1, w-r.X0
	}
	if c == BottomLeft || c == BottomRight {
		h := int64(l.gridY) * Denom
		r.Y0, r.Y1 = h-r.Y1, h-r.Y0
	}
	return r
}

// Border returns the four border bands: top, bottom, left, right.
func (l *Layout) Border() [4]RectQ {
	w, h := int64(l.gridX)*Denom, int64(l.gridY)*Denom
	return [4]RectQ{{0, 0, w, l.uy}, {0, h - l.uy, w, h}, {0, 0, l.uy, h}, {w - l.uy, 0, w, h}}
}

// Separator returns the square [Uy, 9 Uy)^2 of corner c, mirrored: the finder with its separator.
// A renderer paints it in the separator colour and then paints the finder's modules over it.
func (l *Layout) Separator(c Corner) RectQ {
	return l.mirror(c, RectQ{l.uy, l.uy, 9 * l.uy, 9 * l.uy})
}

// FinderModule returns module (mx, my), each in 0..6, of the finder at corner c.
func (l *Layout) FinderModule(c Corner, mx, my int) RectQ {
	x, y := int64(mx+1)*l.uy, int64(my+1)*l.uy
	return l.mirror(c, RectQ{x, y, x + l.uy, y + l.uy})
}

// Slot returns slot (i, r) of header copy c, with i in 0..58 and r in 0..3.
func (l *Layout) Slot(c Corner, i, r int) RectQ {
	x, y := l.c0+int64(11+2*i)*l.ux/2, l.c0+int64(2*r-7)*l.uy/2
	return l.mirror(c, RectQ{x, y, x + l.ux, y + l.uy})
}

// Patch returns patch k, in 0..7, of palette strip c.
func (l *Layout) Patch(c Corner, k int) RectQ {
	x := l.c0 + int64(129+4*k)*l.ux/2
	return l.mirror(c, RectQ{x, l.c0 - 7*l.uy/2, x + 2*l.ux, l.c0 - 3*l.uy/2})
}

// AnchorCellLight reports whether cell (dx, dy), relative to an anchor's top-left cell, is light in
// code frame frame (FORMAT.md section 5.2).
func AnchorCellLight(frame uint32, dx, dy int) bool {
	right, bottom := dx%4 >= 2, dy >= 2
	if dx < 4 { // saddle: top-right and bottom-left quadrants light
		return right != bottom
	}
	q := 0 // phase quadrants: 0 top-left, 1 top-right, 2 bottom-right, 3 bottom-left
	switch {
	case right && !bottom:
		q = 1
	case right && bottom:
		q = 2
	case bottom:
		q = 3
	}
	return uint32(q) == frame%4
}
