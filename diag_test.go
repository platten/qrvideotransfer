package qrvt

import (
	"errors"
	"reflect"
	"testing"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/pipeline"
)

// TestShortDiagnosticsConverted: the pipeline's failure diagnostics reach NotEnoughDataError
// unchanged; a refusal the captures agree on that names an unsupported format version, modulation
// or inner code becomes UnsupportedVersionError naming the value (FORMAT.md section 14, exit 5),
// and any other agreed refusal is named in Reject.
func TestShortDiagnosticsConverted(t *testing.T) {
	h := frame.Header{Version: 1, Session: 9, TotalFrames: 8, GridX: 212, GridY: 120, Modulation: 2, InnerCode: 2}
	with := func(r frame.HeaderReason, e func(*frame.Header)) *pipeline.ShortError {
		v := h
		e(&v)
		return &pipeline.ShortError{Captures: 5, Located: 4, Reject: &frame.HeaderError{Reason: r, Header: v}}
	}
	full := &pipeline.ShortError{Blocks: []pipeline.Shortfall{{Block: 1, Have: 2, Need: 3}}, Captures: 40, Located: 38,
		HeadersRead: 37, Other: 3, FramesSeen: 7, FramesTotal: 8, Descriptor: true, LockedRate: true}
	want := &NotEnoughDataError{Blocks: []Shortfall{{Block: 1, Have: 2, Need: 3}}, Captures: 40, Located: 38, HeadersRead: 37,
		Other: 3, FramesSeen: 7, FramesTotal: 8, Descriptor: true, LockedRate: true}
	var short *NotEnoughDataError
	if err := convertError(full); !errors.As(err, &short) || !reflect.DeepEqual(short, want) {
		t.Errorf("converted to %+v, want %+v", err, want)
	}
	if got, w := short.Error(), (&NotEnoughDataError{Blocks: want.Blocks}).Error(); got != w {
		t.Errorf("message %q, want the message without diagnostics %q", got, w)
	}
	for _, c := range []struct {
		in   *pipeline.ShortError
		what string
		v    int
	}{
		{with(frame.HeaderVersion, func(h *frame.Header) { h.Version = 2 }), "format version", 2},
		{with(frame.HeaderModulation, func(h *frame.Header) { h.Modulation = 5 }), "modulation", 5},
		{with(frame.HeaderInner, func(h *frame.Header) { h.InnerCode = 7 }), "inner code", 7},
	} {
		var u *UnsupportedVersionError
		if err := convertError(c.in); !errors.As(err, &u) || *u != (UnsupportedVersionError{c.what, c.v}) {
			t.Errorf("%v converted to %v, want an unsupported %s %d", c.in.Reject, err, c.what, c.v)
		}
	}
	err := convertError(with(frame.HeaderReserved, func(h *frame.Header) { h.Reserved = 3 }))
	if !errors.As(err, &short) || short.Reject != "header reserved bits are not zero" || short.Captures != 5 || short.Located != 4 {
		t.Errorf("reserved bits converted to %+v", err)
	}
}
