package vision

import (
	"math"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/modem"
)

// Tile size of the level calibration, in cells (DESIGN.md section 10, step 5). Tile (i, j) covers
// cells [32i, 32i + 32) x [30j, 30j + 30); its centre, (32i + 16, 30j + 15), is the centre of the
// anchor at (12 + 32i, 13 + 30j).
const (
	TileW = 32
	TileH = 30
)

// Constants of the fit.
const (
	defaultRounds = 3
	priorCells    = 4 // weight, in cells, of a tile's starting levels in every EM round
	stripePasses  = 2 // rounds of stripe cancellation and refitting
)

// TileEqualiser implements Equaliser: per-tile level statistics started from the palette and the
// anchors' black and white, refined by expectation-maximisation on the data cells, and optionally
// stripe cancellation (DESIGN.md section 10, steps 5 and 6). It holds no state between calls and is
// safe for concurrent use.
type TileEqualiser struct {
	Mod     *modem.Constellation // the modulation of the frames
	Rounds  int                  // EM rounds; 0 means 3
	Stripes bool                 // cancel stripes, correcting obs in place, and fit again
}

// Fit returns the level statistics of every tile. obs holds one observation per data cell in
// data-cell order; palette holds patch k of strip c (a frame.Corner) at 8c + k and may be empty.
// Saturated and non-finite observations are ignored. With Stripes set, Fit subtracts the stripes
// it finds from obs (CancelStripes).
//
// Each tile starts from its anchor's black and white (or its neighbours' when the anchor is
// unusable), with the middle levels placed between them where the palette shows them relative to
// its own darkest and lightest patches, and chroma offset from the palette by the anchor's. Each EM
// round weighs every cell's level responsibilities, under the statistics interpolated at the cell,
// into the tiles around it with bilinear weights, together with priorCells cells at the tile's
// starting levels and at the level's variance over the whole capture.
//
// Only cells that count for some phase are fitted (and have stripes removed): Fit estimates the phase field
// from the anchors itself (AnchorPhase with PaletteGamma) and leaves out the blends of two code
// frames, which are no level and would widen the variances, the dark level's most (a blend adds
// far more to a dark cell's code value than it takes from a light one's). When no cell counts for
// any phase, every cell is fitted.
func (e *TileEqualiser) Fit(obs []modem.Obs, palette []modem.Obs, l *frame.Layout, a []AnchorObs) *Field {
	f := e.start(palette, l, a)
	// Cells that blend code frames are no level; the anchors' phase field finds them.
	gamma, _ := PaletteGamma(palette, e.Mod)
	skip := mixedCells(AnchorPhase{Gamma: gamma}.Estimate(a, l))
	prior := append([]modem.LocalStats(nil), f.Tiles...)
	rounds := e.Rounds
	if rounds <= 0 {
		rounds = defaultRounds
	}
	for range rounds {
		e.round(f, prior, obs, l, skip)
	}
	if e.Stripes {
		for range stripePasses {
			if cancelStripes(obs, l, f, e.Mod, skip) == 0 {
				break
			}
			for range rounds {
				e.round(f, prior, obs, l, skip)
			}
		}
	}
	return f
}

// start returns the starting statistics of every tile.
func (e *TileEqualiser) start(palette []modem.Obs, l *frame.Layout, a []AnchorObs) *Field {
	c := e.Mod
	n := c.Levels()
	def := c.DefaultStats(1)
	p, ok := paletteLevels(palette, n)
	hi := (n - 1) & (modem.MaxLevels - 1) // the lightest level; n is 2, 4 or 8
	if !ok || !(p[hi][0]-p[0][0] >= minContrast) {
		for lv := range n {
			p[lv] = def.Centre[lv]
		}
	}
	lat := newLattice(l, TileW, TileH)
	refs := make([][2]modem.Obs, len(lat.ok)) // black and white per tile
	for i := range a {
		k, in := lat.index(a[i].Anchor)
		o := &a[i]
		if in && o.Found && usable(o.Black) && usable(o.White) && o.White.Y-o.Black.Y >= minContrast {
			refs[k], lat.ok[k] = [2]modem.Obs{o.Black, o.White}, true
		}
	}
	if !lat.fill(false, func(dst int, terms []term) {
		var s [2]modem.Obs
		for _, tm := range terms {
			w := float32(tm.w)
			for r := range 2 {
				s[r].Y += float32(w * refs[tm.n][r].Y)
				s[r].Cb += float32(w * refs[tm.n][r].Cb)
				s[r].Cr += float32(w * refs[tm.n][r].Cr)
			}
		}
		refs[dst] = s
	}) {
		top := p[hi]
		for k := range refs {
			refs[k] = [2]modem.Obs{{Y: p[0][0], Cb: p[0][1], Cr: p[0][2]}, {Y: top[0], Cb: top[1], Cr: top[2]}}
		}
	}
	f := &Field{TilesX: lat.nx, TilesY: lat.ny, TileW: TileW, TileH: TileH, Tiles: make([]modem.LocalStats, len(refs))}
	span := p[hi][0] - p[0][0]
	for k, r := range refs {
		st := &f.Tiles[k]
		st.Levels = n
		b, w := r[0], r[1]
		sp := w.Y - b.Y
		sd := sp / float32(n-1) / 6 // a sixth of the level spacing
		for lv := range n {
			st.Centre[lv][0] = b.Y + sp*(p[lv][0]-p[0][0])/span
			st.Centre[lv][1] = p[lv][1] + ((b.Cb+w.Cb)-(p[0][1]+p[hi][1]))/2
			st.Centre[lv][2] = p[lv][2] + ((b.Cr+w.Cr)-(p[0][2]+p[hi][2]))/2
			st.Variance[lv] = max(sd*sd, modem.MinVariance)
		}
	}
	return f
}

// round is one EM round over the data cells.
func (e *TileEqualiser) round(f *Field, prior []modem.LocalStats, obs []modem.Obs, l *frame.Layout, skip []bool) {
	n := e.Mod.Levels()
	np := planes(e.Mod)
	type acc struct {
		w   [modem.MaxLevels]float64
		sum [modem.MaxLevels][3]float64
		sq  [modem.MaxLevels]float64
	}
	accs := make([]acc, len(f.Tiles))
	lat := f.lattice()
	var resp [modem.MaxLevels]float64
	for i, c := range l.DataCells() {
		if i >= len(obs) || !usable(obs[i]) || skipped(skip, i) {
			continue
		}
		o := [3]float64{float64(obs[i].Y), float64(obs[i].Cb), float64(obs[i].Cr)}
		k, fx, fy := lat.locate(float64(c.X)+0.5-float64(f.TileW)/2, float64(c.Y)+0.5-float64(f.TileH)/2, true)
		st := f.interpolate(k, fx, fy)
		// Responsibilities, with each level's own variance and its log term.
		best := math.Inf(-1)
		var d2 [modem.MaxLevels]float64
		for lv := range n {
			d := 0.0
			for p := range np {
				x := o[p] - float64(st.Centre[lv][p])
				d += x * x
			}
			v := float64(st.Variance[lv])
			d2[lv] = d
			resp[lv] = -d/(2*v) - float64(np)/2*math.Log(v)
			best = math.Max(best, resp[lv])
		}
		tot := 0.0
		for lv := range n {
			resp[lv] = math.Exp(resp[lv] - best)
			tot += resp[lv]
		}
		for _, t := range lat.corners(k, fx, fy) {
			if t.w == 0 {
				continue
			}
			a := &accs[t.n]
			for lv := range n {
				r := t.w * resp[lv] / tot
				a.w[lv] += r
				for p := range np {
					a.sum[lv][p] += r * o[p]
				}
				a.sq[lv] += r * d2[lv]
			}
		}
	}
	// The variance prior is each level's variance over the whole capture, or the starting one
	// where there are no cells at all.
	var gw, gsq [modem.MaxLevels]float64
	for k := range accs {
		for lv := range n {
			gw[lv] += accs[k].w[lv]
			gsq[lv] += accs[k].sq[lv]
		}
	}
	for k := range f.Tiles {
		st, pr, a := &f.Tiles[k], &prior[k], &accs[k]
		for lv := range n {
			w := a.w[lv] + priorCells
			for p := range np {
				st.Centre[lv][p] = float32((a.sum[lv][p] + priorCells*float64(pr.Centre[lv][p])) / w)
			}
			pv := float64(pr.Variance[lv])
			if gw[lv] > 0 {
				pv = gsq[lv] / gw[lv] / float64(np)
			}
			v := (a.sq[lv]/float64(np) + priorCells*pv) / w
			st.Variance[lv] = float32(math.Max(v, modem.MinVariance))
		}
	}
}

// mixedCells marks the cells that count for no phase, or returns nil when none counts (no usable
// anchor, or an exposure too long), in which case every cell is fitted.
func mixedCells(pf PhaseField) []bool {
	if !pf.Clean() {
		return nil
	}
	skip := make([]bool, len(pf.Weights))
	for i := range skip {
		_, ok := pf.Phase(i)
		skip[i] = !ok
	}
	return skip
}

// skipped reports whether skip marks cell i.
func skipped(skip []bool, i int) bool { return i < len(skip) && skip[i] }

// planes returns how many planes of Obs a modulation uses: 1 for brightness only, else 3.
func planes(c *modem.Constellation) int {
	if c.Planes() == modem.PlaneY {
		return 1
	}
	return 3
}

// lattice returns the field's tile lattice.
func (f *Field) lattice() lattice {
	return lattice{nx: f.TilesX, ny: f.TilesY, sx: float64(f.TileW), sy: float64(f.TileH)}
}

// At returns the statistics at cell (x, y), interpolated bilinearly between tile centres and
// extrapolated as constant beyond the outermost centres.
func (f *Field) At(x, y int) modem.LocalStats {
	lat := f.lattice()
	k, fx, fy := lat.locate(float64(x)+0.5-float64(f.TileW)/2, float64(y)+0.5-float64(f.TileH)/2, true)
	return f.interpolate(k, fx, fy)
}

// interpolate mixes the statistics of the tiles around a lattice position.
func (f *Field) interpolate(k int, fx, fy float64) modem.LocalStats {
	lat := f.lattice()
	st := modem.LocalStats{Levels: f.Tiles[k].Levels}
	for _, t := range lat.corners(k, fx, fy) {
		if t.w == 0 {
			continue
		}
		w := float32(t.w)
		src := &f.Tiles[t.n]
		for lv := range st.Levels {
			for p := range 3 {
				st.Centre[lv][p] += float32(w * src.Centre[lv][p])
			}
			st.Variance[lv] += float32(w * src.Variance[lv])
		}
	}
	return st
}

// SoftBits writes b soft bits per data cell to dst, in data-cell order (DESIGN.md section 10,
// steps 7 and 8): m's LLR with the cell's interpolated statistics for cells whose phase weight for
// phase (the frame index mod 4) is at least CleanWeight, and zeros for the others. A nil p counts
// every cell. It returns the number of cells that count. Cells beyond obs and unusable
// observations give zeros and do not count.
func SoftBits(dst []int8, obs []modem.Obs, l *frame.Layout, f *Field, m modem.Modem, p *PhaseField, phase int) int {
	b := m.BitsPerCell()
	n := 0
	lat := f.lattice()
	for i, c := range l.DataCells() {
		d := dst[i*b : i*b+b]
		if i >= len(obs) || !usable(obs[i]) || p != nil && (i >= len(p.Weights) || p.Weights[i][phase&3] < CleanWeight) {
			clear(d)
			continue
		}
		k, fx, fy := lat.locate(float64(c.X)+0.5-float64(f.TileW)/2, float64(c.Y)+0.5-float64(f.TileH)/2, true)
		st := f.interpolate(k, fx, fy)
		if p != nil {
			blend(&st, 1-float64(p.Weights[i][phase&3]))
		}
		m.LLR(d, obs[i], &st, 1)
		n++
	}
	return n
}

// blend widens every level's variance by what a share u of another code frame, showing a random
// level, adds to a cell: u^2 times the variance of the level centres, per plane.
func blend(st *modem.LocalStats, u float64) {
	if !(u > 0) {
		return
	}
	n := st.Levels
	var mean [3]float64
	for lv := range n {
		for p := range 3 {
			mean[p] += float64(st.Centre[lv][p]) / float64(n)
		}
	}
	spread := 0.0
	for lv := range n {
		for p := range 3 {
			d := float64(st.Centre[lv][p]) - mean[p]
			spread += d * d
		}
	}
	add := float32(u * u * spread / float64(3*n))
	for lv := range n {
		st.Variance[lv] += add
	}
}

// lattice is a grid of nx x ny nodes spaced sx and sy cells apart, with node (i, j) at index
// j nx + i, used for the tiles and for the anchors' phase markers, which share it.
type lattice struct {
	nx, ny int
	sx, sy float64
	ok     []bool // which nodes hold a value; used while filling
}

// newLattice returns the lattice of a layout's tiles, one node per tile, none holding a value.
func newLattice(l *frame.Layout, sx, sy int) lattice {
	nx, ny := (l.GridX()+sx-1)/sx, (l.GridY()+sy-1)/sy
	return lattice{nx: nx, ny: ny, sx: float64(sx), sy: float64(sy), ok: make([]bool, nx*ny)}
}

// index returns the node of an anchor and whether the anchor is on the lattice.
func (t *lattice) index(a frame.Anchor) (int, bool) {
	if a.X < 12 || a.Y < 13 || (a.X-12)%TileW != 0 || (a.Y-13)%TileH != 0 {
		return 0, false
	}
	i, j := (a.X-12)/TileW, (a.Y-13)/TileH
	if i >= t.nx || j >= t.ny {
		return 0, false
	}
	return j*t.nx + i, true
}

// term is one node of a linear combination.
type term struct {
	n int
	w float64
}

// fill gives every node without a value a linear combination of nodes that have one, by calling
// set(node, terms), ring by ring outward from the nodes that hold values.
//
// With linear set, a node takes the mean of linear predictions, the most local first: the
// parallelograms of three valued nodes in a quadrant around it; else the predictions along the
// lattice's rows and columns (the midpoint of valued nodes on either side, or the extrapolation
// from two valued nodes in a row on one side); else the same along the diagonals; else the mean of
// its valued neighbours. Local predictions come first because a longer one is likelier to cross a
// bend in the field (where a frame boundary's blend ends). Without linear set, a node takes the
// mean of its valued neighbours, which does not amplify their noise.
//
// fill returns false when no node holds a value.
func (t *lattice) fill(linear bool, set func(dst int, terms []term)) bool {
	missing := 0
	for _, ok := range t.ok {
		if !ok {
			missing++
		}
	}
	if missing == len(t.ok) {
		return false
	}
	valued := func(x, y int) bool { return x >= 0 && y >= 0 && x < t.nx && y < t.ny && t.ok[y*t.nx+x] }
	for missing > 0 {
		var ring []int
		var all [][]term
		for k, ok := range t.ok {
			if ok {
				continue
			}
			var axis, diag, near []term
			i, j := k%t.nx, k/t.nx
			for dj := -1; dj <= 1; dj++ {
				for di := -1; di <= 1; di++ {
					if di == 0 && dj == 0 || !valued(i+di, j+dj) {
						continue
					}
					a := (j+dj)*t.nx + i + di
					near = append(near, term{a, 1})
					pred := &axis
					if di != 0 && dj != 0 {
						pred = &diag
					}
					switch {
					case valued(i-di, j-dj) && (di > 0 || di == 0 && dj > 0): // each midpoint once
						*pred = append(*pred, term{a, 0.5}, term{(j-dj)*t.nx + i - di, 0.5})
					case !valued(i-di, j-dj) && valued(i+2*di, j+2*dj):
						*pred = append(*pred, term{a, 2}, term{(j+2*dj)*t.nx + i + 2*di, -1})
					}
				}
			}
			// The parallelogram of the three nearest nodes in a quadrant is the most local plane.
			var para []term
			for _, d := range [4][2]int{{1, 1}, {1, -1}, {-1, 1}, {-1, -1}} {
				if valued(i+d[0], j) && valued(i, j+d[1]) && valued(i+d[0], j+d[1]) {
					para = append(para, term{j*t.nx + i + d[0], 1}, term{(j+d[1])*t.nx + i, 1}, term{(j+d[1])*t.nx + i + d[0], -1})
				}
			}
			pred := para
			if !linear {
				pred = near
			}
			if len(pred) == 0 {
				pred = axis
			}
			if len(pred) == 0 {
				pred = diag
			}
			if len(pred) == 0 {
				pred = near
			}
			if len(pred) > 0 {
				sum := 0.0
				for _, p := range pred {
					sum += p.w
				}
				for q := range pred {
					pred[q].w /= sum
				}
				ring = append(ring, k)
				all = append(all, pred)
			}
		}
		for r, k := range ring {
			set(k, all[r])
		}
		for _, k := range ring {
			t.ok[k] = true
		}
		missing -= len(ring)
	}
	return true
}

// locate returns the lattice square around position (x, y), in cells from node (0, 0): the index
// of its top-left node and the fractions across it. Beyond the outermost nodes the fractions are
// clamped to [0, 1] when clamp is set and extrapolate otherwise.
func (t *lattice) locate(x, y float64, clamp bool) (k int, fx, fy float64) {
	i, fx := cellOf(x/t.sx, t.nx, clamp)
	j, fy := cellOf(y/t.sy, t.ny, clamp)
	return j*t.nx + i, fx, fy
}

// cellOf splits a lattice coordinate into the index of a node with a neighbour after it and the
// fraction past that node.
func cellOf(u float64, n int, clamp bool) (int, float64) {
	if n < 2 {
		return 0, 0
	}
	i := min(max(int(math.Floor(u)), 0), n-2)
	f := u - float64(i)
	if clamp {
		f = min(max(f, 0), 1)
	}
	return i, f
}

// corner is a node and its bilinear weight.
type corner struct {
	n int
	w float64
}

// corners returns the four nodes of the square at k and their bilinear weights (which may be
// negative when extrapolating). Along an axis with a single node the weights fall on it.
func (t *lattice) corners(k int, fx, fy float64) [4]corner {
	dx, dy := 1, t.nx
	if t.nx < 2 {
		dx = 0
	}
	if t.ny < 2 {
		dy = 0
	}
	return [4]corner{
		{k, float64((1 - fx) * (1 - fy))},
		{k + dx, float64(fx * (1 - fy))},
		{k + dy, float64((1 - fx) * fy)},
		{k + dx + dy, float64(fx * fy)},
	}
}

// bilinear interpolates a value given per node over the square at k.
func (t *lattice) bilinear(k int, fx, fy float64, val func(n int) float64) float64 {
	s := 0.0
	for _, c := range t.corners(k, fx, fy) {
		s += float64(c.w * val(c.n))
	}
	return s
}
