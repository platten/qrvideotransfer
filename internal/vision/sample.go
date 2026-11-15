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

// Glare (clipping) parameters. A phone records limited-range video, whose nominal white, 235 at
// 8 bits, is where the sensor's clipped highlights land; noise and sharpening scatter them a few
// codes either side. A cell's samples are tested against that level less a margin.
const (
	// sampleClipWhite is the limited-range nominal white on the 8-bit scale.
	sampleClipWhite = 235
	// sampleClipMargin is how far below sampleClipWhite a sample still counts as clipped, in 8-bit
	// codes. Under the simulator's handheld noise (about 2.6 codes RMS in brightness) a clipped
	// sample falls below 233 about one time in eight, while display white exposed at 0.9 of the
	// sensor's range (about 225) reaches 233 about one time in 200.
	sampleClipMargin = 4
	// sampleClipCell is the share of a cell's footprint samples that must be clipped for the cell to
	// be a glare candidate: half, so that one noisy sample neither makes nor breaks it.
	sampleClipCell = 0.5
	// sampleClipArea is the share of the samples over the cell's 3 x 3-cell neighbourhood (its
	// central part, 2.6 cells square) that must be clipped too. Glare is a patch many cells wide,
	// while data is scrambled: at most this share of a neighbourhood is white only when all nine of
	// its cells, or eight, show white, which happens to 1 cell in 2,700 or so with four greys and to
	// 3.5% of the white cells with black and white. It spares a capture whose display white lands on
	// the clip (the ideal preset), where every white cell is clipped. It costs the outermost cell or
	// so of a patch, whose neighbourhood straddles its edge.
	sampleClipArea  = 0.85
	sampleClipReach = 1.3 // half-side of the neighbourhood tested, in cells
	// sampleClipIsolated is the share of the sampled cells that may be clipped without a clipped
	// neighbourhood before the capture is taken to show its display white at the clip. Under glare
	// only a patch's rim is such a cell (0.4% of the cells for a 5% patch, about 0.5% for 20%);
	// with the white at the clip every white data cell is (a quarter of the cells with four greys,
	// an eighth with eight colours, half with black and white).
	sampleClipIsolated = 0.05
	// sampleClipMinCells is the fewest cells sampled at once for that decision; with fewer, only
	// cells in a clipped area are flagged.
	sampleClipMinCells = 1000
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
// cell's centre is used.
//
// A cell is saturated when any of its brightness samples is at the bottom or the top of the code
// range, or when it is under glare: at least sampleClipCell of its samples, and sampleClipArea of
// those over its 3 x 3-cell neighbourhood, are clipped (within sampleClipMargin of the limited-range
// white or above). A saturated cell gives zero soft bits downstream. A cell outside the capture, an
// invalid frame or mesh, or a footprint wider than 256 pixels gives a saturated observation with
// zero values: no information.
func (FootprintSampler) Sample(dst []modem.Obs, f *video.Frame, m *Mesh, cells []frame.Cell) {
	n := min(len(dst), len(cells))
	dst, cells = dst[:n], cells[:n]
	v, ok := geomView(f)
	type candidate struct {
		i    int
		area bool // its neighbourhood is clipped too
	}
	var clipped []candidate
	isolated := 0
	for i := range dst {
		if !ok {
			dst[i] = modem.Obs{Saturated: true}
			continue
		}
		x, y := float64(cells[i].X)+0.5, float64(cells[i].Y)+0.5
		const h = sampleShare / 2
		c := gpt(m.At(x, y))
		hx := gpt(m.At(x+h, y)).sub(gpt(m.At(x-h, y))).mul(0.5)
		hy := gpt(m.At(x, y+h)).sub(gpt(m.At(x, y-h))).mul(0.5)
		o, share := v.footprint(c, hx, hy)
		dst[i] = o
		if o.Saturated || share < sampleClipCell {
			continue
		}
		k := sampleClipReach / h
		_, nn, cn, _ := geomMean(v.y, v.w, v.h, c, hx.mul(k), hy.mul(k), v.top, v.clip())
		area := nn > 0 && float64(cn) >= sampleClipArea*float64(nn)
		clipped = append(clipped, candidate{i, area})
		if !area {
			isolated++
		}
	}
	// Clipped cells standing alone mean the display's white itself reaches the clip: then only
	// cells inside a clipped area are glare. Otherwise every clipped cell is, which also catches
	// the edge of a patch.
	whiteClips := len(dst) < sampleClipMinCells || float64(isolated) > sampleClipIsolated*float64(len(dst))
	for _, c := range clipped {
		if c.area || !whiteClips {
			dst[c.i].Saturated = true
		}
	}
}

// clip returns the lowest code value counted as clipped (see sampleClipMargin).
func (v *geomLuma) clip() uint16 {
	return uint16(math.Ceil(float64(sampleClipWhite-sampleClipMargin) / float64(v.scale)))
}

// footprint returns the observation of the parallelogram c + a hx + b hy, |a|, |b| <= 1, in image
// coordinates (see FootprintSampler.Sample), and the share of its brightness samples that are
// clipped. Saturated there means a sample at the bottom or top of the code range only.
func (v *geomLuma) footprint(c, hx, hy gpt) (modem.Obs, float64) {
	unknown := modem.Obs{Saturated: true}
	if !geomFinite(c, hx, hy) {
		return unknown, 0
	}
	ys, ny, cn, sat := geomMean(v.y, v.w, v.h, c, hx, hy, v.top, v.clip())
	if ny == 0 {
		return unknown, 0
	}
	o := modem.Obs{Y: float32(ys) / float32(ny) * v.scale, Saturated: sat}
	if v.cb != nil {
		cc, cx, cy := c.mul(0.5), hx.mul(0.5), hy.mul(0.5)
		bs, nb, _, _ := geomMean(v.cb, v.cw, v.ch, cc, cx, cy, v.top, v.top)
		rs, nr, _, _ := geomMean(v.cr, v.cw, v.ch, cc, cx, cy, v.top, v.top)
		if nb > 0 && nr > 0 {
			o.Cb, o.Cr = float32(bs)/float32(nb)*v.scale, float32(rs)/float32(nr)*v.scale
		}
	}
	return o, float64(cn) / float64(ny)
}

// geomMean sums the samples of a w x h plane whose centres lie in the parallelogram c + a hx + b hy,
// |a|, |b| <= 1, and reports their count, how many are at least clip, and whether any was 0 or top.
// With no sample centre inside, it takes the sample under c, if c is in the plane.
func geomMean(p []uint16, w, h int, c, hx, hy gpt, top, clip uint16) (sum, n, clipped int, sat bool) {
	ex, ey := math.Abs(hx.X)+math.Abs(hy.X), math.Abs(hx.Y)+math.Abs(hy.Y)
	if ex > sampleMaxHalf || ey > sampleMaxHalf {
		return 0, 0, 0, false
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
				if s >= clip {
					clipped++
				}
				sat = sat || s == 0 || s == top
			}
		}
	}
	if n == 0 && c.X >= 0 && c.Y >= 0 && c.X < float64(w) && c.Y < float64(h) {
		s := p[int(c.Y)*w+int(c.X)]
		cn := 0
		if s >= clip {
			cn = 1
		}
		return int(s), 1, cn, s == 0 || s == top
	}
	return sum, n, clipped, sat
}
