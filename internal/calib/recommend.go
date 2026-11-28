package calib

// A segment passes when, decoded with one inner code, at least PassCodewords of the codewords on
// its summed frames decode and at least PassFrames of its code frames are seen. Provisional
// (designer's ruling, M4), revisited at G1. The reason: together they leave 0.92 x 0.90 = 0.83
// of a loop's shards, the share K / (K + M) = 100 / 120 that the 20% parity of section 11's rates
// needs to rebuild every block from one loop; a segment below either is not expected to decode at
// that setting without more loops.
const (
	PassCodewords = 0.92
	PassFrames    = 0.90
)

// A segment passes with margin when its shortfall is at most half of what passing allows: at
// least MarginCodewords of the codewords decode and at least MarginFrames of the code frames are
// seen. Provisional, like the pass thresholds. Only such a segment lets Recommend choose a setting
// that is not established, or established only on a tripod.
const (
	MarginCodewords = 1 - (1-PassCodewords)/2
	MarginFrames    = 1 - (1-PassFrames)/2
)

// Measurement is what calibrate read measured for one segment, decoded with one inner code.
type Measurement struct {
	Setting              // the segment's rate, cell and mode, and the inner code decoded with
	GridX, GridY int     // the segment's grid
	Pixels       float64 // camera pixels per cell measured
	Frames       int     // the segment's code frames
	Seen         int     // code frames with soft bits
	Codewords    int     // codewords on the seen frames
	Decoded      int     // of those, decoded
}

func (m Measurement) fractions() (cw, fr float64) {
	if m.Codewords <= 0 || m.Frames <= 0 {
		return 0, 0
	}
	return float64(m.Decoded) / float64(m.Codewords), float64(m.Seen) / float64(m.Frames)
}

// Passes reports whether the segment passes (PassCodewords, PassFrames).
func (m Measurement) Passes() bool {
	cw, fr := m.fractions()
	return cw >= PassCodewords && fr >= PassFrames
}

// PassesWithMargin reports whether the segment passes with margin (MarginCodewords,
// MarginFrames).
func (m Measurement) PassesWithMargin() bool {
	cw, fr := m.fractions()
	return cw >= MarginCodewords && fr >= MarginFrames
}

// Completeness is the share of the segment's codewords that decoded: codewords decoded over those
// tried, times code frames seen over the segment's.
func (m Measurement) Completeness() float64 {
	cw, fr := m.fractions()
	return min(cw, 1) * min(fr, 1)
}

// MeasuredLabel labels a recommendation that only the user's own recording supports.
const MeasuredLabel = "measured on your device"

// Recommendation is the setting Recommend chose.
type Recommendation struct {
	Measurement
	Rate     float64 // planning rate scaled by the measured completeness, bytes per second
	Standing Standing
	// Measured is set when the setting is not established handheld (Standing is not
	// EstablishedAt): only the user's segment, which passed with margin, supports it, and it is
	// shown with MeasuredLabel. It holds for this user's recommendation only.
	Measured bool
}

// Recommend chooses, from the segments measured with camera c, the highest-rate setting that
// passes and is feasible: conditions 1, 3 and 4 hold for c, and either it is established at
// condition 2's minimum for c's geometry floor, or its segment passed with margin (Measured). The
// rate is the planning rate scaled by the measured completeness, so worse measurements never give
// a faster setting; exact ties go to the larger condition-2 margin, then the simpler mode. It
// reports false when no segment qualifies.
func Recommend(c Camera, ms []Measurement) (Recommendation, bool) {
	var best Recommendation
	var bestMargin float64
	found := false
	for _, m := range ms {
		if _, ok := m.Ranks(); !ok || !m.Passes() {
			continue
		}
		cond := c.Check(m.Setting, m.GridX, m.GridY, m.Pixels)
		if !cond[0] || !cond[2] || !cond[3] {
			continue
		}
		st := StandingOf(m.Mode, m.Inner, m.Pixels, c.Floor)
		measured := st != EstablishedAt
		if measured && !m.PassesWithMargin() {
			continue
		}
		rate := PlanningRate(m.Setting, m.GridX, m.GridY, m.Completeness())
		if rate <= 0 {
			continue
		}
		mg := margin(m.Setting, m.Pixels, c.Floor)
		if !found || better(rate, mg, m.Setting, best.Rate, bestMargin, best.Setting) {
			best = Recommendation{Measurement: m, Rate: rate, Standing: st, Measured: measured}
			bestMargin, found = mg, true
		}
	}
	return best, found
}
