package calib

import (
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// design returns DESIGN.md's section n (from its heading to the next "## "), with every run of
// white space collapsed to one space, and its raw lines.
func design(t *testing.T, heading string) (flat string, lines []string) {
	t.Helper()
	b, err := os.ReadFile("../../docs/DESIGN.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	i := strings.Index(doc, "\n"+heading+"\n")
	if i < 0 {
		t.Fatalf("%q not found", heading)
	}
	j := strings.Index(doc[i+1:], "\n## ")
	if j < 0 {
		t.Fatalf("end of %q not found", heading)
	}
	sec := doc[i+1 : i+1+j]
	return strings.Join(strings.Fields(sec), " "), strings.Split(sec, "\n")
}

// find returns the submatches of re in s, failing when it does not match.
func find(t *testing.T, s, re string) []float64 {
	t.Helper()
	m := regexp.MustCompile(re).FindStringSubmatch(s)
	if m == nil {
		t.Fatalf("DESIGN.md no longer says %q", re)
	}
	var out []float64
	for _, v := range m[1:] {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			t.Fatalf("%q: %v", v, err)
		}
		out = append(out, f)
	}
	return out
}

// table returns the rows of the Markdown table whose header row starts with header, as trimmed
// cells, without the header and rule rows.
func table(t *testing.T, lines []string, header string) [][]string {
	t.Helper()
	var rows [][]string
	in := false
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, header):
			in = true
		case in && !strings.HasPrefix(l, "|"):
			return rows
		case in && !strings.HasPrefix(l, "| ---"):
			cells := strings.Split(strings.Trim(l, "|"), "|")
			for k := range cells {
				cells[k] = strings.TrimSpace(cells[k])
			}
			rows = append(rows, cells)
		}
	}
	if !in {
		t.Fatalf("table %q not found", header)
	}
	return rows
}

func near(a, b, rel float64) bool { return math.Abs(a-b) <= rel*math.Abs(b) }

// The constants of the conditions of DESIGN.md section 11 and the figures of section 5 are the
// document's, read back from it, so the two cannot diverge.
func TestConditionsMatchDesign(t *testing.T) {
	s11, _ := design(t, "## 11. Expected performance")
	if !strings.Contains(s11, "1. Camera frame rate at least twice the display rate.") || MinCameraRatio != 2 {
		t.Error("condition 1 is not twice the display rate")
	}
	v := find(t, s11, `with fill (0\.\d+) handheld and (0\.\d+) on a tripod`)
	if v[0] != FillHandheld || v[1] != FillTripod {
		t.Errorf("fill %v", v)
	}
	v = find(t, s11, "the minimum is ([0-9]+(?:\\.[0-9]+)?) for `robust` and ([0-9]+(?:\\.[0-9]+)?) for `normal`")
	if v[0] != MinPixels(ModeGray4, Robust) || v[1] != MinPixels(ModeGray4, Normal) {
		t.Errorf("four greys %v", v)
	}
	v = find(t, s11, `Planning uses ([0-9]+(?:\.[0-9]+)?) provisionally`)
	if v[0] != MinPixels(ModeGray4, Fast) {
		t.Errorf("four greys fast %v", v)
	}
	v = find(t, s11, "geometry floor: ([0-9]+(?:\\.[0-9]+)?) handheld, ([0-9]+(?:\\.[0-9]+)?) on `budget-android`, ([0-9]+(?:\\.[0-9]+)?) under 10-bit HLG")
	if v[0] != FloorHandheld || v[1] != FloorBudget || v[2] != FloorHLG {
		t.Errorf("geometry floor %v", v)
	}
	v = find(t, s11, "`normal` ([0-9]+(?:\\.[0-9]+)?) and `robust` at the floor")
	if v[0] != MinPixels(ModeBW, Normal) || MinPixels(ModeBW, Robust) > FloorBudget {
		t.Errorf("black and white %v, robust %v", v, MinPixels(ModeBW, Robust))
	}
	v = find(t, s11, "simulator minima of `robust` at most ([0-9]+(?:\\.[0-9]+)?), `normal` ([0-9]+(?:\\.[0-9]+)?) and `fast` ([0-9]+(?:\\.[0-9]+)?)")
	if v[0] != MinPixels(ModeBW, Robust) || v[2] != MinPixels(ModeBW, Fast) || v[1] > MinPixels(ModeBW, Normal) {
		t.Errorf("black and white simulator minima %v", v)
	}
	v = find(t, s11, "`q = ([0-9]+(?:\\.[0-9]+)?) x bitrate / \\(rate x cells\\)` at least ([0-9]+(?:\\.[0-9]+)?) for `fast`, ([0-9]+(?:\\.[0-9]+)?) for `normal`, ([0-9]+(?:\\.[0-9]+)?) for `robust` with HEVC; ([0-9]+(?:\\.[0-9]+)?) times those with H.264")
	if v[0] != RecorderShare || v[1] != minQ[Fast] || v[2] != minQ[Normal] || v[3] != minQ[Robust] || v[4] != H264Factor {
		t.Errorf("condition 3 %v", v)
	}
	v = find(t, s11, `\(black and white ([0-9]+(?:\.[0-9]+)?), four greys ([0-9]+(?:\.[0-9]+)?), colour ([0-9]+(?:\.[0-9]+)?)\), stays within ([0-9]+(?:\.[0-9]+)?) Mbit/s`)
	if v[0] != displayBits[ModeBW] || v[1] != displayBits[ModeGray4] || v[2] != displayBits[ModeColor] || v[3]*1e6 != MaxDisplayBitrate {
		t.Errorf("condition 4 %v", v)
	}
	if v = find(t, s11, `derated by ([0-9]+)%`); 1-v[0]/100 != Derating {
		t.Errorf("derating %v", v)
	}
	if v = find(t, s11, `At ([0-9]+)% parity`); v[0] != ParityPercent {
		t.Errorf("parity %v", v)
	}
	// Section 5's modulation table: steady figures and display bits per cell.
	_, l5 := design(t, "## 5. Modulation")
	modes := map[string]uint8{"Black and white": ModeBW, "Four greys": ModeGray4, "Eight colours": ModeColor}
	seen := 0
	for _, row := range table(t, l5, "| Mode |") {
		m, ok := modes[row[0]]
		if !ok {
			continue
		}
		seen++
		steady, err := strconv.ParseFloat(strings.Fields(row[3])[0], 64)
		if err != nil || steady != steadyPixels[m] {
			t.Errorf("%s: steady %q", row[0], row[3])
		}
		if bits, err := strconv.ParseFloat(row[4], 64); err != nil || bits != displayBits[m] {
			t.Errorf("%s: display bits %q", row[0], row[4])
		}
	}
	if seen != 3 {
		t.Errorf("%d modes in section 5's table", seen)
	}
	_, l13 := design(t, "## 13. Command line")
	s13 := strings.Join(strings.Fields(strings.Join(l13, " ")), " ")
	for _, want := range []string{"F is 4,896 for every size", "session 0x5332, inner code 2", "at least 120 x 90",
		"(12 < 30 < 60), cell size (16 < 12 < 8 < 4), mode (black and white < four greys < colour) and protection (`robust` < `normal` < `fast`)",
		"\"within one step\" is a distance of at most 1", "at least 92% of the codewords", "at least 90% of the code frames", "96% and 95%",
		"\"measured on your device\""} {
		if !strings.Contains(s13, want) {
			t.Errorf("section 13 no longer says %q", want)
		}
	}
	if TotalFrames != 4896 || Session != 0x5332 || InnerCode != 2 || MinGridX != 120 || MinGridY != 90 ||
		PassCodewords != 0.92 || PassFrames != 0.90 || math.Abs(MarginCodewords-0.96) > 1e-12 || math.Abs(MarginFrames-0.95) > 1e-12 || MeasuredLabel != "measured on your device" {
		t.Error("the calibration constants differ from section 13")
	}
}

// rowCamera returns the handheld camera of a planning row's label, with section 11's assumed
// bitrate: the 1080p30 class is budget-android's, with its geometry floor.
func rowCamera(t *testing.T, s11, label string) Camera {
	t.Helper()
	br := find(t, s11, `Assumed recording bitrates: 1080p30 at ([0-9]+(?:\.[0-9]+)?) Mbit/s HEVC or [0-9.]+ H.264; 4K30 at ([0-9]+(?:\.[0-9]+)?); 4K60 at ([0-9]+(?:\.[0-9]+)?); 4K120 at about ([0-9]+(?:\.[0-9]+)?)\.`)
	c := Camera{Fill: FillHandheld, Floor: FloorHandheld, Width: 3840}
	switch name := strings.Fields(label)[0]; name {
	case "1080p30":
		c.Width, c.FPS, c.Bitrate, c.Floor = 1920, 30, br[0]*1e6, FloorBudget
	case "4K30":
		c.FPS, c.Bitrate = 30, br[1]*1e6
	case "4K60":
		c.FPS, c.Bitrate = 60, br[2]*1e6
	case "4K120":
		c.FPS, c.Bitrate = 120, br[3]*1e6
	default:
		t.Fatalf("unknown camera %q", label)
	}
	return c
}

var resolutions = map[string]Size{"480p": {848, 480}, "720p": {1280, 720}, "1080p": {1920, 1080}, "1440p": {2560, 1440}, "4K": {3840, 2160}}
var modeWords = map[string]uint8{"black and white": ModeBW, "four greys": ModeGray4, "colour": ModeColor}
var protWords = map[string]uint8{"`robust`": Robust, "`normal`": Normal, "`fast`": Fast}

// parseRecommended reads "720p at 12 fps, black and white, `normal` (...)".
func parseRecommended(t *testing.T, s string) (Size, Setting) {
	t.Helper()
	m := regexp.MustCompile("^(\\w+) at (\\d+) fps, ([a-z ]+), (`\\w+`)").FindStringSubmatch(s)
	if m == nil {
		t.Fatalf("recommendation %q", s)
	}
	rate, _ := strconv.Atoi(m[2])
	sz, ok1 := resolutions[m[1]]
	mode, ok2 := modeWords[m[3]]
	inner, ok3 := protWords[m[4]]
	if !ok1 || !ok2 || !ok3 {
		t.Fatalf("recommendation %q", s)
	}
	return sz, Setting{Rate: rate, Cell: 4, Mode: mode, Inner: inner}
}

const kib = 1024

// The rules reproduce the four handheld planning rows of DESIGN.md section 11, read from the
// document: section 11's rule (Best) over the preset sizes at 4-pixel cells gives each 4K row's
// setting, s and planning rate. The 1080p30 row's black and white `normal` is established and
// feasible with the row's s and rate, and the strict rule's choice, four greys `robust` at the same
// density, is the one note (a) names, about 3% faster, at its stated rates. Note (b)'s native-1080p
// ladder figure is the rule's on that ladder.
func TestHandheldRowsMatchDesign(t *testing.T) {
	s11, l11 := design(t, "## 11. Expected performance")
	rows := table(t, l11, "| Camera (handheld) |")
	if len(rows) != 4 {
		t.Fatalf("%d handheld rows", len(rows))
	}
	sizes := []Size{{848, 480}, {1280, 720}, {1920, 1080}, {2560, 1440}, {3840, 2160}}
	for _, row := range rows {
		c := rowCamera(t, s11, row[0])
		sz, want := parseRecommended(t, row[1])
		s, _ := strconv.ParseFloat(row[2], 64)
		rate := find(t, row[3], `about ([0-9]+(?:\.[0-9]+)?) KB/s`)[0]
		got, ok := Best(c, sizes, []int{4})
		if !ok {
			t.Fatalf("%s: nothing feasible", row[0])
		}
		gx, gy := sz.W/4, sz.H/4
		px := c.Pixels(gx)
		if !near(px, s, 0.01) {
			t.Errorf("%s: s %.2f, the row says %.1f", row[0], px, s)
		}
		own := PlanningRate(want, gx, gy, 1) / kib
		if !near(own, rate, 0.05) {
			t.Errorf("%s: the row's setting plans %.1f KiB/s, the row says about %.0f", row[0], own, rate)
		}
		if !Established(want.Mode, want.Inner) || !c.Check(want, gx, gy, px).All() {
			t.Errorf("%s: the row's setting is not established and feasible (%v)", row[0], c.Check(want, gx, gy, px))
		}
		t.Logf("%s: rule gives %dx%d %+v, s %.2f, %.2f KiB/s; row %+v at %.2f KiB/s", row[0], got.W, got.H, got.Setting, got.Pixels, got.Rate/kib, want, own)
		if !strings.HasPrefix(row[0], "1080p30") {
			if got.Size != sz || got.Setting != want {
				t.Errorf("%s: rule gives %v %+v, the row says %v %+v", row[0], got.Size, got.Setting, sz, want)
			}
			continue
		}
		// Note (a): the strict rule picks four greys robust at the row's density, within 3%.
		a := find(t, s11, `\(a\) Four greys `+"`robust`"+` at the same density \(720p at 12 fps, s 4\.8, exactly its minimum\) is about ([0-9]+)% faster: about ([0-9]+(?:\.[0-9]+)?) against ([0-9]+(?:\.[0-9]+)?) KiB/s`)
		if got.Size != sz || got.Setting != (Setting{Rate: 12, Cell: 4, Mode: ModeGray4, Inner: Robust}) {
			t.Errorf("1080p30: rule gives %v %+v, note (a) says 720p four greys robust", got.Size, got.Setting)
		}
		if !near(got.Rate/kib, a[1], 0.005) || !near(own, a[2], 0.005) || math.Round(100*(got.Rate/kib/own-1)) != a[0] {
			t.Errorf("1080p30: %.2f against %.2f KiB/s, note (a) says %.2f against %.2f within %.0f%%", got.Rate/kib, own, a[1], a[2], a[0])
		}
		// Note (b): the native 1920 x 1080 ladder.
		b := find(t, s11, `on that ladder the rule gives 12 fps, 8-pixel cells, four greys `+"`normal`"+` \(240 x 135 grid, s ([0-9]+(?:\.[0-9]+)?), about ([0-9]+(?:\.[0-9]+)?) KiB/s\)`)
		lad, ok := Best(c, []Size{{1920, 1080}}, Cells)
		if !ok || lad.Setting != (Setting{Rate: 12, Cell: 8, Mode: ModeGray4, Inner: Normal}) || lad.GridX != 240 ||
			!near(lad.Pixels, b[0], 0.01) || !near(lad.Rate/kib, b[1], 0.005) {
			t.Errorf("1080p30 at a 1920 x 1080 ladder: %+v, s %.2f, %.2f KiB/s; note (b) says cell 8 four greys normal, s %.1f, %.1f KiB/s",
				lad.Setting, lad.Pixels, lad.Rate/kib, b[0], b[1])
		}
	}
}

// The re-check of ruling 1: with black and white `robust` at the floor, no black and white
// `robust` setting beats a 4K handheld row (preset sizes, every cell). The best one is logged.
func TestBWRobustBelowRows(t *testing.T) {
	s11, l11 := design(t, "## 11. Expected performance")
	for _, row := range table(t, l11, "| Camera (handheld) |") {
		c := rowCamera(t, s11, row[0])
		top, _ := Best(c, presets, []int{4})
		var bw Planned
		for _, sz := range presets {
			for _, cell := range Cells {
				if sz.W%cell != 0 || sz.H%cell != 0 {
					continue
				}
				gx, gy := sz.W/cell, sz.H/cell
				for _, r := range Rates {
					s := Setting{Rate: r, Cell: cell, Mode: ModeBW, Inner: Robust}
					if px := c.Pixels(gx); c.Check(s, gx, gy, px).All() {
						if rate := PlanningRate(s, gx, gy, 1); rate > bw.Rate {
							bw = Planned{Setting: s, Size: sz, GridX: gx, GridY: gy, Pixels: px, Rate: rate}
						}
					}
				}
			}
		}
		t.Logf("%s: best black and white robust %v %+v, s %.2f, %.1f KiB/s, against the row's %.1f", row[0], bw.Size, bw.Setting, bw.Pixels, bw.Rate/kib, top.Rate/kib)
		if bw.Rate >= top.Rate {
			t.Errorf("%s: black and white robust %v %+v at %.1f KiB/s beats the row's %.1f", row[0], bw.Size, bw.Setting, bw.Rate/kib, top.Rate/kib)
		}
	}
}

// The conditions, one at a time.
func TestCheck(t *testing.T) {
	c := Camera{Width: 3840, Fill: FillHandheld, FPS: 60, Bitrate: 53e6, Floor: FloorHandheld}
	s := Setting{Rate: 30, Cell: 4, Mode: ModeGray4, Inner: Normal}
	if got := c.Check(s, 480, 270, c.Pixels(480)); !got.All() {
		t.Fatalf("4K60 at 1080p30 four greys normal: %v", got)
	}
	cases := []struct {
		name  string
		c     Camera
		s     Setting
		gx    int
		gy    int
		px    float64
		false int
	}{
		{"camera at the display rate", Camera{FPS: 30, Bitrate: 53e6, Floor: 4}, s, 480, 270, 6.4, 0},
		{"too few pixels", Camera{FPS: 60, Bitrate: 53e6, Floor: 4}, s, 480, 270, 5.9, 1},
		{"floor", Camera{FPS: 60, Bitrate: 53e6, Floor: 4}, Setting{30, 4, ModeBW, Robust}, 480, 270, 3.9, 1},
		{"unknown mode", Camera{FPS: 60, Bitrate: 53e6, Floor: 4}, Setting{30, 4, 4, Normal}, 480, 270, 20, 1},
		{"recorder", Camera{FPS: 60, Bitrate: 20e6, Floor: 4}, s, 480, 270, 6.4, 2},
		{"H.264", Camera{FPS: 60, Bitrate: 30e6, H264: true, Floor: 4}, s, 480, 270, 6.4, 2},
		{"display cap", Camera{FPS: 120, Bitrate: 1e12, Floor: 4}, Setting{60, 4, ModeGray4, Normal}, 960, 540, 6.4, 3},
		{"colour cap", Camera{FPS: 120, Bitrate: 1e12, Floor: 4}, Setting{12, 4, ModeColor, Normal}, 960, 540, 6.4, 3},
	}
	for _, k := range cases {
		got := k.c.Check(k.s, k.gx, k.gy, k.px)
		if got[k.false] || got.All() {
			t.Errorf("%s: %v, want condition %d false", k.name, got, k.false+1)
		}
	}
	if !(Camera{FPS: 60, Bitrate: 40e6, H264: true, Floor: 4}).Check(s, 480, 270, 6.4)[2] {
		t.Error("H.264 at q 9.3 against 7.7 refused")
	}
}

func TestStanding(t *testing.T) {
	cases := []struct {
		mode, inner uint8
		px, floor   float64
		want        Standing
	}{
		{ModeGray4, Normal, 6, FloorHandheld, EstablishedAt},
		{ModeGray4, Normal, 5.9, FloorHandheld, TripodOnly},
		{ModeGray4, Normal, 3.4, FloorHandheld, BelowMinimum},
		{ModeGray4, Fast, 20, FloorHandheld, NotEstablished},
		{ModeColor, Normal, 20, FloorHandheld, NotEstablished},
		{ModeBW, Robust, 3.6, FloorBudget, EstablishedAt},
		{ModeBW, Robust, 3.6, FloorHandheld, TripodOnly},
		{ModeBW, Normal, 4.8, FloorHandheld, EstablishedAt},
		{ModeBW, Fast, 6.4, FloorHandheld, NotEstablished},
	}
	for _, c := range cases {
		if got := StandingOf(c.mode, c.inner, c.px, c.floor); got != c.want {
			t.Errorf("%d/%d at %.1f, floor %.1f: %d, want %d", c.mode, c.inner, c.px, c.floor, got, c.want)
		}
	}
	if Condition2(ModeBW, Robust, FloorHandheld) != 4 || Condition2(ModeBW, Robust, FloorBudget) != 3.6 || Condition2(ModeGray4, Normal, FloorHLG) != 6 {
		t.Error("condition 2 is not the larger of the minimum and the floor")
	}
}

// Section 11's net bytes per frame (its first table, four greys at 20% parity), and the edge cases.
func TestNetBytes(t *testing.T) {
	_, l11 := design(t, "## 11. Expected performance")
	for _, row := range table(t, l11, "| Preset | Data cells |") {
		sz := resolutions[strings.TrimSuffix(row[0], "p")+"p"]
		if row[0] == "4K" {
			sz = resolutions["4K"]
		}
		for k, inner := range []uint8{Fast, Normal, Robust} {
			want := find(t, row[2+k], `([0-9]+(?:\.[0-9]+)?) KB`)[0]
			if got := NetBytes(sz.W/4, sz.H/4, ModeGray4, inner) / kib; math.Abs(got-want) > 0.051 {
				t.Errorf("%s inner %d: %.2f KiB, the table says %.1f", row[0], inner, got, want)
			}
		}
	}
	for _, g := range [][2]int{{0, 10}, {10, 0}, {70000, 100}, {3000, 3000}} {
		if NetBytes(g[0], g[1], ModeGray4, Normal) != 0 {
			t.Errorf("grid %v has net bytes", g)
		}
	}
	if DisplayBitrate(10, 10, 12, 4) <= MaxDisplayBitrate {
		t.Error("a mode without a section 5 figure is within the cap")
	}
	if _, ok := Best(Camera{FPS: 10}, presets, Cells); ok {
		t.Error("a 10 fps camera has a plan")
	}
	if _, ok := Best(Camera{FPS: 30, Width: 1920, Fill: 0.8, Bitrate: 8e6, Floor: FloorBudget}, []Size{{1920, 1080}}, []int{0, 7}); ok {
		t.Error("cells 0 and 7 planned")
	}
}

// An exact tie goes to the larger margin, then the simpler mode, then the lower ladder ranks.
func TestBetter(t *testing.T) {
	a := Setting{12, 4, ModeBW, Normal}
	b := Setting{12, 4, ModeGray4, Robust}
	if !better(2, 1, a, 1, 9, b) || better(1, 9, b, 2, 1, a) {
		t.Error("rate first")
	}
	if !better(1, 2, b, 1, 1, a) {
		t.Error("margin second")
	}
	if !better(1, 1, a, 1, 1, b) || better(1, 1, b, 1, 1, a) {
		t.Error("simpler mode third")
	}
	c := Setting{30, 4, ModeBW, Normal}
	if !better(1, 1, a, 1, 1, c) || better(1, 1, a, 1, 1, a) {
		t.Error("ranks last")
	}
}
