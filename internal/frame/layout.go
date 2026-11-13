package frame

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

// NewLayout computes the layout of a gridX x gridY grid. It rejects grids outside the limits of
// FORMAT.md section 3.
func NewLayout(gridX, gridY int) (*Layout, error) {
	return &Layout{}, nil
}

// GridX returns the grid width in cells.
func (l *Layout) GridX() int { return l.gridX }

// GridY returns the grid height in cells.
func (l *Layout) GridY() int { return l.gridY }

// DataCells returns the data cells in data-cell order (row-major). The slice must not be modified.
func (l *Layout) DataCells() []Cell { return l.cells }

// Anchors returns the anchors in row-major order. The slice must not be modified.
func (l *Layout) Anchors() []Anchor { return l.anchors }

// Reserved reports whether cell (x, y) is reserved for structure.
func (l *Layout) Reserved(x, y int) bool {
	return false
}

// Border returns the four border bands: top, bottom, left, right.
func (l *Layout) Border() [4]RectQ {
	return [4]RectQ{}
}

// Separator returns the square [Uy, 9 Uy)^2 of corner c, mirrored: the finder with its separator.
// A renderer paints it in the separator colour and then paints the finder's modules over it.
func (l *Layout) Separator(c Corner) RectQ {
	return RectQ{}
}

// FinderModule returns module (mx, my), each in 0..6, of the finder at corner c.
func (l *Layout) FinderModule(c Corner, mx, my int) RectQ {
	return RectQ{}
}

// Slot returns slot (i, r) of header copy c, with i in 0..58 and r in 0..3.
func (l *Layout) Slot(c Corner, i, r int) RectQ {
	return RectQ{}
}

// Patch returns patch k, in 0..7, of palette strip c.
func (l *Layout) Patch(c Corner, k int) RectQ {
	return RectQ{}
}

// AnchorCellLight reports whether cell (dx, dy), relative to an anchor's top-left cell, is light in
// code frame frame (FORMAT.md section 5.2).
func AnchorCellLight(frame uint32, dx, dy int) bool {
	return false
}
