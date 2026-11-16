package vision

import (
	"math"
	"slices"

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
	// probability 1/L, or with the median share over the tiles when several levels clip together.
	// A glare patch lifts every cell it covers to white or beyond. At least minAround usable cells
	// must be seen. A known limit: with three of four levels clipped (chance 0.75), 25 lit cells of
	// 25 are no longer unlikely enough, and a patch goes unnoticed.
	chanceTail = 1e-6
	minAround  = 12
	// clipShare: a median tile share of lit cells this many times 1/L means another level clips
	// too (two levels of L clipped give 2/L).
	clipShare = 1.5
	around    = 2 // cells on each side
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
	// The chance that a cell reads as the lightest level: 1/L, or the median over the tiles of
	// their share when that is at least clipShare times larger, as when an overexposed capture
	// clips more than the lightest level and those levels all read lightest. A patch is local and
	// moves few tiles' shares; the median follows the capture, not the patch.
	nt := (gx + TileW - 1) / TileW * ((gy + TileH - 1) / TileH)
	tileSeen, tileLit := make([]int, nt), make([]int, nt)
	for g, st := range state {
		if st == none {
			continue
		}
		t := g%gx/TileW + g/gx/TileH*((gx+TileW-1)/TileW)
		tileSeen[t]++
		if st == top || st == bright {
			tileLit[t]++
		}
	}
	var shares []float64
	for t := range nt {
		if tileSeen[t] >= minAround {
			shares = append(shares, float64(tileLit[t])/float64(tileSeen[t]))
		}
	}
	chance := 1 / float64(c.Levels())
	if len(shares) > 0 {
		// Only a median well above 1/L (another level at the clip) replaces it: noise around 1/L
		// would otherwise weaken the test on every capture.
		slices.Sort(shares)
		if m := shares[len(shares)/2]; m >= clipShare*chance {
			chance = m
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
			flag = seen >= minAround && chance < 1 && binomialTail(lit, seen, chance) < chanceTail
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
