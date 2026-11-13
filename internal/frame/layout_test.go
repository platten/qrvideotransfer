package frame

import (
	"encoding/json"
	"image"
	"math"
	"os"
	"testing"
)

func readGolden(t *testing.T, name string, v any) {
	t.Helper()
	b, err := os.ReadFile("../../testdata/golden/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

func mustLayout(t testing.TB, gx, gy int) *Layout {
	t.Helper()
	l, err := NewLayout(gx, gy)
	if err != nil {
		t.Fatalf("NewLayout(%d, %d): %v", gx, gy, err)
	}
	return l
}

func reservedCount(l *Layout) int {
	n := 0
	for y := range l.GridY() {
		for x := range l.GridX() {
			if l.Reserved(x, y) {
				n++
			}
		}
	}
	return n
}

// FORMAT.md section 5.3: these counts are test vectors.
func TestPresetCounts(t *testing.T) {
	cases := []struct{ gx, gy, reserved, anchors, data int }{
		{212, 120, 1824, 24, 22848},
		{320, 180, 4140, 60, 51540},
		{480, 270, 8296, 131, 117112},
		{640, 360, 15540, 216, 207948},
		{960, 540, 33184, 504, 469088},
	}
	for _, c := range cases {
		l := mustLayout(t, c.gx, c.gy)
		if got := reservedCount(l); got != c.reserved {
			t.Errorf("%dx%d reserved = %d, want %d", c.gx, c.gy, got, c.reserved)
		}
		if got := len(l.Anchors()); got != c.anchors {
			t.Errorf("%dx%d anchors = %d, want %d", c.gx, c.gy, got, c.anchors)
		}
		if got := len(l.DataCells()); got != c.data {
			t.Errorf("%dx%d data cells = %d, want %d", c.gx, c.gy, got, c.data)
		}
	}
}

type layoutGolden struct {
	Layouts []struct {
		GridX, GridY int
		Reserved     int
		Anchors      int
		DataCells    int
		FirstAnchors [][2]int
		FirstCells   [][2]int
		LastCell     [2]int
	}
	Pixels480p struct {
		Pitch          int
		FinderSep      [4]int
		HeaderCopyTR   [4]int
		PaletteStripBR [4]int
	}
}

func TestLayoutGolden(t *testing.T) {
	var g layoutGolden
	readGolden(t, "layout.json", &g)
	for _, c := range g.Layouts {
		l := mustLayout(t, c.GridX, c.GridY)
		if got := reservedCount(l); got != c.Reserved {
			t.Errorf("%dx%d reserved = %d, want %d", c.GridX, c.GridY, got, c.Reserved)
		}
		a, cells := l.Anchors(), l.DataCells()
		if len(a) != c.Anchors || len(cells) != c.DataCells {
			t.Errorf("%dx%d anchors, cells = %d, %d, want %d, %d", c.GridX, c.GridY, len(a), len(cells), c.Anchors, c.DataCells)
			continue
		}
		for i, fa := range c.FirstAnchors {
			if a[i] != (Anchor{fa[0], fa[1]}) {
				t.Errorf("%dx%d anchor %d = %v, want %v", c.GridX, c.GridY, i, a[i], fa)
			}
		}
		for i, fc := range c.FirstCells {
			if cells[i] != (Cell{uint16(fc[0]), uint16(fc[1])}) {
				t.Errorf("%dx%d cell %d = %v, want %v", c.GridX, c.GridY, i, cells[i], fc)
			}
		}
		if last := cells[len(cells)-1]; last != (Cell{uint16(c.LastCell[0]), uint16(c.LastCell[1])}) {
			t.Errorf("%dx%d last cell = %v, want %v", c.GridX, c.GridY, last, c.LastCell)
		}
	}
	l := mustLayout(t, 212, 120)
	p := g.Pixels480p
	headerTR := union(l.Slot(TopRight, 0, 0), l.Slot(TopRight, 58, 3))
	stripBR := union(l.Patch(BottomRight, 0), l.Patch(BottomRight, 7))
	for _, c := range []struct {
		name string
		r    RectQ
		want [4]int
	}{
		{"finder and separator", l.Separator(TopLeft), p.FinderSep},
		{"top-right header copy", headerTR, p.HeaderCopyTR},
		{"bottom-right palette strip", stripBR, p.PaletteStripBR},
	} {
		want := image.Rect(c.want[0], c.want[1], c.want[2], c.want[3])
		if got := c.r.Pixels(p.Pitch); got != want {
			t.Errorf("%s at 480p = %v, want %v", c.name, got, want)
		}
	}
}

func union(a, b RectQ) RectQ {
	return RectQ{min(a.X0, b.X0), min(a.Y0, b.Y0), max(a.X1, b.X1), max(a.Y1, b.Y1)}
}

func cells(n int64) int64 { return n * Denom }

// FORMAT.md section 4: at 1080p, Uy = Ux = 2, c0 = 9, Dx = 462, Dy = 252.
func TestGeometry1080p(t *testing.T) {
	l := mustLayout(t, 480, 270)
	if got, want := l.Separator(TopLeft), (RectQ{cells(2), cells(2), cells(18), cells(18)}); got != want {
		t.Errorf("separator = %v, want %v", got, want)
	}
	// Finder module (3, 3) is centred on c0 = 9.
	if got, want := l.FinderModule(TopLeft, 3, 3), (RectQ{cells(8), cells(8), cells(10), cells(10)}); got != want {
		t.Errorf("finder centre module = %v, want %v", got, want)
	}
	// The bottom-right finder centre is at (c0 + Dx, c0 + Dy) = (471, 261).
	if got, want := l.FinderModule(BottomRight, 3, 3), (RectQ{cells(470), cells(260), cells(472), cells(262)}); got != want {
		t.Errorf("bottom-right centre module = %v, want %v", got, want)
	}
	// Slot (0, 0): x in [c0 + 11/2 Ux, c0 + 13/2 Ux) = [20, 22), y in [c0 - 7/2 Uy, c0 - 5/2 Uy) = [2, 4).
	if got, want := l.Slot(TopLeft, 0, 0), (RectQ{cells(20), cells(2), cells(22), cells(4)}); got != want {
		t.Errorf("slot (0, 0) = %v, want %v", got, want)
	}
	if got, want := l.Patch(TopLeft, 7), (RectQ{cells(9 + 129 + 28), cells(2), cells(9 + 133 + 28), cells(6)}); got != want {
		t.Errorf("patch 7 = %v, want %v", got, want)
	}
	if got, want := l.Border(), [4]RectQ{
		{0, 0, cells(480), cells(2)}, {0, cells(268), cells(480), cells(270)},
		{0, 0, cells(2), cells(270)}, {cells(478), 0, cells(480), cells(270)},
	}; got != want {
		t.Errorf("border = %v, want %v", got, want)
	}
}

// FORMAT.md section 4.5: at 1080p and 4K every structure edge falls on a cell boundary.
func TestCellAlignedPresets(t *testing.T) {
	for _, g := range [][2]int{{480, 270}, {960, 540}} {
		l := mustLayout(t, g[0], g[1])
		for _, r := range allStructure(l) {
			if r.X0%Denom != 0 || r.Y0%Denom != 0 || r.X1%Denom != 0 || r.Y1%Denom != 0 {
				t.Fatalf("%v: rectangle %v is not cell-aligned", g, r)
			}
		}
	}
}

func allStructure(l *Layout) []RectQ {
	b := l.Border()
	rs := b[:]
	for c := range Corner(4) {
		rs = append(rs, l.Separator(c))
		for m := range 49 {
			rs = append(rs, l.FinderModule(c, m%7, m/7))
		}
		for s := range HeaderSlots {
			rs = append(rs, l.Slot(c, s/4, s%4))
		}
		for k := range 8 {
			rs = append(rs, l.Patch(c, k))
		}
	}
	return rs
}

func mirror(l *Layout, c Corner, r RectQ) RectQ {
	gx, gy := cells(int64(l.GridX())), cells(int64(l.GridY()))
	if c == TopRight || c == BottomRight {
		r.X0, r.X1 = gx-r.X1, gx-r.X0
	}
	if c == BottomLeft || c == BottomRight {
		r.Y0, r.Y1 = gy-r.Y1, gy-r.Y0
	}
	return r
}

func TestMirroring(t *testing.T) {
	for _, g := range [][2]int{{212, 120}, {333, 250}, {120, 212}} {
		l := mustLayout(t, g[0], g[1])
		for c := range Corner(4) {
			if got, want := l.Separator(c), mirror(l, c, l.Separator(TopLeft)); got != want {
				t.Errorf("%v separator %d = %v, want %v", g, c, got, want)
			}
			for s := range HeaderSlots {
				i, r := s/4, s%4
				if got, want := l.Slot(c, i, r), mirror(l, c, l.Slot(TopLeft, i, r)); got != want {
					t.Fatalf("%v slot %d (%d, %d) = %v, want %v", g, c, i, r, got, want)
				}
			}
			for k := range 8 {
				if got, want := l.Patch(c, k), mirror(l, c, l.Patch(TopLeft, k)); got != want {
					t.Errorf("%v patch %d %d = %v, want %v", g, c, k, got, want)
				}
			}
			for m := range 49 {
				if got, want := l.FinderModule(c, m%7, m/7), mirror(l, c, l.FinderModule(TopLeft, m%7, m/7)); got != want {
					t.Errorf("%v module %d %d = %v, want %v", g, c, m, got, want)
				}
			}
		}
	}
}

// The fractions of HeaderSlot and PalettePatch agree with the grid geometry for every grid.
func TestFractions(t *testing.T) {
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-12 }
	if p := HeaderSlot(0, 0, 0); !near(p.X, 6.0/231) || !near(p.Y, -3.0/126) {
		t.Errorf("HeaderSlot(0, 0, 0) = %v", p)
	}
	if p := HeaderSlot(3, 58, 3); !near(p.X, 1-64.0/231) || !near(p.Y, 1) {
		t.Errorf("HeaderSlot(3, 58, 3) = %v", p)
	}
	for _, g := range [][2]int{{212, 120}, {480, 270}, {333, 250}, {120, 212}, {2047, 30}} {
		l := mustLayout(t, g[0], g[1])
		sep := l.Separator(TopLeft)
		c0 := float64(sep.X0+sep.X1) / 2 / Denom // finder centre
		dx := float64(cells(int64(l.GridX())))/Denom - 2*c0
		dy := float64(cells(int64(l.GridY())))/Denom - 2*c0
		frac := func(x, y int64) PointF { return PointF{(float64(x)/Denom - c0) / dx, (float64(y)/Denom - c0) / dy} }
		for c := range Corner(4) {
			for s := range HeaderSlots {
				r := l.Slot(c, s/4, s%4)
				want := frac((r.X0+r.X1)/2, (r.Y0+r.Y1)/2)
				if got := HeaderSlot(int(c), s/4, s%4); !near(got.X, want.X) || !near(got.Y, want.Y) {
					t.Fatalf("%v HeaderSlot(%d, %d, %d) = %v, want %v", g, c, s/4, s%4, got, want)
				}
			}
			for k := range 8 {
				r := l.Patch(c, k)
				a, b := frac(r.X0, r.Y0), frac(r.X1, r.Y1)
				got := PalettePatch(int(c), k)
				if !near(got.X0, a.X) || !near(got.Y0, a.Y) || !near(got.X1, b.X) || !near(got.Y1, b.Y) {
					t.Fatalf("%v PalettePatch(%d, %d) = %v, want %v %v", g, c, k, got, a, b)
				}
			}
		}
	}
}

func TestFinderPattern(t *testing.T) {
	// A line through the centre crosses dark, light, dark, light, dark in the ratio 1:1:3:1:1.
	want := []bool{true, false, true, true, true, false, true}
	for c := range Corner(4) {
		for m := range 7 {
			row, col := FinderModuleDark(c, m, 3), FinderModuleDark(c, 3, m)
			w := want[m] != (c == BottomRight)
			if row != w || col != w {
				t.Errorf("corner %d module %d: dark = %v, %v, want %v", c, m, row, col, w)
			}
		}
		if FinderModuleDark(c, 0, 6) != (c != BottomRight) || FinderModuleDark(c, 1, 5) != (c == BottomRight) {
			t.Errorf("corner %d: wrong corner modules", c)
		}
		if SeparatorDark(c) != (c == BottomRight) {
			t.Errorf("corner %d: separator dark = %v", c, SeparatorDark(c))
		}
	}
}

func TestAnchorPattern(t *testing.T) {
	for f := range uint32(8) {
		for dy := range AnchorH {
			for dx := range AnchorW {
				var want bool
				if dx < 4 {
					want = (dx < 2) != (dy < 2) // saddle: top-right and bottom-left light
				} else {
					q := [2][2]int{{0, 1}, {3, 2}}[dy/2][(dx-4)/2]
					want = q == int(f%4)
				}
				if got := AnchorCellLight(f, dx, dy); got != want {
					t.Errorf("frame %d cell (%d, %d): light = %v, want %v", f, dx, dy, got, want)
				}
			}
		}
	}
}

func TestAnchorsAndCells(t *testing.T) {
	l := mustLayout(t, 333, 250)
	in := make(map[Cell]bool)
	for _, a := range l.Anchors() {
		if (a.X-12)%32 != 0 || (a.Y-13)%30 != 0 || a.X+AnchorW > l.GridX() || a.Y+AnchorH > l.GridY() {
			t.Errorf("anchor %v is not a candidate", a)
		}
		for dy := range AnchorH {
			for dx := range AnchorW {
				if l.Reserved(a.X+dx, a.Y+dy) {
					t.Errorf("anchor %v covers a reserved cell", a)
				}
				in[Cell{uint16(a.X + dx), uint16(a.Y + dy)}] = true
			}
		}
	}
	prev := -1
	for _, c := range l.DataCells() {
		k := int(c.Y)*l.GridX() + int(c.X)
		if k <= prev || l.Reserved(int(c.X), int(c.Y)) || in[c] {
			t.Fatalf("data cell %v out of order, reserved or in an anchor", c)
		}
		prev = k
	}
	if got := len(l.DataCells()) + reservedCount(l) + len(in); got != l.GridX()*l.GridY() {
		t.Errorf("cells do not add up: %d", got)
	}
	for _, p := range [][2]int{{0, 0}, {332, 249}, {-1, 5}, {5, 250}, {333, 0}} {
		if !l.Reserved(p[0], p[1]) {
			t.Errorf("cell %v is not reserved", p)
		}
	}
}

func TestNewLayoutLimits(t *testing.T) {
	for _, g := range [][2]int{{212, 29}, {212, 2048}, {2048, 120}, {0, 120}, {8, 120}, {-1, 120}} {
		if _, err := NewLayout(g[0], g[1]); err == nil {
			t.Errorf("NewLayout(%d, %d) succeeded", g[0], g[1])
		}
	}
	for _, g := range [][2]int{{9, 120}, {2047, 2047}, {53, 30}} {
		if _, err := NewLayout(g[0], g[1]); err != nil {
			t.Errorf("NewLayout(%d, %d): %v", g[0], g[1], err)
		}
	}
}

func TestPixelsRounding(t *testing.T) {
	// At pitch 3 a sixth of a cell is half a pixel: 0.5 -> 1 (half up), just under 0.5 -> 0,
	// 1.5 -> 2, 15 -> 15.
	r := RectQ{X0: Denom / 6, Y0: Denom/6 - 1, X1: Denom / 2, Y1: 5 * Denom}
	if got, want := r.Pixels(3), image.Rect(1, 0, 2, 15); got != want {
		t.Errorf("Pixels = %v, want %v", got, want)
	}
}

func BenchmarkNewLayout1080p(b *testing.B) {
	for b.Loop() {
		if _, err := NewLayout(480, 270); err != nil {
			b.Fatal(err)
		}
	}
}
