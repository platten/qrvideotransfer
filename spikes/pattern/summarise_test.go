package main

import (
	"math"
	"testing"
)

// TestAtPixels: delivered bits per cell at a given number of camera pixels per cell are interpolated
// linearly between the two cell sizes that bracket it at the same display rate and modulation; with
// no bracket there is no figure.
func TestAtPixels(t *testing.T) {
	segs := []segReport{
		{Captures: 1, Rate: 12, Cell: 4, Modulation: 2, PxPerCell: 3, DeliveredLocated: 1.0, DeliveredAll: 0.5},
		{Captures: 1, Rate: 12, Cell: 8, Modulation: 2, PxPerCell: 7, DeliveredLocated: 1.8, DeliveredAll: 0.9},
		{Captures: 1, Rate: 30, Cell: 8, Modulation: 2, PxPerCell: 7, DeliveredLocated: 1.9},
		{Captures: 1, Rate: 12, Cell: 8, Modulation: 1, PxPerCell: 7, DeliveredLocated: 0.9},
	}
	got, ok := atPixels(segs, 12, 2, 5)
	if !ok || math.Abs(got.located-1.4) > 1e-12 || math.Abs(got.all-0.7) > 1e-12 {
		t.Errorf("12 fps Gray4 at 5 pixels: %+v %v, want 1.4 and 0.7", got, ok)
	}
	if _, ok := atPixels(segs, 30, 2, 5); ok {
		t.Error("30 fps Gray4 has no bracket around 5 pixels but gave a figure")
	}
	// A segment no capture located has no pixel figure and brackets nothing.
	segs[0].Captures, segs[1].Captures = 0, 5
	segs[0].PxPerCell = 0
	if _, ok := atPixels(segs, 12, 2, 5); ok {
		t.Error("an unlocated segment bracketed 5 pixels")
	}
}

// TestCalRatios: per bucket of at least minBucket bits where at least minExpected errors are stated
// or observed, observed over stated error rate; the largest and smallest are reported with their
// buckets. A bucket stating few errors but showing many is therefore not hidden. The two most
// confident buckets, where almost no error is stated, are reported apart as an error rate.
func TestCalRatios(t *testing.T) {
	var c calTable
	c.N[2], c.Errs[2], c.Stated[2] = 20000, 40, 10    // 4 times
	c.N[3], c.Errs[3], c.Stated[3] = 20000, 10, 40    // 0.25 times
	c.N[4], c.Errs[4], c.Stated[4] = 500, 100, 20     // too few bits
	c.N[5], c.Errs[5], c.Stated[5] = 30000, 1800, 2   // few stated, many observed: 900 times
	c.N[6], c.Errs[6], c.Stated[6] = 30000, 3, 0.5    // few of either: no ratio
	c.N[8], c.Errs[8], c.Stated[8] = 50000, 30, 1e-6  // the confident buckets: an error rate only
	c.N[9], c.Errs[9], c.Stated[9] = 150000, 2, 1e-12 // likewise
	hi, hiB, lo, loB, ok := calRatios(&c)
	if !ok || hi != 900 || hiB != 5 || lo != 0.25 || loB != 3 {
		t.Errorf("ratios %v (bucket %d), %v (bucket %d), %v", hi, hiB, lo, loB, ok)
	}
	if got := confidentErrors(&c); math.Abs(got-32.0/200000) > 1e-15 {
		t.Errorf("error rate in buckets 8 and 9: %g, want %g", got, 32.0/200000)
	}
}

// TestNeighbourChance: the chance that a wrong b-bit symbol equals either neighbouring frame's
// symbol, each uniform and independent: 1 - (1 - 2^-b)^2.
func TestNeighbourChance(t *testing.T) {
	for b, want := range map[int]float64{1: 0.75, 2: 0.4375, 3: 1 - 49.0/64} {
		if got := neighbourChance(b); math.Abs(got-want) > 1e-15 {
			t.Errorf("b = %d: %v, want %v", b, got, want)
		}
	}
}

// TestBestSegment: the best segment by a figure, among those with at least the given located share.
func TestBestSegment(t *testing.T) {
	segs := []segReport{
		{Rate: 12, Cells: 1000, DeliveredLocated: 2.0, Located: 0.02}, // the best figure, but 2% located
		{Rate: 12, Cells: 1000, DeliveredLocated: 1.5, Located: 0.9},
		{Rate: 12, Cells: 1000, DeliveredLocated: 1.0, Located: 1},
	}
	if i := bestSegment(segs, netRate, 0.5); i != 1 {
		t.Errorf("best with half located: %d, want 1", i)
	}
	if i := bestSegment(segs, netRate, 0); i != 0 {
		t.Errorf("best with no floor: %d, want 0", i)
	}
	if i := bestSegment(segs[:1], netRate, 0.5); i != -1 {
		t.Errorf("no segment qualifies: %d, want -1", i)
	}
}
