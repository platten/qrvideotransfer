package channel

import (
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
	ScanOut        time.Duration // time to refresh the panel from the top row to the bottom
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

var presets = []Config{}

func ms(v float64) time.Duration {
	var z0 time.Duration
	return z0
}

// Presets returns the names of the presets of DESIGN.md section 15, in the table's order.
func Presets() []string {
	var z0 []string
	return z0
}

// Preset returns the named preset of DESIGN.md section 15.
func Preset(name string) (Config, error) {
	var z0 Config
	var z1 error
	return z0, z1
}

// Resize returns c with the camera at w x h pixels and the recorder's bitrate scaled with the pixel
// count, so the stand-in quantiser keeps its step. Quantities in camera pixels are kept, which is
// what small, fast tests want.
func (c Config) Resize(w, h int) Config {
	var z0 Config
	return z0
}

// validate reports the first parameter out of range.
func (c *Config) validate() error {
	var z0 error
	return z0
}
