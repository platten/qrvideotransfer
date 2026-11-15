package vision

import (
	"math"
	"slices"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/modem"
)

// CleanWeight is the phase weight a cell needs to count for a code frame (DESIGN.md section 7,
// rule 3).
const CleanWeight = 0.92

// blackCode is the code value of no light on the 8-bit scale of modem.Obs: limited-range black.
const blackCode = 16

// minContrast is the smallest difference between an anchor's white and black, in code values, for
// the anchor to serve as a reference.
const minContrast = 8

// AnchorPhase implements PhaseEstimator (DESIGN.md section 7, rule 2). It is a value and safe for
// concurrent use.
type AnchorPhase struct {
	// Gamma is the exponent that turns code values above black (16 on the 8-bit scale) into linear
	// light; 0 means 2.4. PaletteGamma measures it.
	Gamma float64
}

// Estimate returns each data cell's weights for the four values of frame index mod 4. At each
// usable anchor the four quadrant intensities are linearised with the anchor's own black and white
// (see anchorWeights) and projected onto the weights that are non-negative and sum to one; anchors that were not found
// or are unusable are predicted linearly from their usable neighbours. Each cell's weights are
// interpolated bilinearly between the phase-marker centres of the lattice (extrapolated linearly
// beyond the outermost) and projected again. With no usable anchor every weight is zero.
func (e AnchorPhase) Estimate(a []AnchorObs, l *frame.Layout) PhaseField {
	gamma := e.Gamma
	if !(gamma > 0) {
		gamma = 2.4
	}
	lat := newLattice(l, TileW, TileH)
	nodes := make([][4]float64, len(lat.ok))
	for i := range a {
		k, in := lat.index(a[i].Anchor)
		if !in || !a[i].Found {
			continue
		}
		if w, ok := anchorWeights(&a[i], gamma); ok {
			nodes[k], lat.ok[k] = w, true
		}
	}
	cells := l.DataCells()
	pf := PhaseField{Weights: make([][4]float32, len(cells))}
	if !lat.fill(true, func(dst int, terms []term) {
		var s [4]float64
		for _, tm := range terms {
			for p := range 4 {
				s[p] += float64(tm.w * nodes[tm.n][p])
			}
		}
		nodes[dst] = s
	}) {
		return pf
	}
	for i, c := range cells {
		// The phase marker of the anchor at (12 + 32i, 13 + 30j) is centred at (32i + 18, 30j + 15).
		k, fx, fy := lat.locate(float64(c.X)+0.5-18, float64(c.Y)+0.5-15, false)
		var w [4]float64
		for p := range 4 {
			w[p] = lat.bilinear(k, fx, fy, func(n int) float64 { return nodes[n][p] })
		}
		w = simplex(w)
		for p := range 4 {
			pf.Weights[i][p] = float32(w[p])
		}
	}
	return pf
}

// anchorWeights linearises an anchor's phase quadrants against its black and returns their shares
// of the light above black, projected onto the simplex. The weights sum to one, so the quadrants'
// own total is the white reference; the saddle's white, sampled a few cells away, serves only
// when that total is implausibly small (below half of it). Using the quadrants' own total keeps a
// clean anchor clean when fringes, vignetting or sharpening make the phase marker's light quadrant
// differ from the saddle's light quadrants.
func anchorWeights(o *AnchorObs, gamma float64) ([4]float64, bool) {
	var w [4]float64
	if o.Black.Saturated || o.White.Saturated || !(o.White.Y-o.Black.Y >= minContrast) {
		return w, false
	}
	b, wh := linear(o.Black.Y, gamma), linear(o.White.Y, gamma)
	sum := 0.0
	for q, v := range o.Phase {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return w, false
		}
		w[q] = linear(v, gamma) - b
		sum += math.Max(w[q], 0)
	}
	ref := sum
	if ref < (wh-b)/2 {
		ref = wh - b
	}
	for q := range w {
		w[q] /= ref
	}
	return simplex(w), true
}

// linear turns a code value into linear light, up to a scale.
func linear(v float32, gamma float64) float64 {
	return math.Pow(math.Max(float64(v)-blackCode, 0), gamma)
}

// simplex returns the Euclidean projection of v onto {w : w >= 0, sum w = 1}.
func simplex(v [4]float64) [4]float64 {
	s := v
	slices.SortFunc(s[:], func(a, b float64) int { return cmpDesc(a, b) })
	sum, theta := 0.0, 0.0
	for k := range 4 {
		sum += s[k]
		t := (sum - 1) / float64(k+1)
		if s[k]-t > 0 {
			theta = t
		}
	}
	for k := range v {
		v[k] = math.Max(v[k]-theta, 0)
	}
	return v
}

// cmpDesc orders floats from the largest down.
func cmpDesc(a, b float64) int {
	switch {
	case a > b:
		return -1
	case a < b:
		return 1
	}
	return 0
}

// PaletteGamma measures the linearising exponent from the palette (patch k of strip c at 8c + k):
// the exponent that maps the levels' code values, relative to the darkest and lightest, onto the
// relative light of their default appearance on a BT.1886 display (exponent 2.4). It needs a grey
// modulation with more than two levels and every level seen; otherwise it reports false.
//
// The measurement includes the display's own gamma error, which the anchors' blends of black and
// white do not see; DESIGN.md section 7 records what that costs.
func PaletteGamma(palette []modem.Obs, c *modem.Constellation) (float64, bool) {
	n := c.Levels()
	if c.Planes() != modem.PlaneY || n <= 2 {
		return 0, false
	}
	p, ok := paletteLevels(palette, n)
	if !ok || !(p[n-1][0]-p[0][0] >= minContrast) {
		return 0, false
	}
	def := c.DefaultStats(1)
	want := make([]float64, n)
	for k := range n {
		want[k] = math.Pow(float64((def.Centre[k][0]-blackCode)/(def.Centre[n-1][0]-blackCode)), 2.4)
	}
	cost := func(g float64) float64 {
		b, w := linear(p[0][0], g), linear(p[n-1][0], g)
		s := 0.0
		for k := 1; k < n-1; k++ {
			d := (linear(p[k][0], g)-b)/(w-b) - want[k]
			s += d * d
		}
		return s
	}
	// Golden-section search over [1, 4]; the cost is unimodal there for any monotone palette.
	lo, hi := 1.0, 4.0
	r := (math.Sqrt(5) - 1) / 2
	x1, x2 := hi-r*(hi-lo), lo+r*(hi-lo)
	f1, f2 := cost(x1), cost(x2)
	for hi-lo > 1e-4 {
		if f1 < f2 {
			hi, x2, f2 = x2, x1, f1
			x1 = hi - r*(hi-lo)
			f1 = cost(x1)
		} else {
			lo, x1, f1 = x1, x2, f2
			x2 = lo + r*(hi-lo)
			f2 = cost(x2)
		}
	}
	return (lo + hi) / 2, true
}

// paletteLevels returns the median of the usable palette patches showing each level (patch k shows
// level k mod n) and whether every level was seen.
func paletteLevels(palette []modem.Obs, n int) ([modem.MaxLevels][3]float32, bool) {
	var out [modem.MaxLevels][3]float32
	all := true
	for lv := range n {
		var vals [3][]float32
		for i, o := range palette {
			if i%8%n != lv || !usable(o) {
				continue
			}
			vals[0], vals[1], vals[2] = append(vals[0], o.Y), append(vals[1], o.Cb), append(vals[2], o.Cr)
		}
		if len(vals[0]) == 0 {
			all = false
			continue
		}
		for p := range 3 {
			slices.Sort(vals[p])
			m := len(vals[p])
			out[lv][p] = (vals[p][(m-1)/2] + vals[p][m/2]) / 2
		}
	}
	return out, all
}

// usable reports whether an observation is finite and not saturated.
func usable(o modem.Obs) bool {
	s := o.Y + o.Cb + o.Cr
	return !o.Saturated && !math.IsNaN(float64(s)) && !math.IsInf(float64(s), 0)
}

// Phase returns the phase (frame index mod 4) that data cell i counts for, and false when no
// weight reaches CleanWeight.
func (p *PhaseField) Phase(i int) (int, bool) {
	if i < 0 || i >= len(p.Weights) {
		return 0, false
	}
	for q, w := range p.Weights[i] {
		if w >= CleanWeight {
			return q, true
		}
	}
	return 0, false
}

// Clean reports whether any cell counts for some phase. A capture where none does is unusable and
// raises the ExposureTooLong warning (DESIGN.md section 7, rule 10).
func (p *PhaseField) Clean() bool {
	for i := range p.Weights {
		if _, ok := p.Phase(i); ok {
			return true
		}
	}
	return false
}
