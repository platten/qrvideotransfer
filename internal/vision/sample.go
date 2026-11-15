package vision

import (
	"math"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/modem"
	"github.com/platten/qrvideotransfer/internal/video"
)

// Sampling parameters (docs/DESIGN.md section 10, step 4).
const (
	sampleShare = 0.6 // the central share of a cell's side that is averaged
	// sampleMaxHalf bounds a footprint's half-extent in pixels. No real cell comes near it; it keeps
	// the work per cell bounded on hostile meshes.
	sampleMaxHalf = 128
)

// FootprintSampler is the Sampler of docs/DESIGN.md section 10, step 4. The zero value is ready to
// use and safe for concurrent use.
type FootprintSampler struct{}

var _ Sampler = FootprintSampler{}

// Sample writes the observation of cells[i] to dst[i], for as many cells as dst holds. A cell's
// observation is the mean, per plane, of the samples whose centres fall in the central 60% of the
// cell's footprint (the mesh's image of [gx+0.2, gx+0.8] x [gy+0.2, gy+0.8]), read directly from
// the capture's planes, on the 8-bit scale; colour uses the half-resolution planes the same way, and
// is zero for a brightness-only capture. When no sample centre falls inside, the sample under the
// cell's centre is used. A cell is saturated when any of its brightness samples is at the bottom or
// the top of the code range. A cell outside the capture, an invalid frame or mesh, or a footprint
// wider than 256 pixels gives a saturated observation with zero values: no information.
func (FootprintSampler) Sample(dst []modem.Obs, f *video.Frame, m *Mesh, cells []frame.Cell) {
	n := min(len(dst), len(cells))
	v, ok := geomView(f)
	for i := range n {
		if !ok {
			dst[i] = modem.Obs{Saturated: true}
			continue
		}
		x, y := float64(cells[i].X)+0.5, float64(cells[i].Y)+0.5
		const h = sampleShare / 2
		c := gpt(m.At(x, y))
		hx := gpt(m.At(x+h, y)).sub(gpt(m.At(x-h, y))).mul(0.5)
		hy := gpt(m.At(x, y+h)).sub(gpt(m.At(x, y-h))).mul(0.5)
		dst[i] = v.footprint(c, hx, hy)
	}
}

// footprint returns the observation of the parallelogram c + a hx + b hy, |a|, |b| <= 1, in image
// coordinates (see FootprintSampler.Sample).
func (v *geomLuma) footprint(c, hx, hy gpt) modem.Obs {
	unknown := modem.Obs{Saturated: true}
	if !geomFinite(c, hx, hy) {
		return unknown
	}
	ys, ny, sat := geomMean(v.y, v.w, v.h, c, hx, hy, v.top)
	if ny == 0 {
		return unknown
	}
	o := modem.Obs{Y: float32(ys) / float32(ny) * v.scale, Saturated: sat}
	if v.cb != nil {
		cc, cx, cy := c.mul(0.5), hx.mul(0.5), hy.mul(0.5)
		bs, nb, _ := geomMean(v.cb, v.cw, v.ch, cc, cx, cy, v.top)
		rs, nr, _ := geomMean(v.cr, v.cw, v.ch, cc, cx, cy, v.top)
		if nb > 0 && nr > 0 {
			o.Cb, o.Cr = float32(bs)/float32(nb)*v.scale, float32(rs)/float32(nr)*v.scale
		}
	}
	return o
}

// geomMean sums the samples of a w x h plane whose centres lie in the parallelogram c + a hx + b hy,
// |a|, |b| <= 1, and reports their count and whether any was 0 or top. With no sample centre inside,
// it takes the sample under c, if c is in the plane.
func geomMean(p []uint16, w, h int, c, hx, hy gpt, top uint16) (sum, n int, sat bool) {
	ex, ey := math.Abs(hx.X)+math.Abs(hy.X), math.Abs(hx.Y)+math.Abs(hy.Y)
	if ex > sampleMaxHalf || ey > sampleMaxHalf {
		return 0, 0, false
	}
	det := hx.cross(hy)
	x0, x1 := max(int(math.Ceil(c.X-ex-0.5)), 0), min(int(math.Floor(c.X+ex-0.5)), w-1)
	y0, y1 := max(int(math.Ceil(c.Y-ey-0.5)), 0), min(int(math.Floor(c.Y+ey-0.5)), h-1)
	if det != 0 {
		const eps = 1e-9
		for j := y0; j <= y1; j++ {
			for i := x0; i <= x1; i++ {
				d := gpt{float64(i) + 0.5 - c.X, float64(j) + 0.5 - c.Y}
				a, b := d.cross(hy)/det, hx.cross(d)/det
				if math.Abs(a) > 1+eps || math.Abs(b) > 1+eps {
					continue
				}
				s := p[j*w+i]
				sum += int(s)
				n++
				sat = sat || s == 0 || s == top
			}
		}
	}
	if n == 0 && c.X >= 0 && c.Y >= 0 && c.X < float64(w) && c.Y < float64(h) {
		s := p[int(c.Y)*w+int(c.X)]
		return int(s), 1, s == 0 || s == top
	}
	return sum, n, sat
}
