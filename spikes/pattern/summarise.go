package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"

	"github.com/platten/qrvideotransfer/internal/modem"
)

// minBucket is the fewest bits a confidence bucket needs to be judged, as in the honesty tests, and
// minExpected the fewest errors it must state for its observed-over-stated ratio to mean anything.
const (
	minBucket   = 10000
	minExpected = 10
)

// pixelFigure is delivered bits per cell at a given camera pixels per cell: over the located
// captures' passes, and over every code frame shown.
type pixelFigure struct{ located, all, lowPx, highPx float64 }

// atPixels interpolates a modulation's delivered bits per cell at px camera pixels per cell,
// linearly between the two located segments of display rate rate whose pixels per cell bracket it.
func atPixels(segs []segReport, rate int, mod uint8, px float64) (pixelFigure, bool) {
	var lo, hi *segReport
	for i := range segs {
		s := &segs[i]
		if s.Rate != rate || s.Modulation != mod || s.Captures == 0 || s.PxPerCell <= 0 {
			continue
		}
		if s.PxPerCell <= px && (lo == nil || s.PxPerCell > lo.PxPerCell) {
			lo = s
		}
		if s.PxPerCell >= px && (hi == nil || s.PxPerCell < hi.PxPerCell) {
			hi = s
		}
	}
	if lo == nil || hi == nil {
		return pixelFigure{}, false
	}
	f := 0.0
	if hi.PxPerCell > lo.PxPerCell {
		f = (px - lo.PxPerCell) / (hi.PxPerCell - lo.PxPerCell)
	}
	return pixelFigure{
		located: lo.DeliveredLocated + f*(hi.DeliveredLocated-lo.DeliveredLocated),
		all:     lo.DeliveredAll + f*(hi.DeliveredAll-lo.DeliveredAll),
		lowPx:   lo.PxPerCell, highPx: hi.PxPerCell,
	}, true
}

// calRatios returns the largest and smallest observed-over-stated error rate among the buckets below
// the two most confident with at least minBucket bits that state or show at least minExpected
// errors, with their buckets: a bucket stating few errors but showing many is not hidden.
func calRatios(c *calTable) (hi float64, hiB int, lo float64, loB int, ok bool) {
	lo = math.Inf(1)
	for j := range calBuckets - 2 {
		if c.N[j] < minBucket || c.Stated[j] < minExpected && float64(c.Errs[j]) < minExpected || c.Stated[j] <= 0 {
			continue
		}
		r := float64(c.Errs[j]) / c.Stated[j]
		if !ok || r > hi {
			hi, hiB = r, j
		}
		if r < lo {
			lo, loB = r, j
		}
		ok = true
	}
	return hi, hiB, lo, loB, ok
}

// bestSegment returns the index of the segment with the largest figure f among those with at least
// minLocated of their captures located, or -1.
func bestSegment(segs []segReport, f func(*segReport) float64, minLocated float64) int {
	best := -1
	for i := range segs {
		if segs[i].Located < minLocated {
			continue
		}
		if best < 0 || f(&segs[i]) > f(&segs[best]) {
			best = i
		}
	}
	return best
}

// confidentErrors returns the error rate among the bits of the two most confident buckets (|s| of
// 102 and more), where the stated rate is below 1e-11: any error there is a confident one.
func confidentErrors(c *calTable) float64 {
	n, e := c.N[8]+c.N[9], c.Errs[8]+c.Errs[9]
	if n == 0 {
		return 0
	}
	return float64(e) / float64(n)
}

// neighbourChance is the chance that a wrong b-bit symbol equals the symbol of one of the two
// neighbouring frames by chance, each uniform and independent of it.
func neighbourChance(b int) float64 {
	q := 1 - math.Pow(2, -float64(b))
	return 1 - q*q
}

// summariseFiles reads the -json outputs of several recordings and prints the S2 bars and the
// residual figures (docs/IMPLEMENTATION_PROMPT.md sections 5 and 6).
func summariseFiles(paths []string, w io.Writer) error {
	var recs []recording
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var r recording
		if err := json.Unmarshal(b, &r); err != nil {
			return err
		}
		recs = append(recs, r)
	}
	fmt.Fprintln(w, "Bar 1: Gray4 delivered bits per cell at 5 camera pixels per cell (interpolated between the cell sizes that bracket 5)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| Recording | Display rate | Pixels per cell bracketing 5 | Under located captures | Over every frame shown |")
	fmt.Fprintln(w, "| --- | --- | --- | --- | --- |")
	for _, r := range recs {
		for _, rate := range []int{12, 30, 60} {
			if f, ok := atPixels(r.Segments, rate, 2, 5); ok {
				fmt.Fprintf(w, "| %s | %d | %.2f to %.2f | %.3f | %.3f |\n", r.Label, rate, f.lowPx, f.highPx, f.located, f.all)
			}
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Bar 2: clean cells where the camera runs at twice the display rate")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| Recording | Rate | Cell | Mod | Located | Clean per capture | Clean per code frame (some capture) |")
	fmt.Fprintln(w, "| --- | --- | --- | --- | --- | --- | --- |")
	for _, r := range recs {
		for _, s := range r.Segments {
			if math.Abs(s.RateRatio-2) < 0.05 && s.Captures > 0 {
				fmt.Fprintf(w, "| %s | %d | %d | %s | %d/%d | %.3f | %.3f |\n", r.Label, s.Rate, s.Cell, modNames[s.Modulation],
					s.Captures, s.ByTime, s.CleanCapture, s.CleanLocated)
			}
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Bar 3: estimated net rate against the recording's bitrate, best segment per recording (under located captures: among segments at least half located)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| Recording | Mbit/s | Best under located captures | Delivered | Net Mbit/s | Share | Best over every frame shown | Delivered | Net Mbit/s | Share |")
	fmt.Fprintln(w, "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |")
	for _, r := range recs {
		best, bestAll := bestSegment(r.Segments, netRate, 0.5), bestSegment(r.Segments, netRateAll, 0)
		if bestAll < 0 {
			continue
		}
		loc := "none at least half located | | | "
		if best >= 0 {
			s := &r.Segments[best]
			loc = fmt.Sprintf("%d fps, %d px, %s | %.3f | %.2f | %.1f%%", s.Rate, s.Cell, modNames[s.Modulation], s.DeliveredLocated, netRate(s), 100*netRate(s)/r.Mbps)
		}
		a := &r.Segments[bestAll]
		fmt.Fprintf(w, "| %s | %.1f | %s | %d fps, %d px, %s | %.3f | %.2f | %.1f%% |\n", r.Label, r.Mbps, loc,
			a.Rate, a.Cell, modNames[a.Modulation], a.DeliveredAll, netRateAll(a), 100*netRateAll(a)/r.Mbps)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Residuals: confidence, pooled per recording and modulation over the segments. Ratios of observed to stated error rate over buckets of at least 10,000 bits stating at least 10 errors; confident errors are the error rate among bits with |s| >= 102 (stated below 1e-11)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| Recording | Mod | Cells | Bits | Largest observed/stated (bucket) | Smallest (bucket) | Confident errors |")
	fmt.Fprintln(w, "| --- | --- | --- | --- | --- | --- | --- |")
	var pooled [4][2]calTable
	for _, r := range recs {
		for _, mod := range []uint8{1, 2, 3} {
			var c [2]calTable
			for _, s := range r.Segments {
				if s.Modulation == mod {
					c[0].merge(&s.Cal[0])
					c[1].merge(&s.Cal[1])
				}
			}
			pooled[mod][0].merge(&c[0])
			pooled[mod][1].merge(&c[1])
			for t, name := range []string{"wholly one frame", "partly mixed"} {
				printCal(w, r.Label, mod, name, &c[t])
			}
		}
	}
	for _, mod := range []uint8{1, 2, 3} {
		for t, name := range []string{"wholly one frame", "partly mixed"} {
			printCal(w, "all recordings", mod, name, &pooled[mod][t])
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Residuals: wrong credit (credited cells whose stated symbol is wrong, and of those how many equal a neighbouring frame's symbol, against the share chance alone gives), yield and glare")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| Recording | Mod | Credited cells | Wrong | Matching a neighbour | Chance alone | Mean clean share per capture | Largest glare share |")
	fmt.Fprintln(w, "| --- | --- | --- | --- | --- | --- | --- | --- |")
	for _, r := range recs {
		for _, mod := range []uint8{1, 2, 3} {
			var cred, wrong, nb, caps int
			var clean, glare float64
			for _, s := range r.Segments {
				if s.Modulation != mod {
					continue
				}
				cred, wrong, nb = cred+s.Credited, wrong+s.WrongCells, nb+s.Neighbour
				clean += s.CleanCapture * float64(s.Captures)
				caps += s.Captures
				glare = math.Max(glare, s.Glare)
			}
			if caps == 0 {
				continue
			}
			clean /= float64(caps)
			share := 0.0
			if wrong > 0 {
				share = float64(nb) / float64(wrong)
			}
			c, _ := modem.ByID(mod)
			fmt.Fprintf(w, "| %s | %s | %d | %d (%.1f%%) | %d (%.1f%%) | %.1f%% | %.3f | %.4f |\n", r.Label, modNames[mod], cred, wrong,
				100*float64(wrong)/math.Max(1, float64(cred)), nb, 100*share, 100*neighbourChance(c.BitsPerCell()), clean, glare)
		}
	}
	return nil
}

func printCal(w io.Writer, label string, mod uint8, name string, c *calTable) {
	n := 0
	for j := range calBuckets {
		n += c.N[j]
	}
	if n == 0 {
		return
	}
	hi, hb, lo, lb, ok := calRatios(c)
	conf := confidentErrors(c)
	if !ok {
		fmt.Fprintf(w, "| %s | %s | %s | %d | no bucket to judge | | %.2e |\n", label, modNames[mod], name, n, conf)
		return
	}
	fmt.Fprintf(w, "| %s | %s | %s | %d | %.2f (%d) | %.3f (%d) | %.2e |\n", label, modNames[mod], name, n, hi, hb, lo, lb, conf)
}

// netRateAll is netRate with the bits delivered over every frame shown.
func netRateAll(s *segReport) float64 {
	return s.DeliveredAll * float64(s.Cells) * float64(s.Rate) * 0.75 / 1e6
}
