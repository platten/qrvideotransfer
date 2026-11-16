package vision

import (
	"fmt"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/modem"
)

// decodeFrame collects the soft bits of frame fr's codewords from soft (b per data cell, data-cell
// order), descrambles and decodes them. It returns how many codewords had at least 90% of their bits
// stated and how many of those decoded to the shard sent.
func (s *radSession) decodeFrame(soft []int8, fr int) (tried, good int) {
	m := s.mapping
	scr := m.Scrambler(uint32(fr))
	llr := make([]int8, m.Sent())
	shard := make([]byte, s.code.ShardSize())
	for c := range m.CodewordsPerFrame() {
		stated := 0
		for t := range llr {
			q := m.Position(uint32(fr), c, t)
			v := soft[q]
			if scr.Bit(q) == 1 {
				v = -v
			}
			llr[t] = v
			if v != 0 {
				stated++
			}
		}
		if 10*stated < 9*len(llr) {
			continue
		}
		tried++
		if _, ok := s.code.Decode(shard, llr); ok && string(shard) == string(s.shards[fr][c]) {
			good++
		}
	}
	return tried, good
}

// decodeCaptures runs the radiometry chain on every capture, with or without stripe cancellation,
// and decodes every code frame it credits cells to. The frame index stands in for the header: it
// is the true frame that most credited cells of the phase show.
func (s *radSession) decodeCaptures(t testing.TB, caps []*radCapture, stripes bool) (tried, good int) {
	t.Helper()
	soft := make([]int8, len(s.layout.DataCells())*s.mod.BitsPerCell())
	for _, rc := range caps {
		pf := s.phaseOf(rc)
		obs := append([]modem.Obs(nil), rc.obs...)
		f := (&TileEqualiser{Mod: s.mod, Stripes: stripes}).Fit(obs, rc.palette, s.layout, rc.anchors)
		mix := rc.trueMix(s.layout)
		for p := range 4 {
			if SoftBits(soft, obs, s.layout, f, s.mod, &pf, p) == 0 {
				continue
			}
			votes := map[int]int{}
			for i := range mix {
				if q, ok := pf.Phase(i); ok && q == p {
					fr, _ := credited(mix[i], p)
					votes[fr]++
				}
			}
			fr, best := -1, 0
			for k, v := range votes {
				if v > best || v == best && k < fr {
					fr, best = k, v
				}
			}
			if fr < 0 {
				continue
			}
			a, b := s.decodeFrame(soft, fr)
			tried, good = tried+a, good+b
		}
	}
	return tried, good
}

// fringeAmp is the fringe amplitude, in 8-bit code values per colour channel, at which the stripe
// assertion is made.
const fringeAmp = 24

// TestStripeCancellation asserts DESIGN.md section 15 for moire fringes: on `handheld` with fringes
// of amplitude fringeAmp, Gray4 frames with the `normal` inner code decode with stripe cancellation
// on (every codeword whose bits are at least 90% stated) and fail with it off (fewer than half do).
// The CI version films a 212 x 120 grid (29 codewords per frame) with a 1600 x 900 camera, 3
// recordings with different fringe periods and directions (fixed seeds), 2 captures each of 8
// frames shown at 15 fps (6 in the long version). The long version also sweeps the amplitude and
// logs both success rates.
// Statistical: skipped under the race detector (raceSkipStatistics).
func TestStripeCancellation(t *testing.T) {
	raceSkipStatistics(t)
	t.Parallel()
	s := newRadSession(t, 212, 120, 4, 2, 8, 0x5719, true)
	amps := []float64{fringeAmp}
	if longTests() {
		amps = []float64{0, 4, 8, 12, 16, 20, 24, 28, 32, 40, 48}
	}
	for _, amp := range amps {
		seeds, stripeCaps := uint64(3), 2 // CI; the long version films 6 captures per recording
		if longTests() {
			stripeCaps = 6
		}
		for seed := uint64(1); seed <= seeds; seed++ {
			t.Run(fmt.Sprintf("amp%.0f/seed%d", amp, seed), func(t *testing.T) {
				c := preset(t, "handheld", 1600, 900)
				c.Sensor.Fringe = amp
				caps := s.shoot(t, c, time.Second/15, stripeCaps, 0x900+seed)
				offT, offG := s.decodeCaptures(t, caps, false)
				onT, onG := s.decodeCaptures(t, caps, true)
				t.Logf("amplitude %.0f: decoded %d of %d codewords with stripe cancellation off, %d of %d with it on", amp, offG, offT, onG, onT)
				if amp != fringeAmp {
					return
				}
				if onT == 0 || onG != onT {
					t.Errorf("with stripe cancellation on, %d of %d codewords decoded", onG, onT)
				}
				if 2*offG >= offT {
					t.Errorf("with stripe cancellation off, %d of %d codewords decoded: the amplitude does not test it", offG, offT)
				}
			})
		}
	}
}
