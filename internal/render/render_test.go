package render

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/modem"
	"github.com/platten/qrvideotransfer/internal/prng"
	"github.com/platten/qrvideotransfer/internal/video"
)

type golden struct {
	Width, Height, Pitch int
	Session              uint16
	TotalFrames          uint32
	InnerCode            uint8
	SymbolSeed           uint64
	Frames               []struct {
		Modulation uint8
		FrameIndex uint32
		Y, Cb, Cr  string
	}
}

func newFrame(w, h int) *video.Frame {
	return &video.Frame{Y: make([]uint16, w*h), Cb: make([]uint16, w*h/4), Cr: make([]uint16, w*h/4)}
}

func mustNew(t testing.TB, gx, gy, p int, mod uint8) *Renderer {
	t.Helper()
	r, err := New(gx, gy, p, mod)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func header(r *Renderer, mod uint8, fi uint32) frame.Header {
	return frame.Header{
		Version: frame.Version, Session: 0x1234, FrameIndex: fi, TotalFrames: 8,
		GridX: uint16(r.Layout().GridX()), GridY: uint16(r.Layout().GridY()), Modulation: mod, InnerCode: 2,
	}
}

// testSymbols returns the top b bits of SplitMix64 outputs 0, 1, ... from state seed.
func testSymbols(n, b int, seed uint64) []uint8 {
	g := prng.SplitMix64{State: seed}
	s := make([]uint8, n)
	for i := range s {
		s[i] = uint8(g.Next() >> (64 - b))
	}
	return s
}

func planeHash(p []uint16, bits uint8) string {
	h := sha256.New()
	buf := make([]byte, 0, 2*len(p))
	for _, v := range p {
		if bits == 10 {
			buf = binary.LittleEndian.AppendUint16(buf, v)
		} else {
			buf = append(buf, byte(v))
		}
	}
	h.Write(buf)
	return hex.EncodeToString(h.Sum(nil))
}

// TestGolden renders a fixed array of symbols in each modulation at 480p and compares the plane
// hashes with testdata/golden/render.json, which an independent script wrote from FORMAT.md.
func TestGolden(t *testing.T) {
	b, err := os.ReadFile("../../testdata/golden/render.json")
	if err != nil {
		t.Fatal(err)
	}
	var g golden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	if len(g.Frames) != 8 {
		t.Fatalf("%d golden frames", len(g.Frames))
	}
	f := newFrame(g.Width, g.Height)
	for _, gf := range g.Frames {
		r := mustNew(t, g.Width/g.Pitch, g.Height/g.Pitch, g.Pitch, gf.Modulation)
		h := frame.Header{
			Version: frame.Version, Session: g.Session, FrameIndex: gf.FrameIndex, TotalFrames: g.TotalFrames,
			GridX: uint16(g.Width / g.Pitch), GridY: uint16(g.Height / g.Pitch), Modulation: gf.Modulation, InnerCode: g.InnerCode,
		}
		c, _ := modem.ByID(gf.Modulation)
		syms := testSymbols(len(r.Layout().DataCells()), c.BitsPerCell(), g.SymbolSeed)
		if err := r.Render(f, h, syms); err != nil {
			t.Fatal(err)
		}
		if f.W != g.Width || f.H != g.Height || f.Bits != r.Bits() {
			t.Fatalf("frame %d x %d, %d bits", f.W, f.H, f.Bits)
		}
		for i, p := range [][]uint16{f.Y, f.Cb, f.Cr} {
			want := []string{gf.Y, gf.Cb, gf.Cr}[i]
			if got := planeHash(p, f.Bits); got != want {
				t.Errorf("modulation %d frame %d plane %d: %s, want %s", gf.Modulation, gf.FrameIndex, i, got, want)
			}
		}
	}
}

// ref is a slow reference painter written from FORMAT.md sections 4 and 5.2, with exact rational
// structure rectangles. It records for every pixel the colour painted there.
type ref struct {
	w, h, p int
	gx, gy  int
	px      []modem.Sample
}

func rat(a, b int64) *big.Rat { return big.NewRat(a, b) }

func add(a, b *big.Rat) *big.Rat { return new(big.Rat).Add(a, b) }
func mul(a, b *big.Rat) *big.Rat { return new(big.Rat).Mul(a, b) }
func sub(a, b *big.Rat) *big.Rat { return new(big.Rat).Sub(a, b) }

// round returns v*P rounded to the nearest integer, halves up.
func (rf *ref) round(v *big.Rat) int {
	x := mul(v, rat(int64(rf.p), 1))
	num := new(big.Int).Mul(x.Num(), big.NewInt(2))
	num.Add(num, x.Denom())
	return int(num.Div(num, new(big.Int).Mul(x.Denom(), big.NewInt(2))).Int64()) // Div is floor for positive divisors
}

func (rf *ref) fillPx(x0, y0, x1, y1 int, c modem.Sample) {
	for y := max(y0, 0); y < min(y1, rf.h); y++ {
		for x := max(x0, 0); x < min(x1, rf.w); x++ {
			rf.px[y*rf.w+x] = c
		}
	}
}

// fillQ paints the top-left rectangle [x0, x1) x [y0, y1), in cells, mirrored to corner c.
func (rf *ref) fillQ(corner int, x0, y0, x1, y1 *big.Rat, c modem.Sample) {
	gx, gy := rat(int64(rf.gx), 1), rat(int64(rf.gy), 1)
	if corner == 1 || corner == 3 {
		x0, x1 = sub(gx, x1), sub(gx, x0)
	}
	if corner == 2 || corner == 3 {
		y0, y1 = sub(gy, y1), sub(gy, y0)
	}
	rf.fillPx(rf.round(x0), rf.round(y0), rf.round(x1), rf.round(y1), c)
}

func paintRef(w, h, p int, mod uint8, hd frame.Header, syms []uint8) *ref {
	c, _ := modem.ByID(mod)
	rf := &ref{w: w, h: h, p: p, gx: w / p, gy: h / p, px: make([]modem.Sample, w*h)}
	grey := modem.Sample{Y: 126, Cb: 128, Cr: 128, Bits: 8}
	if mod == 4 {
		grey = modem.Sample{Y: 504, Cb: 512, Cr: 512, Bits: 10}
	}
	dark, light := c.Level(0), c.Level(c.Levels()-1)
	pick := func(l bool) modem.Sample {
		if l {
			return light
		}
		return dark
	}
	rf.fillPx(0, 0, w, h, grey)
	l, _ := frame.NewLayout(rf.gx, rf.gy)
	for e, cell := range l.DataCells() {
		x, y := int(cell.X)*p, int(cell.Y)*p
		rf.fillPx(x, y, x+p, y+p, c.Map(syms[e]))
	}
	for _, a := range l.Anchors() {
		for dy := range 4 {
			for dx := range 8 {
				qx, qy := dx%4/2, dy/2
				var lit bool
				if dx < 4 {
					lit = qx != qy // top-right and bottom-left light
				} else {
					q := [2][2]uint32{{0, 3}, {1, 2}}[qx][qy]
					lit = q == hd.FrameIndex%4
				}
				x, y := (a.X+dx)*p, (a.Y+dy)*p
				rf.fillPx(x, y, x+p, y+p, pick(lit))
			}
		}
	}
	uy := rat(int64(rf.gy), 135)
	c0 := mul(rat(9, 2), uy)
	ux := mul(sub(rat(int64(rf.gx), 1), mul(rat(9, 1), uy)), rat(1, 231))
	zero, gxr, gyr := rat(0, 1), rat(int64(rf.gx), 1), rat(int64(rf.gy), 1)
	rf.fillQ(0, zero, zero, gxr, uy, grey)
	rf.fillQ(0, zero, sub(gyr, uy), gxr, gyr, grey)
	rf.fillQ(0, zero, zero, uy, gyr, grey)
	rf.fillQ(0, sub(gxr, uy), zero, gxr, gyr, grey)
	slots := hd.Encode()
	for corner := range 4 {
		inv := corner == 3
		rf.fillQ(corner, uy, uy, mul(rat(9, 1), uy), mul(rat(9, 1), uy), pick(!inv))
		for my := range 7 {
			for mx := range 7 {
				ring := max(mx-3, 3-mx, my-3, 3-my)
				d := ring != 2
				x, y := mul(rat(int64(mx+1), 1), uy), mul(rat(int64(my+1), 1), uy)
				rf.fillQ(corner, x, y, add(x, uy), add(y, uy), pick(d == inv))
			}
		}
		for i := range 59 {
			for r := range 4 {
				x := add(c0, mul(rat(int64(11+2*i), 2), ux))
				y := add(c0, mul(rat(int64(2*r-7), 2), uy))
				rf.fillQ(corner, x, y, add(x, ux), add(y, uy), pick(slots[4*i+r]))
			}
		}
		for k := range 8 {
			x := add(c0, mul(rat(int64(129+4*k), 2), ux))
			rf.fillQ(corner, x, sub(c0, mul(rat(7, 2), uy)), add(x, mul(rat(2, 1), ux)), sub(c0, mul(rat(3, 2), uy)),
				c.Level(k%c.Levels()))
		}
	}
	return rf
}

// TestStructure compares whole frames with the reference painter for a 16:9 and a 16:10 grid
// (3200 x 2000 at P = 4, where Ux != Uy), in every modulation and for frame indices 0..3. Chroma is
// checked against the colour of the top-left luma pixel of each chroma sample.
func TestStructure(t *testing.T) {
	type geo struct{ w, h, p int }
	geos := []geo{{848, 480, 4}, {3200, 2000, 4}, {1280, 720, 8}, {1920, 1080, 4}}
	if testing.Short() {
		geos = geos[:1]
	}
	for _, g := range geos {
		for mod := uint8(1); mod <= 4; mod++ {
			r := mustNew(t, g.w/g.p, g.h/g.p, g.p, mod)
			c, _ := modem.ByID(mod)
			syms := testSymbols(len(r.Layout().DataCells()), c.BitsPerCell(), uint64(mod))
			f := newFrame(g.w, g.h)
			for fi := range uint32(4) {
				if fi > 0 && (mod != 2 || g.w != 3200) && g.w != 848 {
					continue // all four phases on the small and the 16:10 grids
				}
				hd := header(r, mod, fi)
				if err := r.Render(f, hd, syms); err != nil {
					t.Fatal(err)
				}
				rf := paintRef(g.w, g.h, g.p, mod, hd, syms)
				compare(t, f, rf, mod, fi)
			}
		}
	}
}

func compare(t *testing.T, f *video.Frame, rf *ref, mod uint8, fi uint32) {
	t.Helper()
	bad := 0
	for y := range rf.h {
		for x := range rf.w {
			want := rf.px[y*rf.w+x]
			if f.Y[y*rf.w+x] != want.Y && bad < 5 {
				t.Errorf("%dx%d mod %d frame %d: Y(%d, %d) = %d, want %d", rf.w, rf.h, mod, fi, x, y, f.Y[y*rf.w+x], want.Y)
				bad++
			}
			if x%2 == 0 && y%2 == 0 {
				i := y/2*(rf.w/2) + x/2
				if (f.Cb[i] != want.Cb || f.Cr[i] != want.Cr) && bad < 5 {
					t.Errorf("%dx%d mod %d frame %d: chroma(%d, %d) = %d %d, want %d %d", rf.w, rf.h, mod, fi, x/2, y/2, f.Cb[i], f.Cr[i], want.Cb, want.Cr)
					bad++
				}
			}
		}
	}
}

// centre returns the pixel at the centre of a rasterised rectangle.
func centre(r frame.RectQ, p int) (int, int) {
	px := r.Pixels(p)
	return (px.Min.X + px.Max.X) / 2, (px.Min.Y + px.Max.Y) / 2
}

// TestPhaseAndPalette checks by hand the phase marker of every anchor for frame indices 0..3, the
// saddle, the palette patches (level k mod L) and the border colour.
func TestPhaseAndPalette(t *testing.T) {
	for mod := uint8(1); mod <= 4; mod++ {
		r := mustNew(t, 212, 120, 4, mod)
		c, _ := modem.ByID(mod)
		l := r.Layout()
		f := newFrame(848, 480)
		syms := make([]uint8, len(l.DataCells()))
		dark, light := c.Level(0).Y, c.Level(c.Levels()-1).Y
		for fi := range uint32(4) {
			if err := r.Render(f, header(r, mod, fi), syms); err != nil {
				t.Fatal(err)
			}
			at := func(cx, cy int) uint16 { return f.Y[(cy*4+2)*848+cx*4+2] }
			for _, a := range l.Anchors() {
				// Quadrants 0 top-left, 1 top-right, 2 bottom-right, 3 bottom-left.
				for q, off := range [4][2]int{{4, 0}, {6, 0}, {6, 2}, {4, 2}} {
					want := dark
					if uint32(q) == fi {
						want = light
					}
					for d := range 4 {
						if got := at(a.X+off[0]+d%2, a.Y+off[1]+d/2); got != want {
							t.Errorf("mod %d frame %d anchor %v quadrant %d: %d, want %d", mod, fi, a, q, got, want)
						}
					}
				}
				if at(a.X, a.Y) != dark || at(a.X+3, a.Y) != light || at(a.X, a.Y+3) != light || at(a.X+3, a.Y+3) != dark {
					t.Errorf("mod %d anchor %v: saddle wrong", mod, a)
				}
			}
		}
		for corner := range frame.Corner(4) {
			for k := range 8 {
				x, y := centre(l.Patch(corner, k), 4)
				if got, want := f.Y[y*848+x], c.Level(k%c.Levels()).Y; got != want {
					t.Errorf("mod %d corner %d patch %d: %d, want %d", mod, corner, k, got, want)
				}
			}
		}
		grey := uint16(126) << (r.Bits() - 8)
		if f.Y[0] != grey || f.Y[848*480-1] != grey || f.Cb[0] != 128<<(r.Bits()-8) {
			t.Errorf("mod %d: border %d %d %d", mod, f.Y[0], f.Y[848*480-1], f.Cb[0])
		}
	}
}

func TestNewErrors(t *testing.T) {
	for _, tc := range []struct {
		gx, gy, p int
		mod       uint8
		want      string
	}{
		{212, 120, 4, 0, "modulation 0"},
		{212, 120, 4, 5, "modulation 5"},
		{212, 120, 6, 2, "pitch 6"},
		{212, 120, 0, 2, "pitch 0"},
		{2048, 120, 4, 2, "grid"},
		{212, 119, 4, 2, "height"},
		{160, 30, 16, 2, ""},
	} {
		_, err := New(tc.gx, tc.gy, tc.p, tc.mod)
		if tc.want == "" {
			if err != nil {
				t.Errorf("New(%d, %d, %d, %d): %v", tc.gx, tc.gy, tc.p, tc.mod, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("New(%d, %d, %d, %d) error %v, want %q", tc.gx, tc.gy, tc.p, tc.mod, err, tc.want)
		}
	}
}

func TestRenderErrors(t *testing.T) {
	r := mustNew(t, 212, 120, 4, 2)
	if w, h := r.Size(); w != 848 || h != 480 || r.Bits() != 8 {
		t.Fatalf("size %d x %d, %d bits", w, h, r.Bits())
	}
	n := len(r.Layout().DataCells())
	good := header(r, 2, 1)
	f := newFrame(848, 480)
	syms := make([]uint8, n)
	if err := r.Render(f, good, syms); err != nil {
		t.Fatal(err)
	}
	bad := func(name string, f *video.Frame, h frame.Header, s []uint8) {
		t.Helper()
		if err := r.Render(f, h, s); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	h := good
	h.Modulation = 3
	bad("modulation", f, h, syms)
	h = good
	h.GridX = 213
	bad("grid", f, h, syms)
	h = good
	h.Reserved = 1
	bad("invalid header", f, h, syms)
	bad("short symbols", f, good, syms[:n-1])
	bad("short luma", &video.Frame{Y: f.Y[:100], Cb: f.Cb, Cr: f.Cr}, good, syms)
	bad("short chroma", &video.Frame{Y: f.Y, Cb: f.Cb, Cr: f.Cr[:10]}, good, syms)
	bad("no chroma", &video.Frame{Y: f.Y}, good, syms)
	g := mustNew(t, 212, 120, 4, 4)
	if g.Bits() != 10 {
		t.Errorf("Gray8 bits %d", g.Bits())
	}
}

func TestRenderAllocations(t *testing.T) {
	r := mustNew(t, 212, 120, 4, 3)
	f := newFrame(848, 480)
	syms := testSymbols(len(r.Layout().DataCells()), 3, 1)
	h := header(r, 3, 2)
	// Header.Encode allocates two small slices; nothing frame-sized is allocated.
	if a := testing.AllocsPerRun(5, func() { _ = r.Render(f, h, syms) }); a > 4 {
		t.Errorf("%v allocations per frame", a)
	}
}

func BenchmarkRender4K(b *testing.B) {
	for _, mod := range []uint8{2, 3} {
		r := mustNew(b, 960, 540, 4, mod)
		f := newFrame(3840, 2160)
		syms := testSymbols(len(r.Layout().DataCells()), 3, 1)
		h := header(r, mod, 0)
		b.Run(string(rune('0'+mod)), func(b *testing.B) {
			for range b.N {
				_ = r.Render(f, h, syms)
			}
		})
	}
}
