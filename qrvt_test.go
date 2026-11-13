package qrvt

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNotImplemented(t *testing.T) {
	ctx := context.Background()
	var errs []error
	_, err := Encode(ctx, EncodeOptions{})
	errs = append(errs, err)
	_, err = Decode(ctx, DecodeOptions{})
	errs = append(errs, err)
	errs = append(errs, CalibrationVideo(ctx, CalibrationOptions{}))
	_, err = AnalyzeCalibration(ctx, AnalyzeOptions{})
	errs = append(errs, err)
	_, err = Inspect(ctx, InspectOptions{})
	errs = append(errs, err)
	_, _, err = GenerateKey()
	errs = append(errs, err)
	for i, err := range errs {
		if !errors.Is(err, ErrNotImplemented) {
			t.Errorf("call %d: got %v, want ErrNotImplemented", i, err)
		}
	}
}

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
	}
	for _, c := range cases {
		if got := c.err.Error(); !strings.Contains(got, c.want) {
			t.Errorf("%T: %q does not contain %q", c.err, got, c.want)
		}
	}
}
