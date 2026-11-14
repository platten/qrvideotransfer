package video

import (
	"errors"
	"testing"
)

// TestOutputFormat covers the choice of raw output format.
func TestOutputFormat(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		pix  string
		bits int
		cfg  DecodeConfig
		want string
	}{
		{"yuv420p", 8, DecodeConfig{}, "yuv420p"},
		{"yuvj420p", 8, DecodeConfig{BitDepth: 8}, "yuvj420p"},
		{"yuv420p10le", 10, DecodeConfig{}, "yuv420p10le"},
		{"yuv422p10le", 10, DecodeConfig{BrightnessOnly: true}, "gray10le"},
		{"gray", 8, DecodeConfig{BrightnessOnly: true}, "gray"},
		{"yuv422p", 8, DecodeConfig{}, ""},
		{"rgb24", 8, DecodeConfig{BrightnessOnly: true}, ""},
		{"yuv420p12le", 12, DecodeConfig{BrightnessOnly: true}, ""},
		{"yuv420p", 8, DecodeConfig{BitDepth: 10}, ""},
	} {
		got, err := outputFormat(probeInfo{PixFmt: c.pix, ClipInfo: ClipInfo{BitDepth: c.bits}}, c.cfg)
		if got != c.want || (err == nil) != (c.want != "") {
			t.Errorf("%s %+v: %q %v", c.pix, c.cfg, got, err)
		}
	}
	if !FastPathBuilt {
		if _, err := newFastDecoder(t.Context(), nil, "", probeInfo{}, DecodeConfig{}); !errors.Is(err, ErrUnsupported) {
			t.Errorf("fast path stub: %v", err)
		}
	}
}
