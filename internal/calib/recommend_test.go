package calib

import (
	"math/rand/v2"
	"testing"
)

// cam4K60 is a handheld 4K60 HEVC camera at 53 Mbit/s.
var cam4K60 = Camera{FPS: 60, Bitrate: 53e6, Floor: FloorHandheld}

// meas is a measurement of a 1080p monitor's segment of 4-pixel cells (480 x 270) at px camera
// pixels per cell, with the share of codewords decoded and of frames seen given.
func meas(rate, cell int, mode, inner uint8, px, cw, fr float64) Measurement {
	frames := rate * SegmentSeconds
	seen := int(fr*float64(frames) + 0.5)
	tried := seen * 100
	return Measurement{Setting: Setting{Rate: rate, Cell: cell, Mode: mode, Inner: inner}, GridX: 1920 / cell, GridY: 1080 / cell,
		Pixels: px, Frames: frames, Seen: seen, Codewords: tried, Decoded: int(cw*float64(tried) + 0.5)}
}

// The highest-rate setting that passes, is established and is feasible.
func TestRecommendHighest(t *testing.T) {
	ms := []Measurement{
		meas(12, 4, ModeGray4, Normal, 6.4, 1, 1),
		meas(30, 4, ModeGray4, Normal, 6.4, 0.99, 0.98),          // the answer
		meas(30, 4, ModeGray4, Robust, 6.4, 1, 1),                // passes, slower
		meas(60, 4, ModeGray4, Normal, 6.4, 1, 1),                // condition 1: a 60 fps camera
		meas(30, 4, ModeGray4, Fast, 6.4, 0.94, 0.95),            // not established, no margin
		meas(30, 4, ModeColor, Normal, 6.4, 0.93, 1),             // not established, no margin
		meas(30, 8, ModeGray4, Normal, 12.8, 0.91, 1),            // fails the codeword threshold
		meas(30, 4, ModeBW, Normal, 6.4, 1, 0.89),                // fails the frame threshold
		meas(30, 4, ModeGray4, Normal, 0, 0, 0),                  // nothing measured
		{Setting: Setting{Rate: 24, Cell: 4, Mode: 2, Inner: 2}}, // off the ladder
	}
	r, ok := Recommend(cam4K60, ms)
	if !ok || r.Setting != (Setting{30, 4, ModeGray4, Normal}) || r.Measured || r.Standing != EstablishedAt {
		t.Fatalf("%+v", r)
	}
	if want := PlanningRate(r.Setting, 480, 270, float64(r.Decoded)/float64(r.Codewords)*float64(r.Seen)/float64(r.Frames)); r.Rate < want*0.999 || r.Rate > want*1.001 {
		t.Errorf("rate %.0f, want %.0f", r.Rate, want)
	}
	if _, ok := Recommend(cam4K60, ms[3:]); ok {
		t.Error("recommended with nothing established that passes")
	}
}

// A not-established or tripod-only setting is never recommended without a segment that passed
// with margin; with one it is, labelled measured on the user's device.
func TestRecommendMeasured(t *testing.T) {
	base := meas(12, 4, ModeGray4, Robust, 6.4, 1, 1)
	for _, m := range []Measurement{
		meas(30, 4, ModeGray4, Fast, 12, 0.95, 1),     // not established, passes without margin
		meas(30, 4, ModeColor, Normal, 12, 1, 0.94),   // not established, frames short of margin
		meas(30, 4, ModeGray4, Normal, 4.5, 0.95, 1),  // tripod-only, no margin
		meas(30, 4, ModeGray4, Normal, 3.0, 0.95, 1),  // below even the steady figure, no margin
		meas(30, 4, ModeBW, Normal, 4.5, 0.959, 0.99), // tripod-only (below 4.8), just short
	} {
		r, ok := Recommend(cam4K60, []Measurement{base, m})
		if !ok || r.Setting != base.Setting {
			t.Errorf("%+v at %.1f recommended: %+v", m.Setting, m.Pixels, r)
		}
	}
	for _, m := range []Measurement{
		meas(30, 4, ModeGray4, Fast, 12, 0.97, 0.96),
		meas(30, 4, ModeColor, Normal, 12, 1, 1),
		meas(30, 4, ModeGray4, Normal, 4.5, 0.96, 0.95),
	} {
		r, ok := Recommend(cam4K60, []Measurement{base, m})
		if !ok || r.Setting != m.Setting || !r.Measured || r.Standing == EstablishedAt {
			t.Errorf("%+v at %.1f with margin: %+v", m.Setting, m.Pixels, r)
		}
	}
}

// Condition 4: a segment over the display cap is never chosen, even when it passes with margin
// and would be the fastest: colour at 60 fps on 4-pixel cells of a 1080p monitor needs
// 28 x 129,600 x 60 = 218 Mbit/s.
func TestRecommendDisplayCap(t *testing.T) {
	cam := Camera{FPS: 120, Bitrate: 1e12, Floor: FloorHandheld}
	over := meas(60, 4, ModeColor, Normal, 12, 1, 1)
	if c := cam.Check(over.Setting, over.GridX, over.GridY, over.Pixels); !c[0] || !c[2] || c[3] {
		t.Fatalf("conditions %v: the case does not isolate condition 4", c)
	}
	base := meas(12, 4, ModeGray4, Normal, 6.4, 1, 1)
	r, ok := Recommend(cam, []Measurement{base, over})
	if !ok || r.Setting != base.Setting {
		t.Errorf("recommended %+v", r)
	}
}

// Exact ties: the larger condition-2 margin, then the simpler mode.
func TestRecommendTie(t *testing.T) {
	a := meas(12, 4, ModeBW, Normal, 6.4, 1, 1)
	b := a
	b.Pixels = 8
	if r, _ := Recommend(cam4K60, []Measurement{a, b}); r.Pixels != 8 {
		t.Errorf("tie went to %.1f pixels", r.Pixels)
	}
	if r, _ := Recommend(cam4K60, []Measurement{b, a}); r.Pixels != 8 {
		t.Errorf("tie went to %.1f pixels", r.Pixels)
	}
}

// Monotone: worse measurements never give a faster setting. Random sets of measurements over the
// 1080p monitor's ladder are made worse at random (fewer codewords decoded, fewer frames seen,
// fewer camera pixels per cell); the recommendation's rate never rises, and nothing is
// recommended from the worse set when nothing was from the better.
func TestRecommendMonotone(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	p, _ := NewPlan(1920, 1080)
	for trial := range 30 {
		var ms []Measurement
		for _, s := range p.Segments {
			for _, in := range Protections {
				if rng.IntN(3) == 0 {
					continue
				}
				px := 1920 * 0.8 / float64(s.GridX) * (0.6 + 0.6*rng.Float64())
				cw, fr := 0.85+0.15*rng.Float64(), 0.85+0.15*rng.Float64()
				m := meas(s.Rate, s.Cell, s.Mode, in, px, cw, fr)
				ms = append(ms, m)
			}
		}
		worse := make([]Measurement, len(ms))
		for i, m := range ms {
			w := m
			switch rng.IntN(4) {
			case 1:
				w.Decoded = rng.IntN(m.Decoded + 1)
			case 2:
				w.Seen = rng.IntN(m.Seen + 1)
			case 3:
				w.Pixels *= rng.Float64()
			}
			worse[i] = w
		}
		cam := Camera{FPS: []float64{30, 60, 120}[trial%3], Bitrate: 20e6 + 100e6*rng.Float64(), H264: trial%5 == 0, Floor: FloorHandheld}
		r0, ok0 := Recommend(cam, ms)
		r1, ok1 := Recommend(cam, worse)
		if ok1 && (!ok0 || r1.Rate > r0.Rate) {
			t.Fatalf("trial %d: worse measurements give %+v at %.0f B/s, better %+v at %.0f (%v)", trial, r1.Setting, r1.Rate, r0.Setting, r0.Rate, ok0)
		}
	}
}

func TestMeasurementFractions(t *testing.T) {
	var m Measurement
	if m.Passes() || m.PassesWithMargin() || m.Completeness() != 0 {
		t.Error("an empty measurement passes")
	}
	m = Measurement{Setting: Setting{12, 4, ModeGray4, Normal}, Frames: 10, Seen: 9, Codewords: 100, Decoded: 92}
	if !m.Passes() || m.PassesWithMargin() {
		t.Errorf("at the thresholds: passes %v, margin %v", m.Passes(), m.PassesWithMargin())
	}
}
