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
		{Captures: 1, Rate: 12, Cell: 4, Modulation: 2, PxPerCell: 3, Sum: segSum{Delivered: 1.0}, DeliveredAll: 0.5},
		{Captures: 1, Rate: 12, Cell: 8, Modulation: 2, PxPerCell: 7, Sum: segSum{Delivered: 1.8}, DeliveredAll: 0.9},
		{Captures: 1, Rate: 30, Cell: 8, Modulation: 2, PxPerCell: 7, Sum: segSum{Delivered: 1.9}},
		{Captures: 1, Rate: 12, Cell: 8, Modulation: 1, PxPerCell: 7, Sum: segSum{Delivered: 0.9}},
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

// TestCalRatios: per bucket of at least minBucket bits, observed over stated error rate; the largest
// and smallest are reported with their buckets.
func TestCalRatios(t *testing.T) {
	var c calTable
	c.N[2], c.Errs[2], c.Stated[2] = 20000, 40, 10   // 4 times
	c.N[3], c.Errs[3], c.Stated[3] = 20000, 1, 4     // 0.25 times
	c.N[4], c.Errs[4], c.Stated[4] = 500, 100, 0.001 // too few bits
	hi, hiB, lo, loB, ok := calRatios(&c)
	if !ok || hi != 4 || hiB != 2 || lo != 0.25 || loB != 3 {
		t.Errorf("ratios %v (bucket %d), %v (bucket %d), %v", hi, hiB, lo, loB, ok)
	}
}
