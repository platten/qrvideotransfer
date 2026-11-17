package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
)

// minBucket is the fewest bits a confidence bucket needs to be judged, as in the honesty tests.
const minBucket = 10000

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
		located: lo.Sum.Delivered + f*(hi.Sum.Delivered-lo.Sum.Delivered),
		all:     lo.DeliveredAll + f*(hi.DeliveredAll-lo.DeliveredAll),
		lowPx:   lo.PxPerCell, highPx: hi.PxPerCell,
	}, true
}

// calRatios returns the largest and smallest observed-over-stated error rate among the buckets of
// at least minBucket bits, with their buckets.
func calRatios(c *calTable) (hi float64, hiB int, lo float64, loB int, ok bool) {
	lo = math.Inf(1)
	for j := range calBuckets {
		if c.N[j] < minBucket || c.Stated[j] <= 0 {
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
	fmt.Fprintln(w, "| Recording | Display rate | Pixels per cell bracketing 5 | Over the located passes | Over every frame shown |")
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
					s.Captures, s.ByTime, s.CleanCapture, s.Sum.CleanFrame)
			}
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Bar 3: estimated net rate against the recording's bitrate, best segment per recording")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| Recording | Mbit/s | Rate | Cell | Mod | Delivered (located passes) | Net Mbit/s | Share | Delivered (every frame shown) | Net Mbit/s | Share |")
	fmt.Fprintln(w, "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |")
	for _, r := range recs {
		best, bestAll := -1, -1
		for i := range r.Segments {
			s := &r.Segments[i]
			if best < 0 || netRate(s) > netRate(&r.Segments[best]) {
				best = i
			}
			if bestAll < 0 || netRateAll(s) > netRateAll(&r.Segments[bestAll]) {
				bestAll = i
			}
		}
		if best < 0 {
			continue
		}
		s, a := &r.Segments[best], &r.Segments[bestAll]
		fmt.Fprintf(w, "| %s | %.1f | %d | %d | %s | %.3f | %.2f | %.1f%% | %d/%d/%s: %.3f | %.2f | %.1f%% |\n", r.Label, r.Mbps,
			s.Rate, s.Cell, modNames[s.Modulation], s.Sum.Delivered, netRate(s), 100*netRate(s)/r.Mbps,
			a.Rate, a.Cell, modNames[a.Modulation], a.DeliveredAll, netRateAll(a), 100*netRateAll(a)/r.Mbps)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Residuals: confidence, pooled per recording and modulation over the segments (buckets of at least 10,000 bits)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| Recording | Mod | Cells | Bits | Largest observed/stated (bucket) | Smallest (bucket) |")
	fmt.Fprintln(w, "| --- | --- | --- | --- | --- | --- |")
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
	fmt.Fprintln(w, "Residuals: wrong credit (a credited cell's wrong symbol that is exactly a neighbouring frame's), yield and glare")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| Recording | Credited cells | Wrong | Matching a neighbour | Mean clean share per capture | Largest glare share |")
	fmt.Fprintln(w, "| --- | --- | --- | --- | --- | --- |")
	for _, r := range recs {
		var cred, wrong, nb, caps int
		var clean, glare float64
		for _, s := range r.Segments {
			cred, wrong, nb = cred+s.Credited, wrong+s.WrongCells, nb+s.Neighbour
			clean += s.CleanCapture * float64(s.Captures)
			caps += s.Captures
			glare = math.Max(glare, s.Glare)
		}
		if caps > 0 {
			clean /= float64(caps)
		}
		fmt.Fprintf(w, "| %s | %d | %d | %d | %.3f | %.4f |\n", r.Label, cred, wrong, nb, clean, glare)
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
	if !ok {
		fmt.Fprintf(w, "| %s | %s | %s | %d | no bucket of 10,000 bits | |\n", label, modNames[mod], name, n)
		return
	}
	fmt.Fprintf(w, "| %s | %s | %s | %d | %.2f (%d) | %.3f (%d) |\n", label, modNames[mod], name, n, hi, hb, lo, lb)
}

// netRateAll is netRate with the bits delivered over every frame shown.
func netRateAll(s *segReport) float64 {
	return s.DeliveredAll * float64(s.Cells) * float64(s.Rate) * 0.75 / 1e6
}
