package vision

import (
	"context"
	"math"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/channel"
	"github.com/platten/qrvideotransfer/internal/fec/ldpc"
	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/modem"
	"github.com/platten/qrvideotransfer/internal/prng"
	"github.com/platten/qrvideotransfer/internal/render"
	"github.com/platten/qrvideotransfer/internal/video"
)

// This file is the radiometry stream's test bench. Until the geometry stream's sampler lands, cells,
// anchors and palette patches are sampled at their true positions in the capture, taken from
// channel.Truth, by the minimal sampler below.

// longTests reports whether the long versions of the statistical tests run (QRVT_LONG_TESTS=1).
func longTests() bool { return os.Getenv("QRVT_LONG_TESTS") == "1" }

// radSession is a looping test video: its frames, and what each frame carries.
type radSession struct {
	hdr     frame.Header
	layout  *frame.Layout
	mod     *modem.Constellation
	pitch   int
	frames  []*video.Frame
	symbols [][]uint8 // per frame, one symbol per data cell
	shards  [][][]byte
	mapping *frame.Mapping
	code    *ldpc.Code
}

// newRadSession renders n frames (a multiple of 4) of a gridX x gridY grid at the cell pitch. With
// encode set, every frame carries LDPC codewords of random shards (inner code 2, "normal");
// otherwise its symbols are random.
func newRadSession(t testing.TB, gridX, gridY, pitch int, mod uint8, n int, seed uint64, encode bool) *radSession {
	t.Helper()
	r, err := render.New(gridX, gridY, pitch, mod)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := modem.ByID(mod)
	s := &radSession{
		hdr:    frame.Header{Version: 1, Session: 0x5a17, TotalFrames: uint32(n), GridX: uint16(gridX), GridY: uint16(gridY), Modulation: mod, InnerCode: 2}, //nolint:gosec // small test values
		layout: r.Layout(), mod: c, pitch: pitch,
	}
	g := prng.SplitMix64{State: seed}
	cells := len(s.layout.DataCells())
	if encode {
		if s.mapping, err = frame.NewMapping(s.hdr); err != nil {
			t.Fatal(err)
		}
		s.code, _ = ldpc.New(s.hdr.InnerCode)
	}
	w, h := r.Size()
	for i := range n {
		s.hdr.FrameIndex = uint32(i) //nolint:gosec // small
		sym := make([]uint8, cells)
		if encode {
			mp := render.NewMapper(s.mapping)
			cws := make([][]byte, s.mapping.CodewordsPerFrame())
			var shards [][]byte
			for k := range cws {
				sh := make([]byte, s.code.ShardSize())
				for j := range sh {
					sh[j] = byte(g.Next())
				}
				shards = append(shards, sh)
				cws[k] = make([]byte, (s.code.N()+7)/8)
				s.code.Encode(cws[k], sh)
			}
			if err := mp.Symbols(sym, uint32(i), cws); err != nil { //nolint:gosec // small
				t.Fatal(err)
			}
			s.shards = append(s.shards, shards)
		} else {
			for j := range sym {
				sym[j] = uint8(g.Next() & 7) //nolint:gosec // masked
			}
		}
		f := &video.Frame{Y: make([]uint16, w*h), Cb: make([]uint16, w*h/4), Cr: make([]uint16, w*h/4)}
		if err := r.Render(f, s.hdr, sym); err != nil {
			t.Fatal(err)
		}
		f.Index = i
		s.frames = append(s.frames, f)
		s.symbols = append(s.symbols, sym)
	}
	return s
}

// nodeStep is the spacing, in cells, of the corner nodes the bench maps exactly.
const nodeStep = 2

// radCapture is one capture with what the bench sampled from it.
type radCapture struct {
	f       *video.Frame
	tr      *channel.Truth
	obs     []modem.Obs // per data cell
	palette []modem.Obs // strip c, patch k at 8c + k
	anchors []AnchorObs
	nodes   []Point // capture position of every cell corner, (GridX+1) x (GridY+1), row-major
}

// shoot films the session at period per frame with camera c and samples every capture.
func (s *radSession) shoot(t testing.TB, c channel.Config, period time.Duration, captures int, seed uint64) []*radCapture {
	t.Helper()
	for i, f := range s.frames {
		f.PTS = time.Duration(i) * period
	}
	in := channel.Input{Frames: s.frames, Period: time.Duration(len(s.frames)) * period, Cell: s.pitch}
	var out []*radCapture
	err := channel.Simulate(context.Background(), in, c, channel.Run{Captures: captures, Seed: seed, Workers: runtime.NumCPU()},
		func(f *video.Frame, tr *channel.Truth) error {
			out = append(out, s.observe(f, tr))
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// observe samples a capture at the true positions.
func (s *radSession) observe(f *video.Frame, tr *channel.Truth) *radCapture {
	l := s.layout
	gx, gy := l.GridX(), l.GridY()
	rc := &radCapture{f: f, tr: tr, nodes: make([]Point, (gx+1)*(gy+1))}
	// Corner nodes are mapped exactly every nodeStep cells and interpolated between: the mapping
	// (perspective, lens, rolling shutter, shake) is smooth over that span.
	p := float64(s.pitch)
	exact := func(x, y int) Point {
		u, v := tr.Point(float64(x)*p, float64(y)*p)
		return Point{u, v}
	}
	for y0 := 0; y0 <= gy; y0 += nodeStep {
		y1 := min(y0+nodeStep, gy)
		for x0 := 0; x0 <= gx; x0 += nodeStep {
			x1 := min(x0+nodeStep, gx)
			a, b, c, d := exact(x0, y0), exact(x1, y0), exact(x0, y1), exact(x1, y1)
			for y := y0; y <= y1; y++ {
				fy := float64(y-y0) / float64(max(y1-y0, 1))
				for x := x0; x <= x1; x++ {
					fx := float64(x-x0) / float64(max(x1-x0, 1))
					rc.nodes[y*(gx+1)+x] = Point{
						(a.X*(1-fx)+b.X*fx)*(1-fy) + (c.X*(1-fx)+d.X*fx)*fy,
						(a.Y*(1-fx)+b.Y*fx)*(1-fy) + (c.Y*(1-fx)+d.Y*fx)*fy,
					}
				}
			}
		}
	}
	cells := l.DataCells()
	rc.obs = make([]modem.Obs, len(cells))
	for i, c := range cells {
		rc.obs[i] = rc.region(float64(c.X)+0.2, float64(c.Y)+0.2, float64(c.X)+0.8, float64(c.Y)+0.8)
	}
	uy := float64(gy) / 135
	c0, dx, dy := 4.5*uy, float64(gx)-9*uy, 126*uy
	for cp := range 4 {
		for k := range 8 {
			r := frame.PalettePatch(cp, k)
			x0, x1 := c0+r.X0*dx, c0+r.X1*dx
			y0, y1 := c0+r.Y0*dy, c0+r.Y1*dy
			mx, my := 0.2*(x1-x0), 0.2*(y1-y0)
			rc.palette = append(rc.palette, rc.region(x0+mx, y0+my, x1-mx, y1-my))
		}
	}
	for _, a := range l.Anchors() {
		ax, ay := float64(a.X), float64(a.Y)
		q := func(qx, qy float64) modem.Obs { return rc.region(ax+qx+0.4, ay+qy+0.4, ax+qx+1.6, ay+qy+1.6) }
		mean := func(a, b modem.Obs) modem.Obs {
			return modem.Obs{Y: (a.Y + b.Y) / 2, Cb: (a.Cb + b.Cb) / 2, Cr: (a.Cr + b.Cr) / 2, Saturated: a.Saturated || b.Saturated}
		}
		o := AnchorObs{Anchor: a, Found: true, Corner: rc.at(ax+2, ay+2)}
		o.Black, o.White = mean(q(0, 0), q(2, 2)), mean(q(2, 0), q(0, 2))
		for i, d := range [4][2]float64{{4, 0}, {6, 0}, {6, 2}, {4, 2}} {
			o.Phase[i] = q(d[0], d[1]).Y
		}
		rc.anchors = append(rc.anchors, o)
	}
	return rc
}

// at maps continuous cell coordinates (corner origin) to the capture by bilinear interpolation of
// the corner nodes.
func (rc *radCapture) at(x, y float64) Point {
	gx := rc.tr.GridX
	ix, iy := min(int(x), gx-1), min(int(y), rc.tr.GridY-1)
	fx, fy := x-float64(ix), y-float64(iy)
	n := func(i, j int) Point { return rc.nodes[j*(gx+1)+i] }
	a, b, c, d := n(ix, iy), n(ix+1, iy), n(ix, iy+1), n(ix+1, iy+1)
	return Point{
		(a.X*(1-fx)+b.X*fx)*(1-fy) + (c.X*(1-fx)+d.X*fx)*fy,
		(a.Y*(1-fx)+b.Y*fx)*(1-fy) + (c.Y*(1-fx)+d.Y*fx)*fy,
	}
}

// region returns the mean of a 4 x 4 grid of bilinear samples over the cell-coordinate rectangle,
// normalised to the 8-bit scale. It is saturated when a sample touches a clipped pixel and is NaN
// when a sample falls outside the capture.
func (rc *radCapture) region(x0, y0, x1, y1 float64) modem.Obs {
	f := rc.f
	top := uint16(1)<<f.Bits - 1
	scale := float32(int(1) << (f.Bits - 8))
	var o modem.Obs
	var sy, sb, sr float64
	for j := range 4 {
		for i := range 4 {
			pt := rc.at(x0+(x1-x0)*(float64(i)+0.5)/4, y0+(y1-y0)*(float64(j)+0.5)/4)
			v, sat, ok := bilinear(f.Y, f.W, f.H, pt.X, pt.Y, top)
			if !ok {
				nan := float32(math.NaN())
				return modem.Obs{Y: nan, Cb: nan, Cr: nan}
			}
			cb, _, _ := bilinear(f.Cb, f.W/2, f.H/2, pt.X/2, pt.Y/2, top)
			cr, _, _ := bilinear(f.Cr, f.W/2, f.H/2, pt.X/2, pt.Y/2, top)
			sy, sb, sr = sy+v, sb+cb, sr+cr
			o.Saturated = o.Saturated || sat
		}
	}
	o.Y, o.Cb, o.Cr = float32(sy/16)/scale, float32(sb/16)/scale, float32(sr/16)/scale
	return o
}

// bilinear interpolates plane p (pixel centres at half-integers) at (x, y). sat reports a
// neighbouring sample at 0 or top; ok is false outside the plane.
func bilinear(p []uint16, w, h int, x, y float64, top uint16) (v float64, sat, ok bool) {
	x, y = x-0.5, y-0.5
	if !(x >= 0 && y >= 0 && x <= float64(w-1) && y <= float64(h-1)) {
		return 0, false, false
	}
	ix, iy := min(int(x), w-2), min(int(y), h-2)
	fx, fy := x-float64(ix), y-float64(iy)
	a, b := p[iy*w+ix], p[iy*w+ix+1]
	c, d := p[(iy+1)*w+ix], p[(iy+1)*w+ix+1]
	for _, s := range [4]uint16{a, b, c, d} {
		sat = sat || s == 0 || s == top
	}
	v = (float64(a)*(1-fx)+float64(b)*fx)*(1-fy) + (float64(c)*(1-fx)+float64(d)*fx)*fy
	return v, sat, true
}

// trueMix returns the true code-frame weights of every data cell.
func (rc *radCapture) trueMix(l *frame.Layout) [][]channel.Weight {
	cells := l.DataCells()
	out := make([][]channel.Weight, len(cells))
	for i, c := range cells {
		out[i] = rc.tr.CellMix(int(c.X), int(c.Y))
	}
	return out
}

// trueAnchorWeights returns the true weight of each phase at an anchor's phase marker centre.
func (rc *radCapture) truePhase(x, y float64, pitch int) [4]float64 {
	var w [4]float64
	for _, m := range rc.tr.Mix(x*float64(pitch), y*float64(pitch)) {
		w[m.Frame%4] += m.W
	}
	return w
}

// preset returns a simulator preset at a smaller camera size.
func preset(t testing.TB, name string, w, h int) channel.Config {
	t.Helper()
	c, err := channel.Preset(name)
	if err != nil {
		t.Fatal(err)
	}
	return c.Resize(w, h)
}

// phaseOf estimates the capture's phase field with the gamma the palette gives, or the default.
func (s *radSession) phaseOf(rc *radCapture) PhaseField {
	g, _ := PaletteGamma(rc.palette, s.mod)
	return AnchorPhase{Gamma: g}.Estimate(rc.anchors, s.layout)
}

// credited returns, for data cell i credited to phase p, the true frame with that phase and its
// true weight; frame -1 when no frame with that phase was seen there.
func credited(mix []channel.Weight, p int) (f int, w float64) {
	f = -1
	for _, m := range mix {
		if m.Frame%4 == p && m.W > w {
			f, w = m.Frame, m.W
		}
	}
	return f, w
}

// symbolBit returns bit k (0 the most significant) of a b-bit symbol.
func symbolBit(sym uint8, b, k int) uint8 { return sym >> (b - 1 - k) & 1 }
