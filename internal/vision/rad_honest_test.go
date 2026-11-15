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

// underFloor is, per preset, the smallest observed error rate relative to the stated one that the
// honesty test accepts; above it the band is [stated/2, 2 stated] as DESIGN.md section 15 has it.
// On tripod clean cells are underconfident, a residual the designer accepted on 2026-11-15
// (DESIGN.md sections 10 and 15): pooled over the Gray4 variants, bucket 1 holds 24 errors where 167
// are expected (0.14 times) and bucket 2 none where 23 are expected. The floor is that 0.14 less a
// margin, so that the underconfidence cannot grow unnoticed; overconfidence keeps the bound of twice.
//
// On handheld clean cells are underconfident through the geometry pipeline (pooled bucket 1: 0.46
// times; the preset alone 0.23 times), also accepted on 2026-11-15 and revisited with S2; its floor
// is that 0.23 less a margin.
var underFloor = map[string]float64{"tripod": 0.10, "handheld": 0.15}

// overCeil is, per preset, the largest observed error rate relative to the stated one accepted for
// credited cells that hold some of another frame, and for tables that include them. Above the
// band's 2 it is a regression bound on a residual the designer accepted on 2026-11-15 and that is
// revisited with S2: through the geometry pipeline, tripod's partly mixed cells are 9.8 times the
// stated rate in pooled bucket 1 (4.6 times in bucket 2) and the whole pooled bucket 1 8.7 times.
var overCeil = map[string]float64{"tripod": 12, "handheld": 2}

// pointExpected is the expected error count from which a bucket's observed rate itself must lie
// within half and twice the stated rate.
const pointExpected = 100

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
// stated rate. Where few errors are expected only an excess of errors (overconfidence) can be
// shown. checkPooled also requires, where at least pointExpected errors are expected, the observed
// rate itself to lie in [stated/2, 2 stated]; it judges the tables pooled over the variants. One
// case alone does not meet that bound: Gray4 on tripod with gamma error +0.3 has 66 errors in
// bucket 1 where 166 are expected (0.40 times; its clean cells are underconfident), which the
// Poisson test accepts and the pooled bucket absorbs.
func (c *calTable) check() []string { return c.judge(false, 0.5, 2) }

// checkPooled is check with the point estimate also bounded, for tables pooled over the variants.
func (c *calTable) checkPooled() []string { return c.judge(true, 0.5, 2) }

// judge is check, bounding the point estimate too when point is set, with low and high (normally
// 0.5 and 2) the smallest and largest observed rates, relative to the stated one, that are honest.
func (c *calTable) judge(point bool, low, high float64) []string {
	var bad []string
	for j := range calBuckets {
		if c.n[j] < minBucket {
			continue
		}
		mu := c.stated[j]
		le, _ := poisson(c.errs[j], low*mu)
		_, ge := poisson(c.errs[j], high*mu)
		ratio := float64(c.errs[j]) / mu
		if ge < 1e-3 || le < 1e-3 || point && mu >= pointExpected && (ratio < low || ratio > high) {
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
func (s *radSession) calibrate(t testing.TB, caps []*radCapture, stripes bool) (tab, cleanTab, mixedTab *calTable, cells int) {
	t.Helper()
	tab, cleanTab, mixedTab = &calTable{}, &calTable{}, &calTable{}
	b := s.mod.BitsPerCell()
	soft := make([]int8, len(s.layout.DataCells())*b)
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
				fr, tw := credited(mix[i], p)
				if fr < 0 {
					t.Fatalf("capture %d cell %d credited to phase %d, which it never showed", rc.tr.Capture, i, p)
				}
				cells++
				for k := range b {
					tab.add(soft[i*b+k], symbolBit(s.symbols[fr][i], b, k))
					if tw < 0.999 {
						mixedTab.add(soft[i*b+k], symbolBit(s.symbols[fr][i], b, k))
					} else {
						cleanTab.add(soft[i*b+k], symbolBit(s.symbols[fr][i], b, k))
					}
				}
			}
		}
	}
	t.Logf("of which credited cells that are not wholly one frame (true weight below 0.999):\n%s", mixedTab)
	return tab, cleanTab, mixedTab, cells
}

// TestHonestConfidence is the honest-confidence assertion of DESIGN.md section 15 on `tripod` and
// `handheld`, under gamma, white-balance and vignetting changes, for Gray4, Gray2 and RGB8. The CI
// version films a 212 x 120 grid at 4 pixels per cell with a 1600 x 900 camera (6.8 and 6.0 camera
// pixels per cell), 6 captures per case (8 frames shown at 30 fps, camera 60 fps, fixed seeds): about
// 180,000 to 230,000 credited Gray4 bits per case. Every case is judged alone and then pooled over
// the variants of a modulation and preset. The long version films a 480 x 270 grid with the
// presets' own 4K cameras, 12 captures per case; the race version Gray4 without variants.
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
	mods := []uint8{2, 1, 3}
	if radRace {
		mods, variants = mods[:1], variants[:1]
	}
	for _, mod := range mods {
		s := newRadSession(t, grid[0], grid[1], pitch, mod, 8, 0x40+uint64(mod), false)
		for pi, name := range []string{"tripod", "handheld"} {
			total, totalClean, totalMixed := &calTable{}, &calTable{}, &calTable{}
			for vi, v := range variants {
				t.Run(fmt.Sprintf("%s/%s/%s", s.mod.Name(), name, v.name), func(t *testing.T) {
					c := preset(t, name, cw, ch)
					v.edit(&c)
					caps := s.shoot(t, c, time.Second/30, ncap, uint64(1000*pi+10*vi)+uint64(mod))
					tab, clean, mixed, cells := s.calibrate(t, caps, false)
					if cells == 0 {
						t.Fatal("no cell credited")
					}
					t.Logf("%d captures, %d credited cells\n%s", len(caps), cells, tab)
					for _, e := range tab.judge(false, underFloor[name], overCeil[name]) {
						t.Error(e)
					}
					total.merge(tab)
					totalMixed.merge(mixed)
					totalClean.merge(clean)
				})
			}
			// Pooled over the variants, the buckets are larger and the test stricter.
			t.Logf("%s %s, all variants:\n%s", s.mod.Name(), name, total)
			for _, e := range total.judge(true, underFloor[name], overCeil[name]) {
				t.Errorf("%s %s pooled: %s", s.mod.Name(), name, e)
			}
			// Clean cells and the credited cells that hold some of another frame (where the
			// variance widening of SoftBits acts) must each be honest on their own, so that errors
			// in opposite directions cannot cancel in the pooled table.
			t.Logf("%s %s, all variants, credited cells wholly one frame:\n%s", s.mod.Name(), name, totalClean)
			for _, e := range totalClean.judge(true, underFloor[name], 2) {
				t.Errorf("%s %s pooled, clean cells: %s", s.mod.Name(), name, e)
			}
			t.Logf("%s %s, all variants, credited cells not wholly one frame:\n%s", s.mod.Name(), name, totalMixed)
			for _, e := range totalMixed.judge(true, 0.5, overCeil[name]) {
				t.Errorf("%s %s pooled, partly mixed cells: %s", s.mod.Name(), name, e)
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
	// 40 errors where 18 are expected is consistent with twice the stated rate but not below
	// pointExpected; 400 where 180 are expected is outside the band.
	for _, k := range []struct {
		errs int
		mu   float64
		ok   bool
	}{{40, 18, true}, {400, 180, false}, {80, 180, false}, {300, 180, true}} {
		d := calTable{}
		d.n[2], d.errs[2], d.stated[2] = 20000, k.errs, k.mu
		if got := len(d.checkPooled()) == 0; got != k.ok {
			t.Errorf("%d errors where %.0f are expected: honest %v, want %v", k.errs, k.mu, got, k.ok)
		}
	}
	if !strings.Contains(c.String(), "bucket") {
		t.Fatal("no header")
	}
}
