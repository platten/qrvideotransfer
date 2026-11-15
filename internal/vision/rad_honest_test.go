package vision

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/channel"
	"github.com/platten/qrvideotransfer/internal/modem"
)

// calBuckets is the number of confidence buckets of the honesty test (DESIGN.md section 15).
const calBuckets = 10

// minBucket is the number of bits a bucket needs before it is judged.
const minBucket = 10000

// calTable accumulates soft bits by stated confidence: bucket j holds |s| in
// [j * 128/10, (j+1) * 128/10).
type calTable struct {
	n, errs [calBuckets]int
	stated  [calBuckets]float64 // sum of stated error probabilities
}

// add records soft bit s against the true bit.
func (c *calTable) add(s int8, bit uint8) {
	a := int(s)
	if a < 0 {
		a = -a
	}
	j := a * calBuckets / 128
	c.n[j]++
	c.stated[j] += 1 / (1 + math.Exp(float64(a)/modem.LLRScale))
	// Positive means 0; a zero soft bit states nothing and counts as an error half the time.
	switch {
	case s == 0:
		if c.n[j]%2 == 0 {
			c.errs[j]++
		}
	case (s > 0) != (bit == 0):
		c.errs[j]++
	}
}

// merge adds another table.
func (c *calTable) merge(o *calTable) {
	for j := range calBuckets {
		c.n[j] += o.n[j]
		c.errs[j] += o.errs[j]
		c.stated[j] += o.stated[j]
	}
}

// poisson returns P(X <= k) and P(X >= k) for X Poisson with mean mu.
func poisson(k int, mu float64) (le, ge float64) {
	if mu <= 0 {
		if k == 0 {
			return 1, 1
		}
		return 1, 0
	}
	pmf := func(i int) float64 {
		lg, _ := math.Lgamma(float64(i + 1))
		return math.Exp(float64(i)*math.Log(mu) - mu - lg)
	}
	cdf := 0.0 // P(X < k)
	for i := range k {
		cdf += pmf(i)
	}
	return math.Min(1, cdf+pmf(k)), math.Max(0, 1-cdf)
}

// check judges every bucket of at least minBucket bits: its observed error count must be
// consistent, at the 0.1% level of a Poisson test, with a rate between half and twice the mean
// stated rate. Where the expected count is large this is the band [stated/2, 2 stated] itself;
// where it is small, only an excess of errors (overconfidence) can be shown.
func (c *calTable) check() []string {
	var bad []string
	for j := range calBuckets {
		if c.n[j] < minBucket {
			continue
		}
		mu := c.stated[j]
		le, _ := poisson(c.errs[j], mu/2)
		_, ge := poisson(c.errs[j], 2*mu)
		if ge < 1e-3 || le < 1e-3 {
			bad = append(bad, fmt.Sprintf("bucket %d: %d errors in %d bits, stated %.3g", j, c.errs[j], c.n[j], mu/float64(c.n[j])))
		}
	}
	return bad
}

// String formats the table.
func (c *calTable) String() string {
	var b strings.Builder
	b.WriteString("bucket |s|      bits      errors  stated rate  observed rate\n")
	for j := range calBuckets {
		if c.n[j] == 0 {
			continue
		}
		fmt.Fprintf(&b, "%d  %3d-%3d  %9d  %7d  %11.3g  %13.3g\n", j, j*128/calBuckets, (j+1)*128/calBuckets-1,
			c.n[j], c.errs[j], c.stated[j]/float64(c.n[j]), float64(c.errs[j])/float64(c.n[j]))
	}
	return b.String()
}

// calibrate runs the radiometry chain on every capture and records each credited soft bit against
// the bit its true frame carried. It returns the table and the number of credited cells.
func (s *radSession) calibrate(t testing.TB, caps []*radCapture, stripes bool) (*calTable, int) {
	t.Helper()
	tab := &calTable{}
	b := s.mod.BitsPerCell()
	soft := make([]int8, len(s.layout.DataCells())*b)
	cells := 0
	for _, rc := range caps {
		pf := s.phaseOf(rc)
		eq := &TileEqualiser{Mod: s.mod, Stripes: stripes}
		obs := append([]modem.Obs(nil), rc.obs...)
		f := eq.Fit(obs, rc.palette, s.layout, rc.anchors)
		mix := rc.trueMix(s.layout)
		for p := range 4 {
			if SoftBits(soft, obs, s.layout, f, s.mod, &pf, p) == 0 {
				continue
			}
			for i := range s.layout.DataCells() {
				if q, ok := pf.Phase(i); !ok || q != p {
					continue
				}
				fr, _ := credited(mix[i], p)
				if fr < 0 {
					t.Fatalf("capture %d cell %d credited to phase %d, which it never showed", rc.tr.Capture, i, p)
				}
				cells++
				for k := range b {
					tab.add(soft[i*b+k], symbolBit(s.symbols[fr][i], b, k))
				}
			}
		}
	}
	return tab, cells
}

// TestHonestConfidence is the honest-confidence assertion of DESIGN.md section 15 on `tripod` and
// `handheld`, under gamma, white-balance and vignetting changes, for Gray4, Gray2 and RGB8. The CI
// version films a 212 x 120 grid at 4 pixels per cell with a 1600 x 900 camera (6.8 and 6.0 camera
// pixels per cell), 6 captures per case (8 frames shown at 30 fps, camera 60 fps, fixed seeds): about
// 270,000 Gray4 bits per case. The long version films a 480 x 270 grid with the presets' own 4K
// cameras, 12 captures per case.
func TestHonestConfidence(t *testing.T) {
	grid, pitch, cw, ch, ncap := [2]int{212, 120}, 4, 1600, 900, 6
	if longTests() {
		grid, pitch, cw, ch, ncap = [2]int{480, 270}, 4, 3840, 2160, 12
	}
	type variant struct {
		name string
		edit func(*channel.Config)
	}
	variants := []variant{
		{"preset", func(*channel.Config) {}},
		{"gamma +0.3", func(c *channel.Config) { c.Display.GammaError += 0.3 }},
		{"gamma -0.3", func(c *channel.Config) { c.Display.GammaError -= 0.3 }},
		{"white balance +0.10", func(c *channel.Config) { c.Sensor.WhiteBalance = 0.10 }},
		{"white balance -0.10", func(c *channel.Config) { c.Sensor.WhiteBalance = -0.10 }},
		{"vignetting 0.25", func(c *channel.Config) { c.Optics.Vignette = 0.25 }},
	}
	for _, mod := range []uint8{2, 1, 3} {
		s := newRadSession(t, grid[0], grid[1], pitch, mod, 8, 0x40+uint64(mod), false)
		for pi, name := range []string{"tripod", "handheld"} {
			total := &calTable{}
			for vi, v := range variants {
				t.Run(fmt.Sprintf("%s/%s/%s", s.mod.Name(), name, v.name), func(t *testing.T) {
					c := preset(t, name, cw, ch)
					v.edit(&c)
					caps := s.shoot(t, c, time.Second/30, ncap, uint64(1000*pi+10*vi)+uint64(mod))
					tab, cells := s.calibrate(t, caps, false)
					if cells == 0 {
						t.Fatal("no cell credited")
					}
					t.Logf("%d captures, %d credited cells\n%s", len(caps), cells, tab)
					for _, e := range tab.check() {
						t.Error(e)
					}
					total.merge(tab)
				})
			}
			t.Logf("%s %s, all variants:\n%s", s.mod.Name(), name, total)
			for _, e := range total.check() {
				t.Errorf("%s %s pooled: %s", s.mod.Name(), name, e)
			}
		}
	}
}

// TestCalTable checks the bench's own bookkeeping.
func TestCalTable(t *testing.T) {
	var c calTable
	c.add(0, 0)
	c.add(0, 1)
	c.add(127, 0)
	c.add(-127, 0)
	c.add(-20, 1)
	if c.n[0] != 2 || c.errs[0] != 1 || c.n[9] != 2 || c.errs[9] != 1 || c.n[1] != 1 || c.errs[1] != 0 {
		t.Fatalf("table %+v", c)
	}
	if le, ge := poisson(0, 0); le != 1 || ge != 1 {
		t.Fatalf("poisson(0, 0) = %v, %v", le, ge)
	}
	if le, ge := poisson(3, 0); le != 1 || ge != 0 {
		t.Fatalf("poisson(3, 0) = %v, %v", le, ge)
	}
	if le, ge := poisson(2, 1); math.Abs(le-0.9197) > 1e-4 || math.Abs(ge-0.2642) > 1e-4 {
		t.Fatalf("poisson(2, 1) = %v, %v", le, ge)
	}
	// 20 errors in 10,000 bits stated at 0.002 is honest; 200 is not, nor is 0.
	for _, k := range []struct {
		errs int
		ok   bool
	}{{20, true}, {200, false}, {0, false}} {
		d := calTable{}
		d.n[1], d.errs[1], d.stated[1] = 10000, k.errs, 20
		if got := len(d.check()) == 0; got != k.ok {
			t.Errorf("%d errors: honest %v, want %v", k.errs, got, k.ok)
		}
	}
	if !strings.Contains(c.String(), "bucket") {
		t.Fatal("no header")
	}
}
