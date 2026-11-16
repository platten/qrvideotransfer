package vision

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/channel"
	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/modem"
	"github.com/platten/qrvideotransfer/internal/video"
)

// This file measures the radiometry through the geometry stream's pipeline rather than at the
// true cell positions: FinderDetector finds the code, MeshFitter fits the mesh and measures the
// anchors, FootprintSampler samples the cells, and the palette patches are sampled through the same
// mesh. The ground truth still judges the result.

// Limits through the geometry pipeline, beside the bench's (designer's decisions of 2026-11-15):
//   - The phase weight error is a regression check at 0.12 inner and 0.19 in the pockets, against
//     the bench's 0.09 and 0.16, set by handheld scanning left to right at roll 60, where the long
//     version measured 0.1105 and 0.1724: the rectifier's anchors are less exact than the true
//     positions on that steep pose.
//   - As on the bench, only overconfidence is asserted; underconfidence is logged.
const (
	maxInnerErrPipeline  = 0.12
	maxPocketErrPipeline = 0.19
)

// pipeStats counts what the pipeline did with the captures it was given.
type pipeStats struct {
	captures, notFound, fitFailed int
}

// shootReal films the session like shoot and observes every capture through the geometry pipeline.
// Captures where no code is found or no mesh fits are counted and dropped.
func (s *radSession) shootReal(t testing.TB, c channel.Config, period time.Duration, captures int, seed uint64) ([]*radCapture, pipeStats) {
	t.Helper()
	all, st := s.shootRealAll(t, c, period, captures, seed)
	var out []*radCapture
	for _, rc := range all {
		if rc != nil {
			out = append(out, rc)
		}
	}
	return out, st
}

// paletteThrough samples the palette patches through the mesh, over the central 60% of each patch
// (patch k of strip c at 8c + k), as a decoder would.
func paletteThrough(f *video.Frame, m *Mesh, l *frame.Layout) []modem.Obs {
	v, ok := geomView(f)
	out := make([]modem.Obs, 32)
	gx, gy := float64(l.GridX()), float64(l.GridY())
	uy := gy / 135
	c0, dx, dy := 4.5*uy, gx-9*uy, 126*uy
	for cp := range 4 {
		for k := range 8 {
			if !ok {
				out[8*cp+k] = modem.Obs{Saturated: true}
				continue
			}
			r := frame.PalettePatch(cp, k)
			x0, x1 := c0+r.X0*dx, c0+r.X1*dx
			y0, y1 := c0+r.Y0*dy, c0+r.Y1*dy
			x, y := (x0+x1)/2, (y0+y1)/2
			hw, hh := 0.3*(x1-x0), 0.3*(y1-y0)
			c := gpt(m.At(x, y))
			hx := gpt(m.At(x+hw, y)).sub(gpt(m.At(x-hw, y))).mul(0.5)
			hy := gpt(m.At(x, y+hh)).sub(gpt(m.At(x, y-hh))).mul(0.5)
			out[8*cp+k], _ = v.footprint(c, hx, hy)
		}
	}
	return out
}

// TestPipelinePhase measures the frame-mixing assertions of TestPhaseWeights through the geometry
// pipeline, against the same limits: no wrong credit, a credited cell's true weight at least
// minCreditedWeight, per-preset yield at least minYield, and phase weight error within
// maxInnerErrPipeline and maxPocketErrPipeline. Gray4, 480 x 270 grid, 4K cameras; `tripod` scanning
// top to bottom and bottom to top at roll 0, `handheld` scanning right to left and left to right at
// roll 30, 2 captures each (fixed seeds); the long version runs both presets in every scan direction
// at rolls 0, 30 and 60 with 8 captures each. It also reports how often the code was not
// found or no mesh fitted. Under the race detector it runs one combination of 2 captures on a 212 x
// 120 grid with a 1600 x 900 camera, the smoke test of the whole chain through the simulator's
// goroutines, asserting none of the figures (they are asserted in the non-race step).
func TestPipelinePhase(t *testing.T) {
	t.Parallel()
	ncap := 2
	all4 := []int{0, 90, 180, 270}
	// CI: two scan directions per preset, tripod untilted in roll and handheld at roll 30.
	rots := map[string][]int{"tripod": {0, 180}, "handheld": {90, 270}}
	rolls := map[string][]float64{"tripod": {0}, "handheld": {30}}
	switch {
	case longTests():
		ncap, rots = 8, map[string][]int{"tripod": all4, "handheld": all4}
		rolls = map[string][]float64{"tripod": {0, 30, 60}, "handheld": {0, 30, 60}}
	case raceEnabled:
		rots, rolls = map[string][]int{"handheld": {90}}, map[string][]float64{"handheld": {30}}
	}
	// Under the race detector a 4K pose is too slow: the smoke run films a 212 x 120 grid with a
	// 1600 x 900 camera instead.
	gx, gy := raceSize(480, 212), raceSize(270, 120)
	cw, ch := raceSize(3840, 1600), raceSize(2160, 900)
	s := newRadSession(t, gx, gy, 4, 2, 8, 0x77, false)
	for pi, name := range []string{"tripod", "handheld"} {
		var all phaseStats
		var pipe pipeStats
		for _, rot := range rots[name] {
			for _, roll := range rolls[name] {
				t.Run(fmt.Sprintf("%s/scan%d/roll%.0f", name, rot, roll), func(t *testing.T) {
					c := preset(t, name, cw, ch)
					c.Display.ScanRotation = rot
					c.Geometry.Roll = roll
					sn, cs := math.Sincos(roll * math.Pi / 180)
					c.Geometry.Fill = math.Min(c.Geometry.Fill, 0.9*math.Min(1/(cs+sn*9/16), 9.0/16/(sn+cs*9/16)))
					caps, ps := s.shootReal(t, c, time.Second/30, ncap, uint64(100*pi+rot)+uint64(roll))
					pipe.captures, pipe.notFound, pipe.fitFailed = pipe.captures+ps.captures, pipe.notFound+ps.notFound, pipe.fitFailed+ps.fitFailed
					var st phaseStats
					for _, rc := range caps {
						pf := s.phaseOf(rc)
						st.measure(s, rc, &pf)
					}
					t.Logf("%d captures (%d without a code, %d without a mesh); cells %d (mixed %d, pocket %d); largest weight error %.4f inner, %.4f pocket; credited %d, wrong %d, smallest true weight credited %.4f",
						ps.captures, ps.notFound, ps.fitFailed, st.cells, st.mixed, st.pocket, st.maxErr, st.maxPocket, st.credited, st.wrong, st.minTrue)
					if len(caps) == 0 {
						t.Fatal("no capture went through the pipeline")
					}
					if raceEnabled {
						return // the smoke run walks the chain; its figures are asserted without race
					}
					if st.maxErr > maxInnerErrPipeline || st.maxPocket > maxPocketErrPipeline {
						t.Errorf("largest phase weight error %.4f inner, %.4f pocket, above %.2f and %.2f", st.maxErr, st.maxPocket, maxInnerErrPipeline, maxPocketErrPipeline)
					}
					if st.wrong != 0 {
						t.Errorf("%d cells credited to the wrong code frame", st.wrong)
					}
					if st.credited > 0 && st.minTrue < minCreditedWeight {
						t.Errorf("a credited cell's true weight is %.4f, below %.2f", st.minTrue, minCreditedWeight)
					}
					all.merge(&st)
				})
			}
		}
		if all.clean == 0 {
			continue
		}
		lim := minYield[name][0]
		if longTests() {
			lim = minYield[name][1]
		}
		t.Logf("%s through the pipeline: yield %.4f (%d of %d truly clean cells credited), smallest true weight credited %.4f; %d captures, %d without a code, %d without a mesh",
			name, all.yield(), all.cleanCredited, all.clean, all.minTrue, pipe.captures, pipe.notFound, pipe.fitFailed)
		if !raceEnabled && all.yield() < lim {
			t.Errorf("%s: yield %.4f below %.2f", name, all.yield(), lim)
		}
	}
}

// TestPipelineHonesty measures honest confidence (DESIGN.md section 15) through the geometry
// pipeline, judged as TestHonestConfidence judges it: each case alone, and pooled over the variants
// as a whole and split into clean and partly mixed cells. Gray4 on `tripod` and `handheld` with a
// 480 x 270 grid and the 4K cameras, the preset and gamma error +0.3, 3 captures each (fixed seeds);
// the long version adds white balance -0.10 and +0.10, gamma error -0.3 and vignetting 0.25 with 6
// captures each. Statistical: skipped under the race detector.
func TestPipelineHonesty(t *testing.T) {
	raceSkipStatistics(t)
	t.Parallel()
	ncap := 3
	type variant struct {
		name string
		edit func(*channel.Config)
	}
	variants := []variant{
		{"preset", func(*channel.Config) {}},
		{"gamma +0.3", func(c *channel.Config) { c.Display.GammaError += 0.3 }},
	}
	if longTests() {
		ncap = 6
		variants = append(variants,
			variant{"white balance -0.10", func(c *channel.Config) { c.Sensor.WhiteBalance = -0.10 }},
			variant{"gamma -0.3", func(c *channel.Config) { c.Display.GammaError -= 0.3 }},
			variant{"white balance +0.10", func(c *channel.Config) { c.Sensor.WhiteBalance = 0.10 }},
			variant{"vignetting 0.25", func(c *channel.Config) { c.Optics.Vignette = 0.25 }})
	}
	s := newRadSession(t, 480, 270, 4, 2, 8, 0x42, false)
	for pi, name := range []string{"tripod", "handheld"} {
		total, totalClean, totalMixed := &calTable{}, &calTable{}, &calTable{}
		for vi, v := range variants {
			t.Run(fmt.Sprintf("%s/%s", name, v.name), func(t *testing.T) {
				c := preset(t, name, 3840, 2160)
				v.edit(&c)
				caps, ps := s.shootReal(t, c, time.Second/30, ncap, uint64(3000+100*pi+10*vi))
				if len(caps) == 0 {
					t.Fatalf("no capture went through the pipeline (%+v)", ps)
				}
				tab, clean, mixed, cells := s.calibrate(t, caps, false)
				t.Logf("%d captures through the pipeline of %d, %d credited cells\n%s", len(caps), ps.captures, cells, tab)
				for _, e := range tab.judge(false, overCeil[name]) {
					t.Error(e)
				}
				total.merge(tab)
				totalClean.merge(clean)
				totalMixed.merge(mixed)
			})
		}
		t.Logf("%s through the pipeline, all variants:\n%sclean cells:\n%spartly mixed cells:\n%sunderconfident (logged, not asserted): all cells%s clean cells%s partly mixed cells%s",
			name, total, totalClean, totalMixed, total.under(), totalClean.under(), totalMixed.under())
		for _, e := range total.judge(true, overCeil[name]) {
			t.Errorf("%s pooled: %s", name, e)
		}
		for _, e := range totalClean.judge(true, 2) {
			t.Errorf("%s pooled, clean cells: %s", name, e)
		}
		for _, e := range totalMixed.judge(true, overCeil[name]) {
			t.Errorf("%s pooled, partly mixed cells: %s", name, e)
		}
	}
}

// TestPipelineGlare measures the glare rule through the geometry pipeline: as TestGlareUnknown, but
// the cells, anchors and palette come from the detector, mesh and sampler. Gray4 on `tripod` and
// `handheld`, 480 x 270 grid, 4K cameras, a patch over 5% of the code, 2 captures each (fixed
// seeds; 4 in the long version). A capture is used only when the code is found with and without the
// patch. The miss rate and the loss away from the patch are held to TestGlareUnknown's limits.
func TestPipelineGlare(t *testing.T) {
	raceSkipStatistics(t)
	t.Parallel()
	ncap := 2
	if longTests() {
		ncap = 4
	}
	names := []string{"tripod", "handheld"}
	s := newRadSession(t, 480, 270, 4, 2, 8, 0x63, false)
	for pi, name := range names {
		t.Run(name, func(t *testing.T) {
			c := preset(t, name, 3840, 2160)
			seed := uint64(0x900 + 10*pi)
			clean, _ := s.shootRealAll(t, c, time.Second/30, ncap, seed)
			c.Optics.Glare = 0.05
			glare, _ := s.shootRealAll(t, c, time.Second/30, ncap, seed)
			spacing := 219.0 / 3
			gx, gy := s.layout.GridX(), s.layout.GridY()
			var under, missed, away, falsely, used, quarter, quarterMissed int
			soft := make([]int8, 2*len(s.layout.DataCells()))
			for n := range glare {
				if glare[n] == nil || clean[n] == nil {
					continue
				}
				used++
				obs := append([]modem.Obs(nil), glare[n].obs...)
				f := (&TileEqualiser{Mod: s.mod}).Fit(obs, glare[n].palette, s.layout, glare[n].anchors)
				pf := s.phaseOf(glare[n])
				lit := make([]bool, gx*gy)
				for i, cl := range s.layout.DataCells() {
					lit[int(cl.Y)*gx+int(cl.X)] = glare[n].obs[i].Y-clean[n].obs[i].Y >= 1
				}
				near := func(x, y int) bool {
					const r = around + grow
					for v := max(y-r, 0); v <= min(y+r, gy-1); v++ {
						for u := max(x-r, 0); u <= min(x+r, gx-1); u++ {
							if lit[v*gx+u] {
								return true
							}
						}
					}
					return false
				}
				for p := range 4 {
					if SoftBits(soft, obs, s.layout, f, s.mod, &pf, p) == 0 {
						continue
					}
					for i, cl := range s.layout.DataCells() {
						if q, ok := pf.Phase(i); !ok || q != p || clean[n].obs[i].Saturated {
							continue
						}
						d := float64(glare[n].obs[i].Y - clean[n].obs[i].Y)
						stated := soft[2*i] != 0 || soft[2*i+1] != 0
						if d > spacing/4 {
							quarter++
							if stated {
								quarterMissed++
							}
						}
						switch {
						case d > spacing/2:
							under++
							if stated {
								missed++
							}
						case !near(int(cl.X), int(cl.Y)):
							away++
							if !stated {
								falsely++
							}
						}
					}
				}
			}
			if used == 0 {
				t.Fatal("no capture found with and without the patch")
			}
			miss, fa := float64(missed)/float64(max(under, 1)), float64(falsely)/float64(max(away, 1))
			t.Logf("%d captures used; %d credited cells under the patch, %d still stated (miss rate %.4f); at a quarter of a spacing %d of %d still stated (%.4f, logged only); %d away from it, %d made unknown (%.5f)",
				used, under, missed, miss, quarterMissed, quarter, float64(quarterMissed)/float64(max(quarter, 1)), away, falsely, fa)
			if miss > maxGlareMissPipeline {
				t.Errorf("miss rate %.4f above %.3f", miss, maxGlareMissPipeline)
			}
			if fa > maxGlareFalse {
				t.Errorf("%.5f of the cells away from the patch made unknown, above %.4f", fa, maxGlareFalse)
			}
		})
	}
}

// shootRealAll is shootReal keeping one entry per capture, nil where the pipeline dropped it, so
// that two recordings with the same seed line up.
func (s *radSession) shootRealAll(t testing.TB, c channel.Config, period time.Duration, captures int, seed uint64) ([]*radCapture, pipeStats) {
	t.Helper()
	for i, f := range s.frames {
		f.PTS = time.Duration(i) * period
	}
	in := channel.Input{Frames: s.frames, Period: time.Duration(len(s.frames)) * period, Cell: s.pitch}
	var out []*radCapture
	var st pipeStats
	var d FinderDetector
	err := channel.Simulate(context.Background(), in, c, channel.Run{Captures: captures, Seed: seed, Workers: runtime.NumCPU()},
		func(f *video.Frame, tr *channel.Truth) error {
			st.captures++
			q, ok := d.Detect(f, nil)
			if !ok {
				st.notFound++
				out = append(out, nil)
				return nil
			}
			m, anchors, err := MeshFitter{}.Fit(f, q, s.layout)
			if err != nil {
				st.fitFailed++
				out = append(out, nil)
				return nil
			}
			rc := &radCapture{f: f, tr: tr, anchors: anchors, obs: make([]modem.Obs, len(s.layout.DataCells()))}
			FootprintSampler{}.Sample(rc.obs, f, m, s.layout.DataCells())
			rc.palette = paletteThrough(f, m, s.layout)
			out = append(out, rc)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	return out, st
}

// maxMixedCost is the most codewords per noise setting that scaling the partly mixed cells' soft
// bits may recover (measured 10).
const maxMixedCost = 15

// TestPipelineMixedCost measures what the overconfident partly mixed cells cost the decoder near its
// margin (long version only). Gray4 frames carrying `normal` codewords are filmed on `tripod` with
// sensor noise raised to 14, 15 and 16, where most but not all codewords decode, and observed through
// the geometry pipeline; 4 recordings of 6 captures each (fixed seeds). Every frame a capture credits
// is decoded three ways: with the soft bits as produced, with those of credited cells holding some of
// another frame (true weight below 0.999) scaled by 0.6, and with them zeroed (zeroing also drops
// codewords below 90% of bits stated, so it is no fair comparison; it is logged only). Measured: at
// noise 14 and 15 every codeword decodes either way; at noise 16 scaling decodes 1,490 of 1,506
// against 1,480 as produced, a cost of 10 codewords (0.7%). The test bounds that cost at
// maxMixedCost per setting, a regression bound on the residual the designer accepted.
func TestPipelineMixedCost(t *testing.T) {
	if !longTests() {
		t.Skip("long test: QRVT_LONG_TESTS=1")
	}
	s := newRadSession(t, 480, 270, 4, 2, 8, 0x3c3, true)
	b := s.mod.BitsPerCell()
	for _, noise := range []float64{14, 15, 16} {
		var good [3]int
		tried := 0
		for seed := uint64(1); seed <= 4; seed++ {
			c := preset(t, "tripod", 3840, 2160)
			c.Sensor.Noise = noise
			caps, _ := s.shootReal(t, c, time.Second/15, 6, 0xc057+seed)
			soft := make([]int8, len(s.layout.DataCells())*b)
			alt := make([]int8, len(soft))
			for _, rc := range caps {
				pf := s.phaseOf(rc)
				obs := append([]modem.Obs(nil), rc.obs...)
				f := (&TileEqualiser{Mod: s.mod, Stripes: true}).Fit(obs, rc.palette, s.layout, rc.anchors)
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
					for v, scale := range []float64{1, 0.6, 0} {
						copy(alt, soft)
						for i := range mix {
							if q, ok := pf.Phase(i); !ok || q != p {
								continue
							}
							if _, tw := credited(mix[i], p); tw < 0.999 {
								for k := range b {
									alt[i*b+k] = int8(math.Round(float64(alt[i*b+k]) * scale))
								}
							}
						}
						n, g := s.decodeFrame(alt, fr)
						if v == 0 {
							tried += n
						}
						good[v] += g
					}
				}
			}
		}
		t.Logf("noise %.0f: of %d codewords tried, %d decode as produced, %d with partly mixed cells scaled by 0.6, %d with them zeroed", noise, tried, good[0], good[1], good[2])
		if good[1]-good[0] > maxMixedCost {
			t.Errorf("noise %.0f: the partly mixed cells' soft bits cost %d codewords, above %d", noise, good[1]-good[0], maxMixedCost)
		}
	}
}
