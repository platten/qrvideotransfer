package qrvt

import (
	"strings"
	"testing"
)

func TestErrorMessages(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{&NotEnoughDataError{Blocks: []Shortfall{{Block: 1, Have: 3, Need: 5}, {Block: 4, Have: 0, Need: 1}}},
			"2 blocks short by 3 shards"},
		{&UnsafeArchiveError{Path: "../x", Reason: "parent component"}, `"../x": parent component`},
		{&LimitError{Limit: "total size", Value: 10, Max: 5}, "total size 10 exceeds the limit 5"},
		{&UnsupportedVersionError{What: "modulation", Value: 7}, "unsupported modulation 7"},
		{&CalibrationError{Reason: CalibrationNoCode, Captures: 3}, "no code in any of the 3 captures"},
		{&CalibrationError{Reason: CalibrationNoHeader, Located: 2, Captures: 3}, "found in 2 of 3 captures, but no frame header"},
		{&CalibrationError{Reason: CalibrationTransfer, HeadersRead: 4}, "the 4 captures with a frame header show a transfer"},
		{&CalibrationError{Reason: CalibrationMixedSizes, Sizes: 2}, "recover to 2 video sizes"},
		{&CalibrationError{Reason: CalibrationNothingPassed, Calibration: 5}, "no step of the calibration video passed in its 5 captures"},
	}
	for _, c := range cases {
		if got := c.err.Error(); !strings.Contains(got, c.want) {
			t.Errorf("%T: %q does not contain %q", c.err, got, c.want)
		}
	}
}
