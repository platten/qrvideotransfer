package vision

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/channel"
)

// The raw bit error rate's response to blur, noise and the recorder quantiser (DESIGN.md section
// 15, "Blur, noise, recorder quantiser"). Each sweep varies one parameter of a preset, from worse to
// better, and measures the raw bit error rate of the credited soft bits at true positions (the
// radiometry bench: render, simulator, the bench's sampler, phase field, equaliser and SoftBits).
// Four greys only: black and white has 0 or 1 errors at most points of these sweeps, too few for a
// rate to be compared.
//
// Two properties are asserted. The rate never rises as a parameter improves by more than counting
// noise: between any two points of a sweep, the better one's rate may exceed the worse one's by at
// most twice the standard deviation of the difference, taking each error count as Poisson. And
// every point stays within rawBERTolerance of the reference stored in testdata/rawber.json at the
// M3 exit (2026-11-19). Both are asserted in the long version only (TestRawBERSweeps, 4 captures a
// point); the always-on TestRawBERReference is a regression check of one point at one capture
// against its own stored count, and asserts no monotonicity. The references are integer counts;
// the 10% band also covers the few bits by which a platform whose compiler fuses multiply-adds
// (arm64) may differ from amd64 in the equaliser's floating point: every stored point has at least
// 64 errors, so a difference of a few bits is a few percent at most.

// rawBERTolerance is the largest relative difference from a stored rate.
const rawBERTolerance = 0.10

// rawBERFile holds the references, relative to the package.
const rawBERFile = "testdata/rawber.json"

// berPoint is one point of a sweep: errors among the credited bits.
type berPoint struct {
	Preset string  `json:"preset"`
	Param  string  `json:"param"` // blur (sigma, camera pixels), noise (sigma, 8-bit codes) or step (stand-in quantiser step, 8-bit codes; 0 lossless)
	Value  float64 `json:"value"`
	Caps   int     `json:"captures"`
	Errors int     `json:"errors"`
	Bits   int     `json:"bits"`
}

func (p berPoint) rate() float64 { return float64(p.Errors) / float64(max(p.Bits, 1)) }

func (p berPoint) key() string {
	return fmt.Sprintf("%s/%s=%g/%d captures", p.Preset, p.Param, p.Value, p.Caps)
}

// berSweep is one parameter's values, worst first.
type berSweep struct {
	param string
	vals  []float64
	set   func(*channel.Config, float64)
}

// berSweeps are the sweeps of the assertion, each from the worse end to the better.
var berSweeps = []berSweep{
	{"blur", []float64{3, 2.5, 2, 1.5, 1, 0.5}, func(c *channel.Config, v float64) { c.Optics.Blur = v }},
	{"noise", []float64{16, 12, 8, 4, 2, 0}, func(c *channel.Config, v float64) { c.Sensor.Noise = v }},
	{"step", []float64{60, 40, 30, 20, 10, 0}, func(c *channel.Config, v float64) {
		c.Recorder.Bitrate = 0 // lossless
		if v > 0 {
			// The stand-in's step is 3 / (bits per camera pixel per frame), channel.QuantiserStep.
			c.Recorder.Bitrate = 3 * float64(c.Geometry.Width*c.Geometry.Height*c.Timing.FPS) / (v * 1e6)
		}
	}},
}

// berSession is the bench session of the sweeps: a 212 x 120 four-grey grid at 4 pixels per cell,
// 8 random frames shown at 30 fps.
func berSession(t testing.TB) *radSession { return newRadSession(t, 212, 120, 4, 2, 8, 0x92, false) }

// measureBER films the session with preset name at 1600 x 900, edited by set, for n captures at a
// fixed seed and counts the errors of the credited bits.
func measureBER(t *testing.T, s *radSession, name, param string, v float64, set func(*channel.Config, float64), n int) berPoint {
	t.Helper()
	c := preset(t, name, 1600, 900)
	set(&c, v)
	caps := s.shoot(t, c, time.Second/30, n, 0xb2)
	tab, _, _, _ := s.calibrate(t, caps, false)
	p := berPoint{Preset: name, Param: param, Value: v, Caps: n}
	for j := range calBuckets {
		p.Bits += tab.n[j]
		p.Errors += tab.errs[j]
	}
	if p.Bits == 0 {
		t.Fatalf("%s: no bit credited", p.key())
	}
	return p
}

// loadBER reads the stored references by key.
func loadBER(t *testing.T) map[string]berPoint {
	t.Helper()
	b, err := os.ReadFile(rawBERFile)
	if err != nil {
		t.Fatalf("references: %v", err)
	}
	var ps []berPoint
	if err := json.Unmarshal(b, &ps); err != nil {
		t.Fatalf("references: %v", err)
	}
	m := map[string]berPoint{}
	for _, p := range ps {
		m[p.key()] = p
	}
	return m
}

// checkStored compares a measured point with its reference.
func checkStored(t *testing.T, ref map[string]berPoint, p berPoint) {
	t.Helper()
	r, ok := ref[p.key()]
	if !ok {
		t.Errorf("%s: no stored reference", p.key())
		return
	}
	if d := math.Abs(p.rate()-r.rate()) / r.rate(); !(d <= rawBERTolerance) {
		t.Errorf("%s: raw bit error rate %.5f (%d of %d), stored %.5f (%d of %d): %.1f%% off, above %.0f%%",
			p.key(), p.rate(), p.Errors, p.Bits, r.rate(), r.Errors, r.Bits, 100*d, 100*rawBERTolerance)
	}
}

// riseBeyondNoise reports whether better's rate exceeds worse's by more than twice the standard
// deviation of the difference of two Poisson counts' rates.
func riseBeyondNoise(worse, better berPoint) bool {
	nw, nb := float64(worse.Bits), float64(better.Bits)
	sd := math.Sqrt(float64(worse.Errors)/(nw*nw) + float64(better.Errors)/(nb*nb))
	return better.rate()-worse.rate() > 2*sd
}

// TestRawBERSweeps is the long version of the assertion: every sweep on `tripod` and `handheld`,
// monotonic within counting noise and within rawBERTolerance of the references. With
// QRVT_UPDATE_RAWBER=1 it writes the references instead (testdata/README.md). Long only
// (QRVT_LONG_TESTS=1): 36 points, about 20 s pinned to 4 CPUs.
func TestRawBERSweeps(t *testing.T) {
	raceSkipStatistics(t)
	update := os.Getenv("QRVT_UPDATE_RAWBER") == "1"
	if !longTests() && !update {
		t.Skip("long test: QRVT_LONG_TESTS=1")
	}
	var ref map[string]berPoint
	if !update {
		ref = loadBER(t)
	}
	s := berSession(t)
	var all []berPoint
	for _, name := range []string{"tripod", "handheld"} {
		for _, sw := range berSweeps {
			t.Run(name+"/"+sw.param, func(t *testing.T) {
				var pts []berPoint
				for _, v := range sw.vals {
					p := measureBER(t, s, name, sw.param, v, sw.set, 4)
					t.Logf("%s: %d errors in %d bits, %.5f", p.key(), p.Errors, p.Bits, p.rate())
					pts = append(pts, p)
					if !update {
						checkStored(t, ref, p)
					}
				}
				for i := range pts {
					for j := i + 1; j < len(pts); j++ {
						if riseBeyondNoise(pts[i], pts[j]) {
							t.Errorf("%s: rate %.5f rises to %.5f at %s, beyond counting noise", pts[i].key(), pts[i].rate(), pts[j].rate(), pts[j].key())
						}
					}
				}
				all = append(all, pts...)
			})
		}
	}
	if update {
		all = append(all, measureBER(t, s, "tripod", berSweeps[0].param, 1, berSweeps[0].set, 1))
		b, err := json.MarshalIndent(all, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.FromSlash(rawBERFile), append(b, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRawBERReference is the always-on regression check: `tripod`'s own parameters (the blur
// sweep's point at 1.0) filmed for one capture, within rawBERTolerance of its own stored one-capture
// count. It asserts no monotonicity; that is the long version's (TestRawBERSweeps). About 0.2 s
// pinned to 4 CPUs.
func TestRawBERReference(t *testing.T) {
	raceSkipStatistics(t)
	sw := berSweeps[0]
	checkStored(t, loadBER(t), measureBER(t, berSession(t), "tripod", sw.param, 1, sw.set, 1))
}

// TestRiseBeyondNoise checks the monotonicity rule on known counts.
func TestRiseBeyondNoise(t *testing.T) {
	w := berPoint{Errors: 8170, Bits: 118124}
	if riseBeyondNoise(w, berPoint{Errors: 8231, Bits: 118390}) {
		t.Error("a rise of 61 errors in 8,170 is counting noise")
	}
	if !riseBeyondNoise(w, berPoint{Errors: 8500, Bits: 118124}) {
		t.Error("a rise of 330 errors in 8,170 is not counting noise")
	}
	if riseBeyondNoise(w, berPoint{Errors: 100, Bits: 118124}) {
		t.Error("a fall is never a rise")
	}
}
