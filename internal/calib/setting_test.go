package calib

import "testing"

// The distance of DESIGN.md section 13: the sum of rank differences over rate, cell, mode and
// protection.
func TestDistance(t *testing.T) {
	base := Setting{Rate: 12, Cell: 4, Mode: ModeBW, Inner: Normal}
	cases := []struct {
		s    Setting
		want int
	}{
		{base, 0},
		{Setting{30, 4, ModeBW, Normal}, 1},
		{Setting{60, 4, ModeBW, Normal}, 2},
		{Setting{12, 8, ModeBW, Normal}, 1},
		{Setting{12, 16, ModeBW, Normal}, 3},
		{Setting{12, 4, ModeGray4, Normal}, 1},
		{Setting{12, 4, ModeBW, Robust}, 1},
		{Setting{12, 4, ModeBW, Fast}, 1},
		{Setting{12, 8, ModeGray4, Normal}, 2},
		{Setting{60, 16, ModeColor, Robust}, 2 + 3 + 2 + 1},
		{Setting{24, 4, ModeBW, Normal}, -1},
		{Setting{12, 6, ModeBW, Normal}, -1},
		{Setting{12, 4, 4, Normal}, -1},
		{Setting{12, 4, ModeBW, 3}, -1},
	}
	for _, c := range cases {
		if got := Distance(base, c.s); got != c.want {
			t.Errorf("Distance(%+v, %+v) = %d, want %d", base, c.s, got, c.want)
		}
		if got := Distance(c.s, base); got != c.want {
			t.Errorf("Distance is not symmetric for %+v", c.s)
		}
	}
}

// The ladder runs from the slowest, most robust value to the fastest on each axis.
func TestLadder(t *testing.T) {
	r, ok := Setting{Rate: 60, Cell: 4, Mode: ModeColor, Inner: Fast}.Ranks()
	if !ok || r != [4]int{2, 3, 2, 2} {
		t.Errorf("fastest ranks %v", r)
	}
	r, ok = Setting{Rate: 12, Cell: 16, Mode: ModeBW, Inner: Robust}.Ranks()
	if !ok || r != [4]int{} {
		t.Errorf("slowest ranks %v", r)
	}
}
