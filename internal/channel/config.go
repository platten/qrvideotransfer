package channel

import (
	"fmt"
	"time"
)

// Config holds every parameter of the simulated camera, one group per stage of docs/DESIGN.md
// section 15. Quantities in camera pixels (blur, shake) are absolute, not relative to the frame.
type Config struct {
	Name     string
	Display  DisplayParams
	Timing   TimingParams
	Geometry GeometryParams
	Optics   OpticsParams
	Sensor   SensorParams
	Recorder RecorderParams
}

// DisplayParams describe stage 1, the screen showing the video.
type DisplayParams struct {
	PanelW, PanelH int           // panel pixels; 0 means the video's size. The player scales to fit
	GammaError     float64       // added to the BT.1886 exponent 2.4
	Black          float64       // panel black, as a share of white
	Response       time.Duration // pixel response, modelled as a linear ramp
	ScanOut        time.Duration // time to refresh the whole panel
	ScanRotation   int           // scan direction, degrees clockwise from top-to-bottom: 0, 90, 180 or 270
	FlickerAmp     float64       // backlight modulation, as a share of the mean
	FlickerHz      float64       // backlight modulation frequency
	SubPixel       bool          // draw each panel pixel as red, green and blue stripes
}

// TimingParams describe stage 2, when the camera looks.
type TimingParams struct {
	FPS        int           // camera frame rate
	Jitter     time.Duration // RMS deviation of each capture's start time
	Exposure   time.Duration // exposure of one row; must be positive
	Readout    time.Duration // time from the first row's exposure start to the last row's
	Drop       float64       // probability that a capture starts a run of dropped captures
	DropBurst  int           // captures lost per drop; 0 means 1
	SeamFreeze time.Duration // the player holds the loop's last frame this much longer
}

// GeometryParams describe stage 3, where the screen appears in the capture.
type GeometryParams struct {
	Width, Height int     // camera pixels, both even
	Fill          float64 // screen width as a share of the frame width, untilted
	Tilt          float64 // maximum tilt of the screen out of the image plane, degrees
	Roll          float64 // in-plane rotation of the screen, degrees
	Mirror        bool    // the capture is mirrored left to right (a front camera)
	Shake         float64 // hand-shake, pixels RMS of the movement between captures
	K1            float64 // radial distortion; positive is barrel
}

// OpticsParams describe stage 4, the lens.
type OpticsParams struct {
	Blur     float64 // defocus, Gaussian sigma in camera pixels
	Vignette float64 // light lost in the frame's corners, as a share
	Veil     float64 // veiling glare added everywhere, as a share of white
	Glare    float64 // specular glare patch, as a share of the code area
}

// SensorParams describe stage 5, the sensor and the phone's processing. Fringes are applied here
// rather than in stage 4 because their amplitude is in code values.
type SensorParams struct {
	Bits         int     // 8, or 10
	HLG          bool    // hybrid log-gamma tone curve and BT.2020 matrix; else gamma 1/2.4 and BT.709
	Gain         float64 // linear signal produced by the display's white
	Noise        float64 // sigma, 8-bit code values, per colour channel
	WhiteBalance float64 // red gain 1 + s, blue gain 1 - s
	Saturation   float64 // colour matrix saturation boost
	Sharpen      float64 // unsharp-mask amount on brightness
	ChromaNR     float64 // share of colour noise reduction, 0 to 1
	Fringe       float64 // interference fringe amplitude, 8-bit code values
}

// RecorderParams describe stage 6, the phone's video encoder.
type RecorderParams struct {
	Bitrate float64 // Mbit/s; 0 means lossless
}

// Constants shared by every preset (DESIGN.md section 15).
const (
	panelGamma   = 2.4  // BT.1886 exponent before GammaError
	focalScale   = 1.4  // focal length is the frame width / 1.4: about 71 degrees across
	surround     = 0.02 // linear light around the screen, as a share of white
	glarePeak    = 1.5  // linear light at a glare patch's core, as a share of white
	shakeRevert  = 0.95 // the hand-shake walk's pull back to the mean per capture
	fringeMin    = 8.0  // fringe period range, camera pixels
	fringeMax    = 32.0 //
	stepPerBPP   = 3.0  // stand-in quantiser step times bits per pixel per frame
	defaultScan  = time.Second / 60
	defaultHz    = 240.0
	maxDimension = 1 << 14 // largest camera or panel side accepted
)

var presets = []Config{
	{
		Name:     "ideal",
		Display:  DisplayParams{ScanOut: defaultScan, FlickerHz: defaultHz},
		Timing:   TimingParams{FPS: 60, Exposure: ms(1)},
		Geometry: GeometryParams{Width: 3840, Height: 2160, Fill: 0.90},
		Optics:   OpticsParams{Blur: 0.5},
		Sensor:   SensorParams{Bits: 8, Gain: 1},
	},
	{
		Name:     "tripod",
		Display:  DisplayParams{GammaError: 0.1, Black: 0.001, Response: ms(4), ScanOut: defaultScan, FlickerHz: defaultHz},
		Timing:   TimingParams{FPS: 60, Jitter: ms(0.2), Exposure: ms(4), Readout: ms(8)},
		Geometry: GeometryParams{Width: 3840, Height: 2160, Fill: 0.90, Tilt: 5, K1: 0.02},
		Optics:   OpticsParams{Blur: 1.0, Vignette: 0.05},
		Sensor:   SensorParams{Bits: 8, Gain: 0.9, Noise: 2, WhiteBalance: 0.02, Saturation: 0.1, Sharpen: 0.3, ChromaNR: 0.5},
		Recorder: RecorderParams{Bitrate: 74},
	},
	{
		Name:     "handheld",
		Display:  DisplayParams{GammaError: 0.2, Black: 0.001, Response: ms(4), ScanOut: defaultScan, FlickerAmp: 0.02, FlickerHz: defaultHz},
		Timing:   TimingParams{FPS: 60, Jitter: ms(0.5), Exposure: ms(8), Readout: ms(8), Drop: 0.005},
		Geometry: GeometryParams{Width: 3840, Height: 2160, Fill: 0.80, Tilt: 15, Shake: 3, K1: 0.04},
		Optics:   OpticsParams{Blur: 1.5, Vignette: 0.10, Veil: 0.01},
		Sensor:   SensorParams{Bits: 8, Gain: 0.9, Noise: 4, WhiteBalance: 0.05, Saturation: 0.2, Sharpen: 0.5, ChromaNR: 0.5, Fringe: 2},
		Recorder: RecorderParams{Bitrate: 74},
	},
	{
		Name:     "budget-android",
		Display:  DisplayParams{GammaError: 0.2, Black: 0.002, Response: ms(4), ScanOut: defaultScan, FlickerAmp: 0.02, FlickerHz: defaultHz},
		Timing:   TimingParams{FPS: 30, Jitter: ms(0.5), Exposure: ms(16), Readout: ms(25), Drop: 0.01},
		Geometry: GeometryParams{Width: 1920, Height: 1080, Fill: 0.80, Tilt: 15, Shake: 3, K1: 0.06},
		Optics:   OpticsParams{Blur: 1.5, Vignette: 0.15, Veil: 0.02},
		Sensor:   SensorParams{Bits: 8, Gain: 0.9, Noise: 6, WhiteBalance: 0.05, Saturation: 0.3, Sharpen: 0.8, ChromaNR: 1, Fringe: 2},
		Recorder: RecorderParams{Bitrate: 17},
	},
	{
		Name:     "iphone-hdr",
		Display:  DisplayParams{GammaError: 0.2, Black: 0.001, Response: ms(4), ScanOut: defaultScan, FlickerAmp: 0.02, FlickerHz: defaultHz},
		Timing:   TimingParams{FPS: 60, Jitter: ms(0.5), Exposure: ms(8), Readout: ms(8), Drop: 0.005},
		Geometry: GeometryParams{Width: 3840, Height: 2160, Fill: 0.80, Tilt: 15, Shake: 3, K1: 0.04},
		Optics:   OpticsParams{Blur: 1.5, Vignette: 0.10, Veil: 0.01},
		Sensor:   SensorParams{Bits: 10, HLG: true, Gain: 0.265, Noise: 4, WhiteBalance: 0.05, Saturation: 0.2, Sharpen: 0.5, ChromaNR: 0.5, Fringe: 2},
		Recorder: RecorderParams{Bitrate: 74},
	},
	{
		Name:     "harsh",
		Display:  DisplayParams{GammaError: 0.3, Black: 0.005, Response: ms(8), ScanOut: defaultScan, FlickerAmp: 0.06, FlickerHz: defaultHz},
		Timing:   TimingParams{FPS: 30, Jitter: ms(1), Exposure: ms(16), Readout: ms(25), Drop: 0.03},
		Geometry: GeometryParams{Width: 3840, Height: 2160, Fill: 0.70, Tilt: 25, Shake: 8, K1: 0.08},
		Optics:   OpticsParams{Blur: 2.0, Vignette: 0.25, Veil: 0.03, Glare: 0.02},
		Sensor:   SensorParams{Bits: 8, Gain: 0.9, Noise: 8, WhiteBalance: 0.10, Saturation: 0.3, Sharpen: 1.0, ChromaNR: 1, Fringe: 6},
		Recorder: RecorderParams{Bitrate: 32},
	},
}

func ms(v float64) time.Duration { return time.Duration(v * float64(time.Millisecond)) }

// Presets returns the names of the presets of DESIGN.md section 15, in the table's order.
func Presets() []string {
	names := make([]string, len(presets))
	for i, p := range presets {
		names[i] = p.Name
	}
	return names
}

// Preset returns the named preset of DESIGN.md section 15.
func Preset(name string) (Config, error) {
	for _, p := range presets {
		if p.Name == name {
			return p, nil
		}
	}
	return Config{}, fmt.Errorf("channel: unknown preset %q", name)
}

// Resize returns c with the camera at w x h pixels and the recorder's bitrate scaled with the pixel
// count, so the stand-in quantiser keeps its step. Quantities in camera pixels are kept, which is
// what small, fast tests want.
func (c Config) Resize(w, h int) Config {
	c.Recorder.Bitrate *= float64(w*h) / float64(c.Geometry.Width*c.Geometry.Height)
	c.Geometry.Width, c.Geometry.Height = w, h
	return c
}

// validate reports the first parameter out of range.
func (c *Config) validate() error {
	g, t, s := &c.Geometry, &c.Timing, &c.Sensor
	switch {
	case g.Width < 2 || g.Height < 2 || g.Width%2 != 0 || g.Height%2 != 0 || g.Width > maxDimension || g.Height > maxDimension:
		return fmt.Errorf("channel: camera size %dx%d must be even, from 2 to %d", g.Width, g.Height, maxDimension)
	case c.Display.PanelW < 0 || c.Display.PanelH < 0 || c.Display.PanelW > maxDimension || c.Display.PanelH > maxDimension:
		return fmt.Errorf("channel: panel size %dx%d out of range", c.Display.PanelW, c.Display.PanelH)
	case !(g.Fill > 0 && g.Fill <= 4):
		return fmt.Errorf("channel: screen fill %v out of range", g.Fill)
	case !(g.Tilt >= 0 && g.Tilt <= 60):
		return fmt.Errorf("channel: tilt %v out of range", g.Tilt)
	case !(g.K1 >= 0 && g.K1 <= 0.2):
		return fmt.Errorf("channel: k1 %v out of range", g.K1)
	case t.FPS <= 0 || t.FPS > 1000:
		return fmt.Errorf("channel: camera rate %d out of range", t.FPS)
	case t.Exposure <= 0 || t.Readout < 0 || t.Jitter < 0 || t.SeamFreeze < 0 || c.Display.Response < 0 || c.Display.ScanOut < 0:
		return fmt.Errorf("channel: exposure must be positive and other durations not negative")
	case !(t.Drop >= 0 && t.Drop < 1) || t.DropBurst < 0:
		return fmt.Errorf("channel: drop probability %v out of range", t.Drop)
	case s.Bits != 8 && s.Bits != 10:
		return fmt.Errorf("channel: bit depth %d is not 8 or 10", s.Bits)
	case !(s.Gain > 0) || !(s.Noise >= 0) || !(c.Optics.Blur >= 0) || !(c.Optics.Glare >= 0 && c.Optics.Glare < 1) ||
		!(c.Recorder.Bitrate >= 0) || !(g.Shake >= 0) || !(s.ChromaNR >= 0 && s.ChromaNR <= 1):
		return fmt.Errorf("channel: a parameter of preset %q is out of range", c.Name)
	}
	return nil
}
