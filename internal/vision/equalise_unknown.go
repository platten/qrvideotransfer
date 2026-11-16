package vision

import (
	"math"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/modem"
)

// Margins by which the equaliser makes a cell unknown (glare, reflections, overlays: DESIGN.md
// section 10, step 5).
const (
	// A cell brighter than its tile's lightest level by whiteSigmas standard deviations of that
	// level, and by at least whiteFloor code values, shows more light than the screen emits. Five
	// deviations is a Gaussian tail of 3e-7, so noise alone almost never trips it; the floor keeps a
	// near-noiseless capture from flagging a cell one code value above white.
	whiteSigmas = 5
	whiteFloor  = 3
	// A cell is implausible when, in the 5 x 5 cells around it, so many of the usable data cells
	// read as the lightest level (or brighter) that chance would give that many less than once in
	// 1/chanceTail: scrambling makes every level equally likely, so each cell is the lightest with
	// probability 1/L. A glare patch lifts every cell it covers to white or beyond. At least
	// minAround usable cells must be seen.
	chanceTail = 1e-6
	minAround  = 12
	around     = 2 // cells on each side
	// grow is how many rings of cells around a flagged region are flagged too: a glare patch fades
	// out over a quarter of its radius, lifting the cells there without making them white.
	grow = 2
)

// flagUnknown marks as saturated, so that they give zero soft bits, the usable cells that are
// brighter than white by the margin above or sit in a patch of cells that all read white. Cells
// that skip marks (those the phase field does not credit) never trip the brightness margin: they had
// no stripes removed, so a fringe can lift them above white, and a region grown from them would
// spread into credited cells. They still count among the neighbours that read white, since a glare
// patch's core spoils the anchors there and is mostly not credited. Cells the sampler marked
// saturated (clipped) are already unknown but count as neighbours too, by the level they read. It
// returns how many it marked.
func flagUnknown(obs []modem.Obs, l *frame.Layout, f *Field, c *modem.Constellation, skip []bool) int {
	cells := l.DataCells()
	n := min(len(obs), len(cells))
	gx, gy := l.GridX(), l.GridY()
	np := planes(c)
	hi := (c.Levels() - 1) & (modem.MaxLevels - 1)
	const (
		none   = 0
		other  = 1
		top    = 2
		bright = 3
	)
	state := make([]uint8, gx*gy)
	lat := f.lattice()
	for i := range n {
		o := obs[i]
		clipped := o.Saturated && !math.IsNaN(float64(o.Y)) && !math.IsInf(float64(o.Y), 0)
		if !usable(o) && !clipped {
			continue
		}
		cl := cells[i]
		k, fx, fy := lat.locate(float64(cl.X)+0.5-float64(f.TileW)/2, float64(cl.Y)+0.5-float64(f.TileH)/2, true)
		st := f.interpolate(k, fx, fy)
		g := int(cl.Y)*gx + int(cl.X)
		margin := math.Max(whiteSigmas*math.Sqrt(float64(st.Variance[hi])), whiteFloor)
		switch {
		case clipped:
			// The sampler already made it unknown; a clipped light cell still counts among the
			// neighbours that read white (a glare patch's core), a clipped dark one counts as seen.
			if decide(o, &st, np) == hi {
				state[g] = top
			} else {
				state[g] = other
			}
		case !skipped(skip, i) && float64(obs[i].Y) > float64(st.Centre[hi][0])+margin:
			state[g] = bright
		case decide(obs[i], &st, np) == hi:
			state[g] = top
		default:
			state[g] = other
		}
	}
	flagged := make([]bool, gx*gy)
	for i := range n {
		cl := cells[i]
		x, y := int(cl.X), int(cl.Y)
		s := state[y*gx+x]
		if s == none {
			continue
		}
		flag := s == bright
		if !flag {
			seen, lit := 0, 0
			for v := max(y-around, 0); v <= min(y+around, gy-1); v++ {
				for u := max(x-around, 0); u <= min(x+around, gx-1); u++ {
					switch state[v*gx+u] {
					case other:
						seen++
					case top, bright:
						seen++
						lit++
					}
				}
			}
			flag = seen >= minAround && binomialTail(lit, seen, 1/float64(c.Levels())) < chanceTail
		}
		flagged[y*gx+x] = flag
	}
	// A patch's soft edge lifts the cells around it without making them white; grow the flagged
	// region by grow rings of usable cells.
	for range grow {
		next := append([]bool(nil), flagged...)
		for i := range n {
			x, y := int(cells[i].X), int(cells[i].Y)
			if state[y*gx+x] == none || flagged[y*gx+x] {
				continue
			}
			for v := max(y-1, 0); v <= min(y+1, gy-1) && !next[y*gx+x]; v++ {
				for u := max(x-1, 0); u <= min(x+1, gx-1); u++ {
					if flagged[v*gx+u] {
						next[y*gx+x] = true
						break
					}
				}
			}
		}
		flagged = next
	}
	marked := 0
	for i := range n {
		if flagged[int(cells[i].Y)*gx+int(cells[i].X)] {
			obs[i].Saturated = true
			marked++
		}
	}
	return marked
}

// binomialTail returns P(X >= k) for X binomial with n trials of probability p.
func binomialTail(k, n int, p float64) float64 {
	sum := 0.0
	for j := k; j <= n; j++ {
		lg, _ := math.Lgamma(float64(n + 1))
		la, _ := math.Lgamma(float64(j + 1))
		lb, _ := math.Lgamma(float64(n - j + 1))
		sum += math.Exp(lg - la - lb + float64(j)*math.Log(p) + float64(n-j)*math.Log1p(-p))
	}
	return sum
}
