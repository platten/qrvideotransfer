package calib

import (
	"github.com/platten/qrvideotransfer/internal/frame"
)

// The conditions of DESIGN.md section 11 (simulated thresholds). TestConditionsMatchDesign reads
// each one back from the document.

// MinCameraRatio is condition 1: the camera's frame rate is at least this many times the display
// rate.
const MinCameraRatio = 2

// Fill is the share of the camera's frame width the screen fills, handheld and on a tripod
// (condition 2).
const (
	FillHandheld = 0.80
	FillTripod   = 0.90
)

// The geometry floor of condition 2: the least camera pixels per cell at which finders, header
// and mesh are reliable under the handheld blur, on budget-android, and under the 10-bit HLG
// preset. Every mode's minimum is at least the camera's floor.
const (
	FloorHandheld = 4.0
	FloorBudget   = 3.6
	FloorHLG      = 4.4
)

// minPixels is condition 2's mode-and-code minimum under the handheld blur (Gaussian sigma 1.5
// camera pixels in linear light), before the geometry floor. Four greys: robust 4.8, normal 6
// (re-derived 2026-11-18), fast 10 provisionally (not established). Black and white: normal 4.8
// and robust at or below 3.0, so the floor binds (provisional until G1; the simulator gives
// robust at or below 3.0, normal 3.2 and fast 3.6, and S2's real Gray2 passes at 3.0, 3.6 and
// 6.4 and is inconclusive at 4.0); fast 3.6, the simulator's, not established. Colour 6, the
// upper end of section 5's 5.5-6, never re-derived under linear-light blur and not established.
var minPixels = map[[2]uint8]float64{
	{ModeGray4, Robust}: 4.8, {ModeGray4, Normal}: 6, {ModeGray4, Fast}: 10,
	{ModeBW, Robust}: 3.0, {ModeBW, Normal}: 4.8, {ModeBW, Fast}: 3.6,
	{ModeColor, Robust}: 6, {ModeColor, Normal}: 6, {ModeColor, Fast}: 6,
}

// steadyPixels is the steady (tripod, sigma 1.0) figure of section 5 by mode: simulated only;
// it establishes no setting (section 11, "On a tripod").
var steadyPixels = map[uint8]float64{ModeBW: 2.6, ModeGray4: 3.5, ModeColor: 4.3}

// established are the settings that both the simulator and S2's real recordings support handheld
// (section 11's rule): black and white and four greys, `robust` and `normal`. Every `fast` and
// every colour setting is not established.
var established = map[[2]uint8]bool{
	{ModeBW, Robust}: true, {ModeBW, Normal}: true,
	{ModeGray4, Robust}: true, {ModeGray4, Normal}: true,
}

// MinPixels is condition 2's mode-and-code minimum camera pixels per cell under the handheld blur,
// before the geometry floor; 0 for a mode or code not planned. Not-established settings have
// provisional figures.
func MinPixels(mode, inner uint8) float64 { return minPixels[[2]uint8{mode, inner}] }

// Established reports whether a mode and code are established handheld (section 11).
func Established(mode, inner uint8) bool { return established[[2]uint8{mode, inner}] }

// Condition2 is condition 2's minimum for a camera with geometry floor floor: the larger of the
// mode-and-code minimum and the floor.
func Condition2(mode, inner uint8, floor float64) float64 {
	return max(MinPixels(mode, inner), floor)
}

// Condition 3's least recorder bits per cell per code frame with HEVC, by inner code; H.264 needs
// H264Factor times as many.
var minQ = map[uint8]float64{Fast: 9, Normal: 5.5, Robust: 2.5}

// H264Factor is condition 3's factor for an H.264 recorder.
const H264Factor = 1.4

// RecorderShare is condition 3's share of the recording's bitrate that carries cells.
const RecorderShare = 0.9

// MaxDisplayBitrate is condition 4's cap (DESIGN.md section 11): the display video's bitrate a
// stock player sustains, 131 Mbit/s, that of 4K30 four greys.
const MaxDisplayBitrate = 131e6

// displayBits are the display video's bits per cell by mode (DESIGN.md section 5, simulated). A
// mode without a figure is not planned.
var displayBits = map[uint8]float64{ModeBW: 7.3, ModeGray4: 8.4, ModeColor: 28}

// DisplayBitrate is condition 4's display video bitrate, the mode's bits per cell x cells x rate;
// +Inf for a mode without a section 5 figure.
func DisplayBitrate(gridX, gridY, rate int, mode uint8) float64 {
	b, ok := displayBits[mode]
	if !ok {
		return inf
	}
	return b * float64(gridX*gridY) * float64(rate)
}

const inf = 1e308

// Derating is the planning figures' factor for everything not simulated: 40% off (section 11).
const Derating = 0.6

// ParityPercent is the parity of section 11's net bytes per frame.
const ParityPercent = 20

// Camera is what the conditions need to know of the camera and its recording.
type Camera struct {
	Width   int     // frame width in pixels (planning: camera pixels per cell = Fill x Width / GridX)
	Fill    float64 // share of the frame width the screen fills (planning only)
	FPS     float64 // frame rate
	Bitrate float64 // recording bitrate, bits per second
	H264    bool    // the recorder is H.264, not HEVC
	Floor   float64 // geometry floor: FloorHandheld, FloorBudget or FloorHLG
}

// Pixels is the planned camera pixels per cell of a grid gridX cells wide.
func (c Camera) Pixels(gridX int) float64 { return c.Fill * float64(c.Width) / float64(gridX) }

// Conditions are section 11's four conditions for one setting, in order.
type Conditions [4]bool

// All reports whether every condition holds.
func (c Conditions) All() bool { return c[0] && c[1] && c[2] && c[3] }

// Check evaluates the four conditions for setting s on a gridX x gridY grid filmed by c at px
// camera pixels per cell.
func (c Camera) Check(s Setting, gridX, gridY int, px float64) Conditions {
	cells := float64(gridX * gridY)
	need := minQ[s.Inner]
	if c.H264 {
		need *= H264Factor
	}
	q := RecorderShare * c.Bitrate / (float64(s.Rate) * cells)
	return Conditions{
		c.FPS >= MinCameraRatio*float64(s.Rate),
		px >= Condition2(s.Mode, s.Inner, c.Floor) && MinPixels(s.Mode, s.Inner) > 0,
		need > 0 && q >= need,
		DisplayBitrate(gridX, gridY, s.Rate, s.Mode) <= MaxDisplayBitrate,
	}
}

// NetBytes is section 11's net bytes per frame, (C - 1) x shard size x K / (K + M) at 20% parity,
// for a grid, mode and inner code; 0 when the frame cannot be laid out.
func NetBytes(gridX, gridY int, mode, inner uint8) float64 {
	if gridX <= 0 || gridY <= 0 || gridX > 0xffff || gridY > 0xffff {
		return 0
	}
	h := frame.Header{Version: frame.Version, TotalFrames: 4, GridX: uint16(gridX), GridY: uint16(gridY), Modulation: mode, InnerCode: inner}
	m, err := frame.NewMapping(h)
	if err != nil {
		return 0
	}
	in, _ := frame.LookupInner(inner)
	return float64(m.CodewordsPerFrame()-1) * float64(in.ShardSize) * 100 / (100 + ParityPercent)
}

// PlanningRate is section 11's planning rate in bytes per second: net bytes per frame x display
// rate x Derating, scaled by the measured completeness (1 when planning without measurements).
func PlanningRate(s Setting, gridX, gridY int, completeness float64) float64 {
	return NetBytes(gridX, gridY, s.Mode, s.Inner) * float64(s.Rate) * Derating * completeness
}

// Standing is how a setting stands at a number of camera pixels per cell.
type Standing uint8

// The standings.
const (
	BelowMinimum   Standing = iota // an established mode and code below even the steady figure
	NotEstablished                 // the mode and code are not established (section 11)
	TripodOnly                     // established, but only at the steady (tripod) figure
	EstablishedAt                  // established, and at condition 2's handheld minimum
)

// StandingOf returns how mode and inner stand at px camera pixels per cell with geometry floor
// floor.
func StandingOf(mode, inner uint8, px, floor float64) Standing {
	switch {
	case !Established(mode, inner):
		return NotEstablished
	case px >= Condition2(mode, inner, floor):
		return EstablishedAt
	case px >= steadyPixels[mode]:
		return TripodOnly
	}
	return BelowMinimum
}

// Size is a video size in pixels.
type Size struct{ W, H int }

// Planned is a setting chosen by Best, with its video size, grid and planning figures.
type Planned struct {
	Setting
	Size
	GridX, GridY int
	Pixels       float64 // camera pixels per cell
	Rate         float64 // planning rate, bytes per second
}

// margin is a setting's condition-2 margin: camera pixels per cell over the minimum.
func margin(s Setting, px, floor float64) float64 { return px / Condition2(s.Mode, s.Inner, floor) }

// better reports whether a candidate of rate ra, margin ma and setting a beats one of rate rb,
// margin mb and setting b: the strictly higher rate; at an exact tie the larger condition-2 margin,
// then the simpler mode, then the lower ladder ranks (rate, cell, protection), so the order is total.
func better(ra, ma float64, a Setting, rb, mb float64, b Setting) bool {
	if ra != rb {
		return ra > rb
	}
	if ma != mb {
		return ma > mb
	}
	x, _ := a.Ranks()
	y, _ := b.Ranks()
	for _, i := range [4]int{2, 0, 1, 3} {
		if x[i] != y[i] {
			return x[i] < y[i]
		}
	}
	return false
}

// Best applies section 11's planning rule: of every video size, cell and setting on the ladder
// that conditions 1 to 4 allow camera c handheld and that is established, the highest planning
// rate (ties as better). It reports false when none qualifies.
func Best(c Camera, sizes []Size, cells []int) (Planned, bool) {
	var best Planned
	var bestMargin float64
	found := false
	for _, sz := range sizes {
		for _, cell := range cells {
			if cell <= 0 || sz.W%cell != 0 || sz.H%cell != 0 {
				continue
			}
			gx, gy := sz.W/cell, sz.H/cell
			px := c.Pixels(gx)
			for _, r := range Rates {
				for _, m := range Modes {
					for _, in := range Protections {
						s := Setting{Rate: r, Cell: cell, Mode: m, Inner: in}
						if !Established(m, in) || !c.Check(s, gx, gy, px).All() {
							continue
						}
						rate, mg := PlanningRate(s, gx, gy, 1), margin(s, px, c.Floor)
						if !found || better(rate, mg, s, best.Rate, bestMargin, best.Setting) {
							best = Planned{Setting: s, Size: sz, GridX: gx, GridY: gy, Pixels: px, Rate: rate}
							bestMargin, found = mg, true
						}
					}
				}
			}
		}
	}
	return best, found
}
