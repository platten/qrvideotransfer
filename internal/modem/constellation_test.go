package modem

import (
	"math"
	"math/bits"
	"strconv"
	"strings"
	"testing"

	"github.com/platten/qrvideotransfer/internal/frame"
)

var _ Modem = (*Constellation)(nil)

func mustByID(t *testing.T, id uint8) *Constellation {
	t.Helper()
	c, err := ByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestByIDUnknown(t *testing.T) {
	for _, id := range []uint8{0, 5, 6, 7, 255} {
		_, err := ByID(id)
		if err == nil || !strings.Contains(err.Error(), "modulation") ||
			!strings.Contains(err.Error(), " "+strconv.Itoa(int(id))) {
			t.Errorf("ByID(%d) error = %v, want one naming the id", id, err)
		}
	}
}

func TestRegistry(t *testing.T) {
	for _, tc := range []struct {
		id     uint8
		b, l   int
		planes PlaneMask
	}{
		{1, 1, 2, PlaneY},
		{2, 2, 4, PlaneY},
		{3, 3, 8, PlaneY | PlaneCb | PlaneCr},
		{4, 3, 8, PlaneY},
	} {
		c := mustByID(t, tc.id)
		if info, _ := frame.LookupModulation(tc.id); c.Name() != info.Name {
			t.Errorf("modulation %d: name %q", tc.id, c.Name())
		}
		if c.ID() != tc.id || c.BitsPerCell() != tc.b || c.Levels() != tc.l || c.Planes() != tc.planes {
			t.Errorf("modulation %d: id %d, b %d, L %d, planes %b", tc.id, c.ID(), c.BitsPerCell(), c.Levels(), c.Planes())
		}
	}
}

// TestMap checks the default appearance of every symbol (FORMAT.md section 7).
func TestMap(t *testing.T) {
	g := func(y uint16) Sample { return Sample{y, 128, 128, 8} }
	g10 := func(y uint16) Sample { return Sample{y, 512, 512, 10} }
	want := map[uint8][]Sample{
		1: {g(16), g(235)},
		// Gray4: symbol v shows level l with l ^ (l >> 1) = v: 0->0, 1->1, 2->3, 3->2.
		2: {g(16), g(89), g(235), g(162)},
		3: {
			{16, 128, 128, 8}, {32, 240, 118, 8}, {173, 42, 26, 8}, {188, 154, 16, 8},
			{63, 102, 240, 8}, {78, 214, 230, 8}, {219, 16, 138, 8}, {235, 128, 128, 8},
		},
		// Gray8: levels 0..7 are 64, 189, ..., 940; symbol v shows level l with l ^ (l >> 1) = v.
		4: {g10(64), g10(189), g10(439), g10(314), g10(940), g10(815), g10(565), g10(690)},
	}
	for id, w := range want {
		c := mustByID(t, id)
		for s, ws := range w {
			if got := c.Map(uint8(s)); got != ws {
				t.Errorf("modulation %d: Map(%d) = %v, want %v", id, s, got, ws)
			}
			// Bits above b are ignored.
			if got := c.Map(uint8(s) | 0xF8&^uint8(1<<c.BitsPerCell()-1)); got != ws {
				t.Errorf("modulation %d: Map(%d with high bits) = %v, want %v", id, s, got, ws)
			}
		}
	}
}

// TestLevels checks the level numbering: level and symbol are inverse, Gray modulations change one
// bit between adjacent levels, grey luminance increases with the level, RGB8 level k is value k, and
// level 0 is dark and level L-1 light.
func TestLevels(t *testing.T) {
	for id := uint8(1); id <= 4; id++ {
		c := mustByID(t, id)
		n := c.Levels()
		for l := range n {
			s := c.SymbolOf(l)
			if c.LevelOf(s) != l {
				t.Errorf("modulation %d: LevelOf(SymbolOf(%d)) = %d", id, l, c.LevelOf(s))
			}
			if c.Map(s) != c.Level(l) {
				t.Errorf("modulation %d: Map(SymbolOf(%d)) != Level(%d)", id, l, l)
			}
			if id == 3 && s != uint8(l) {
				t.Errorf("RGB8: level %d shows symbol %d", l, s)
			}
			if id != 3 && l > 0 {
				if d := bits.OnesCount8(s ^ c.SymbolOf(l-1)); id != 1 && d != 1 {
					t.Errorf("modulation %d: levels %d and %d differ in %d bits", id, l-1, l, d)
				}
				if c.Level(l).Y <= c.Level(l-1).Y {
					t.Errorf("modulation %d: luma not increasing at level %d", id, l)
				}
			}
			if y := c.Level(l).Y; y < c.Level(0).Y || y > c.Level(n-1).Y {
				t.Errorf("modulation %d: level %d luma %d outside dark..light", id, l, y)
			}
		}
	}
}

func TestDefaultStats(t *testing.T) {
	st := mustByID(t, 4).DefaultStats(9)
	if st.Levels != 8 {
		t.Fatalf("Levels = %d", st.Levels)
	}
	for l, y := range []float32{16, 47.25, 78.5, 109.75, 141.25, 172.5, 203.75, 235} {
		if st.Centre[l] != [3]float32{y, 128, 128} || st.Variance[l] != 9 {
			t.Errorf("Gray8 level %d: centre %v variance %v", l, st.Centre[l], st.Variance[l])
		}
	}
	st = mustByID(t, 3).DefaultStats(2)
	if st.Levels != 8 || st.Centre[5] != [3]float32{78, 214, 230} || st.Variance[7] != 2 {
		t.Errorf("RGB8 stats %+v", st)
	}
}

// obsOf returns the observation of a sample at its default level, on the 8-bit scale.
func obsOf(s Sample) Obs {
	k := float32(1)
	if s.Bits == 10 {
		k = 4
	}
	return Obs{Y: float32(s.Y) / k, Cb: float32(s.Cb) / k, Cr: float32(s.Cr) / k}
}

func symbolBit(s uint8, b, k int) uint8 { return s >> (b - 1 - k) & 1 }

// TestLLRIdeal: an observation exactly at a level gives large soft bits of the right sign.
func TestLLRIdeal(t *testing.T) {
	for id := uint8(1); id <= 4; id++ {
		c := mustByID(t, id)
		st := c.DefaultStats(4)
		b := c.BitsPerCell()
		dst := make([]int8, b)
		for s := range uint8(1 << b) {
			c.LLR(dst, obsOf(c.Map(s)), &st, 1)
			for k := range b {
				want := int8(127)
				if symbolBit(s, b, k) == 1 {
					want = -127
				}
				if dst[k] != want {
					t.Errorf("modulation %d symbol %d bit %d: soft bit %d, want %d", id, s, k, dst[k], want)
				}
			}
		}
	}
}

// TestLLRValue checks the scale: Gray2, levels 16 and 235 with variance 10000, observed at 16:
// the metric difference is 219^2 / (2 * 10000) = 2.39805 nats, times LLRScale 4 = 9.59, rounded to 10.
func TestLLRValue(t *testing.T) {
	c := mustByID(t, 1)
	st := c.DefaultStats(10000)
	var dst [1]int8
	c.LLR(dst[:], Obs{16, 128, 128, false}, &st, 1)
	if dst[0] != 10 {
		t.Errorf("soft bit %d, want 10", dst[0])
	}
	c.LLR(dst[:], Obs{235, 128, 128, false}, &st, 1)
	if dst[0] != -10 {
		t.Errorf("soft bit %d, want -10", dst[0])
	}
	// Chroma is ignored by a grey modulation.
	c.LLR(dst[:], Obs{16, 0, 255, false}, &st, 1)
	if dst[0] != 10 {
		t.Errorf("soft bit with odd chroma %d, want 10", dst[0])
	}
}

// TestLLRMidpoint: an observation half way between two levels gives zero for the bit that tells them
// apart and keeps the sign of a bit they share.
func TestLLRMidpoint(t *testing.T) {
	c := mustByID(t, 1)
	st := c.DefaultStats(4)
	var d1 [1]int8
	c.LLR(d1[:], Obs{Y: 125.5}, &st, 1)
	if d1[0] != 0 {
		t.Errorf("Gray2 midpoint: %d", d1[0])
	}
	c = mustByID(t, 2) // levels 1 (symbol 01) and 2 (symbol 11) at 89 and 162
	st = c.DefaultStats(25)
	var d2 [2]int8
	c.LLR(d2[:], Obs{Y: 125.5}, &st, 1)
	if d2[0] != 0 || d2[1] >= 0 {
		t.Errorf("Gray4 midpoint of levels 1 and 2: %v, want [0 negative]", d2)
	}
	c = mustByID(t, 3) // between black (000) and blue (001)
	st = c.DefaultStats(25)
	var d3 [3]int8
	c.LLR(d3[:], Obs{24, 184, 123, false}, &st, 1)
	if d3[0] <= 0 || d3[1] <= 0 || d3[2] != 0 {
		t.Errorf("RGB8 midpoint of black and blue: %v, want [positive positive 0]", d3)
	}
}

// TestLLRScaling: soft bits scale inversely with the variance and linearly with the weight.
func TestLLRScaling(t *testing.T) {
	c := mustByID(t, 2)
	obs := Obs{Y: 100}
	get := func(v, w float32) [2]int8 {
		st := c.DefaultStats(v)
		var d [2]int8
		c.LLR(d[:], obs, &st, w)
		return d
	}
	// Levels 89 (01) and 162 (11), plus 16 (00) and 235 (10). At Y = 100 and variance 400:
	// metrics (84^2, 11^2, 62^2, 135^2) / 800; bit 0: min(62^2,135^2) - min(84^2,11^2) = 3723 / 800
	// = 4.654 nats -> 18.6 -> 19; bit 1: min(11^2, 62^2) - min(84^2, 135^2) = -6935 / 800 = -8.669
	// nats -> -34.7 -> -35.
	if d := get(400, 1); d != [2]int8{19, -35} {
		t.Errorf("variance 400: %v", d)
	}
	if d := get(800, 1); d != [2]int8{9, -17} { // 9.31 -> 9, -17.3 -> -17
		t.Errorf("variance 800: %v", d)
	}
	if d := get(400, 0.5); d != [2]int8{9, -17} {
		t.Errorf("weight 0.5: %v", d)
	}
	if d := get(400, 0); d != [2]int8{0, 0} {
		t.Errorf("weight 0: %v", d)
	}
	if d := get(400, -1); d != [2]int8{0, 0} {
		t.Errorf("weight -1: %v", d)
	}
	if d := get(0.01, 1); d != [2]int8{127, -127} {
		t.Errorf("tiny variance: %v", d)
	}
	if d := get(float32(math.NaN()), 1); d != [2]int8{0, 0} && d != [2]int8{127, -127} {
		t.Errorf("NaN variance: %v", d)
	}
}

// TestLLRUnknown: saturated, mismatched or missing statistics and non-finite observations give zero.
func TestLLRUnknown(t *testing.T) {
	c := mustByID(t, 3)
	st := c.DefaultStats(4)
	d := []int8{5, 5, 5}
	c.LLR(d, Obs{16, 128, 128, true}, &st, 1)
	if d[0]|d[1]|d[2] != 0 {
		t.Errorf("saturated: %v", d)
	}
	d = []int8{5, 5, 5}
	c.LLR(d, Obs{16, 128, 128, false}, nil, 1)
	if d[0]|d[1]|d[2] != 0 {
		t.Errorf("nil stats: %v", d)
	}
	g := mustByID(t, 2).DefaultStats(4)
	d = []int8{5, 5, 5}
	c.LLR(d, Obs{16, 128, 128, false}, &g, 1)
	if d[0]|d[1]|d[2] != 0 {
		t.Errorf("wrong level count: %v", d)
	}
	d = []int8{5, 5, 5}
	c.LLR(d, Obs{float32(math.NaN()), 128, 128, false}, &st, 1)
	if d[0]|d[1]|d[2] != 0 {
		t.Errorf("NaN observation: %v", d)
	}
	d = []int8{5, 5, 5}
	c.LLR(d, Obs{16, 128, 128, false}, &st, float32(math.Inf(1)))
	if d[0] != 127 || d[1] != 127 || d[2] != 127 {
		t.Errorf("infinite weight: %v", d)
	}
}

// TestLLRNoise: on Gaussian noise, the sign of the soft bit agrees with the transmitted bit far more
// often than not, and confident soft bits are almost never wrong.
func TestLLRNoise(t *testing.T) {
	c := mustByID(t, 2)
	st := c.DefaultStats(100)
	var d [2]int8
	var x uint64 = 1
	wrong, confidentWrong, n := 0, 0, 0
	for i := range 20000 {
		s := uint8(i % 4)
		// Box-Muller from a small LCG; sigma 10.
		x = x*6364136223846793005 + 1442695040888963407
		u1 := float64(x>>11)/(1<<53) + 1e-12
		x = x*6364136223846793005 + 1442695040888963407
		u2 := float64(x>>11) / (1 << 53)
		noise := 10 * math.Sqrt(-2*math.Log(u1)) * math.Cos(2*math.Pi*u2)
		c.LLR(d[:], Obs{Y: float32(float64(c.Map(s).Y) + noise)}, &st, 1)
		for k := range 2 {
			n++
			if (d[k] < 0) != (symbolBit(s, 2, k) == 1) && d[k] != 0 {
				wrong++
				if d[k] > 40 || d[k] < -40 { // above 10 nats
					confidentWrong++
				}
			}
		}
	}
	if wrong > n/50 || confidentWrong > 0 {
		t.Errorf("%d of %d soft bits wrong, %d confidently", wrong, n, confidentWrong)
	}
}

func TestLLRNoAlloc(t *testing.T) {
	c := mustByID(t, 3)
	st := c.DefaultStats(16)
	var d [3]int8
	if a := testing.AllocsPerRun(100, func() { c.LLR(d[:], Obs{Y: 100, Cb: 120, Cr: 130}, &st, 1) }); a != 0 {
		t.Errorf("%v allocations", a)
	}
}

func BenchmarkLLR(b *testing.B) {
	for _, id := range []uint8{2, 3} {
		c, _ := ByID(id)
		st := c.DefaultStats(16)
		d := make([]int8, c.BitsPerCell())
		b.Run(c.Name(), func(b *testing.B) {
			for i := range b.N {
				c.LLR(d, Obs{Y: float32(i % 256), Cb: 120, Cr: 130}, &st, 1)
			}
		})
	}
}
