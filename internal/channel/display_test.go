package channel

import (
	"math"
	"testing"

	"github.com/platten/qrvideotransfer/internal/video"
)

// Gamma and black level on a ramp: each brightness code is driven to the nearest 8-bit panel level
// and emitted as black + (1 - black) V^(2.4 + error).
func TestDisplayGammaRamp(t *testing.T) {
	f := grey(220, 2, func(x, _ int) uint16 { return uint16(16 + x) })
	p := DisplayParams{GammaError: 0.2, Black: 0.01}
	pn := display(f, newScreen(220, 2, p))
	if pn.w != 220 || pn.h != 2 {
		t.Fatalf("panel %dx%d", pn.w, pn.h)
	}
	for x := range 220 {
		q := math.Round(float64(x) / 219 * 255)
		want := 0.01 + 0.99*math.Pow(q/255, 2.6)
		for c, pl := range [][]float32{pn.r, pn.g, pn.b} {
			if got := float64(pl[220+x]); math.Abs(got-want) > 1e-6 {
				t.Fatalf("code %d plane %d = %v, want %v", 16+x, c, got, want)
			}
		}
	}
}

// BT.709 colour: the limited-range codes of pure red give full red only.
func TestDisplayColour(t *testing.T) {
	f := flat(2, 2, 63)
	f.Cb[0], f.Cr[0] = 102, 240
	pn := display(f, newScreen(2, 2, DisplayParams{}))
	if pn.r[0] < 0.98 || pn.g[0] > 0.001 || pn.b[0] > 0.001 {
		t.Errorf("red = %v %v %v", pn.r[0], pn.g[0], pn.b[0])
	}
	// Brightness only, and a 10-bit source.
	g := &video.Frame{W: 2, H: 2, Bits: 10, Y: []uint16{940, 940, 64, 64}}
	pn = display(g, newScreen(2, 2, DisplayParams{}))
	if pn.r[0] != 1 || pn.b[1] != 1 || pn.g[2] != 0 {
		t.Errorf("10-bit grey = %v %v", pn.r, pn.g)
	}
}

// Player scaling doubles a frame bilinearly and letterboxes a frame of another shape.
func TestDisplayScaling(t *testing.T) {
	f := grey(4, 2, func(x, _ int) uint16 { return []uint16{16, 16, 235, 235}[x] })
	pn := display(f, newScreen(4, 2, DisplayParams{PanelW: 8, PanelH: 4}))
	row := pn.r[8:16]
	if row[0] != 0 || row[1] != 0 || row[2] != 0 || row[6] != 1 || row[7] != 1 || !(row[3] > 0 && row[3] < row[4] && row[4] < 1) {
		t.Errorf("scaled row %v", row)
	}
	s := newScreen(4, 4, DisplayParams{PanelW: 8, PanelH: 4, Black: 0.05})
	if s.scale != 1 || s.ox != 2 || s.oy != 0 {
		t.Fatalf("letterbox %v %v %v", s.scale, s.ox, s.oy)
	}
	pn = display(flat(4, 4, 235), s)
	for x := range 8 {
		want := float32(1)
		if x < 2 || x >= 6 {
			want = 0.05
		}
		if pn.g[8+x] != want {
			t.Errorf("letterboxed column %d = %v, want %v", x, pn.g[8+x], want)
		}
	}
	if u, v := s.toPanel(1, 2); u != 3 || v != 2 {
		t.Errorf("toPanel = %v, %v", u, v)
	}
}

// Sub-pixel structure: each pixel becomes red, green and blue stripes with the same mean.
func TestDisplaySubPixel(t *testing.T) {
	f := flat(2, 1, 235)
	f.H = 1
	pn := display(f, newScreen(2, 1, DisplayParams{SubPixel: true}))
	want := [][]float32{{3, 0, 0, 3, 0, 0}, {0, 3, 0, 0, 3, 0}, {0, 0, 3, 0, 0, 3}}
	for c, pl := range [][]float32{pn.r, pn.g, pn.b} {
		for i := range want[c] {
			if pl[i] != want[c][i] {
				t.Fatalf("plane %d = %v, want %v", c, pl, want[c])
			}
		}
	}
}

func TestCheckFrame(t *testing.T) {
	good := flat(4, 4, 16)
	if err := checkFrame(good, 4, 4); err != nil {
		t.Fatal(err)
	}
	bad := []func(f *video.Frame) *video.Frame{
		func(*video.Frame) *video.Frame { return nil },
		func(f *video.Frame) *video.Frame { f.W = 5; return f },
		func(f *video.Frame) *video.Frame { f.Bits = 4; return f },
		func(f *video.Frame) *video.Frame { f.Y = f.Y[:3]; return f },
		func(f *video.Frame) *video.Frame { f.Cr = nil; return f },
		func(f *video.Frame) *video.Frame { f.Cb = f.Cb[:1]; return f },
	}
	for i, b := range bad {
		if err := checkFrame(b(flat(4, 4, 16)), 4, 4); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}
