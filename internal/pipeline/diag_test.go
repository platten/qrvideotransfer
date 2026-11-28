package pipeline

import (
	"errors"
	"reflect"
	"testing"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/prng"
	"github.com/platten/qrvideotransfer/internal/video"
)

// diagRun decodes a recording of len(rs) blank captures whose vision results processCapture
// replaces by rs, in order, and returns the ShortError the decode must end in.
func diagRun(t *testing.T, rs []captureResult) *ShortError {
	t.Helper()
	f := &video.Frame{W: 32, H: 32, Bits: 8, Y: make([]uint16, 32*32), Cb: make([]uint16, 16*16), Cr: make([]uint16, 16*16)}
	frames := make([]*video.Frame, len(rs))
	for i := range frames {
		frames[i] = f
	}
	rec := writeRaw(t, frames, 30)
	defer func() { processCapture = (*captureWorker).process }()
	processCapture = func(_ *captureWorker, _ *video.Frame, i int) captureResult {
		r := rs[i]
		r.index = i
		return r
	}
	_, dr, err := decodeTo(t, rec)
	var short *ShortError
	if !errors.As(err, &short) {
		t.Fatalf("got %v, %+v; want a ShortError", err, dr)
	}
	return short
}

// refused returns a capture whose finders were found and whose header was refused for reason r, the
// refused header being the fixture's with e applied.
func refused(key params, r frame.HeaderReason, e func(*frame.Header)) captureResult {
	h := key.header()
	if e != nil {
		e(&h)
	}
	if r == frame.HeaderCRC {
		h = frame.Header{}
	}
	return captureResult{finders: true, reject: frame.HeaderError{Reason: r, Header: h}}
}

// TestShortDiagnostics: each failure class of DESIGN.md section 13 ends in a ShortError whose
// counts say what the recordings showed, with processCapture stubbed. A refusal is agreed only when
// at least agreeCaptures captures give the same reason and value, at least half of the captures
// with finders, with at least two frame indices among them, and no header was read.
func TestShortDiagnostics(t *testing.T) {
	key, truth := fixture(t)
	f := int(key.frames)
	g := prng.SplitMix64{State: 7}
	weak := func(fr uint32) []phaseBits { // soft bits far too weak to decode anything
		s := make([]int8, len(truth[0]))
		for i := range s {
			s[i] = int8(g.Next()%3) - 1
		}
		return []phaseBits{{frame: fr, soft: s}}
	}
	located := func(k params, fr uint32, ph []phaseBits) captureResult {
		return captureResult{located: true, finders: true, key: k, first: fr, cells: 100, clean: 100, phases: ph}
	}
	other := key
	other.session ^= 1
	v := func(n uint8) func(*frame.Header) { return func(h *frame.Header) { h.Version = n } }
	v2, crc := refused(key, frame.HeaderVersion, v(2)), refused(key, frame.HeaderCRC, nil)
	mod5 := refused(key, frame.HeaderModulation, func(h *frame.Header) { h.Modulation = 5 })
	// rep repeats r n times; a refused header's frame index counts up, as a playing video's does.
	rep := func(r captureResult, n int) []captureResult {
		out := make([]captureResult, n)
		for i := range out {
			out[i] = r
			out[i].reject.Header.FrameIndex += uint32(i)
		}
		return out
	}
	cat := func(parts ...[]captureResult) []captureResult {
		var out []captureResult
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	v2err := &frame.HeaderError{Reason: frame.HeaderVersion, Header: v2.reject.Header}
	moderr := &frame.HeaderError{Reason: frame.HeaderModulation, Header: mod5.reject.Header}
	cases := []struct {
		name   string
		rs     []captureResult
		want   ShortError
		blocks bool
	}{
		{"no code", rep(captureResult{}, 6), ShortError{Captures: 6}, false},
		{"finders, header refused", rep(crc, 4), ShortError{Captures: 4, Located: 4}, false},
		{"unsupported version", cat(rep(v2, 3), rep(crc, 1)), ShortError{Captures: 4, Located: 4, Reject: v2err}, false},
		{"two agree", cat(rep(v2, 2), rep(crc, 2)), ShortError{Captures: 4, Located: 4}, false},
		// A still frame: one false CRC pass repeated in every capture does not agree.
		{"one frame index", []captureResult{v2, v2, v2, v2}, ShortError{Captures: 4, Located: 4}, false},
		{"three agree of seven", cat(rep(v2, 3), rep(crc, 4)), ShortError{Captures: 7, Located: 7}, false},
		{"values differ", cat(rep(v2, 2), rep(refused(key, frame.HeaderVersion, v(3)), 2)), ShortError{Captures: 4, Located: 4}, false},
		{"a header was read", cat(rep(v2, 3), []captureResult{located(key, 0, nil)}),
			ShortError{Captures: 4, Located: 4, HeadersRead: 1, FramesTotal: f}, false},
		{"unknown modulation", rep(mod5, 3), ShortError{Captures: 3, Located: 3, Reject: moderr}, false},
		{"headers, no descriptor", []captureResult{located(key, 0, weak(0)), located(key, 1, weak(1)), {}, located(key, 1, weak(1))},
			ShortError{Captures: 4, Located: 3, HeadersRead: 3, FramesSeen: 2, FramesTotal: f}, false},
		{"blocks short", []captureResult{located(key, 0, []phaseBits{{frame: 0, soft: truth[0]}}), {finders: true}},
			ShortError{Captures: 2, Located: 2, HeadersRead: 1, FramesSeen: 1, FramesTotal: f, Descriptor: true}, true},
		{"other session", cat(rep(located(key, 0, nil), 2), rep(located(other, 0, nil), 3)),
			ShortError{Captures: 5, Located: 5, HeadersRead: 5, Other: 3, FramesTotal: f}, false},
		// Headers of another transfer read before the decode locked onto one (not located, so not
		// locked onto) count too.
		{"other session first", cat(rep(captureResult{finders: true, key: other}, 2), rep(located(key, 0, nil), 2),
			rep(captureResult{finders: true, key: key}, 1)),
			ShortError{Captures: 5, Located: 5, HeadersRead: 5, Other: 2, FramesTotal: f}, false},
		// One transfer's headers read in every capture, none located (the mesh fit failed, say):
		// nothing is of another transfer (re-review of M4's third pull request).
		{"headers, never located", rep(captureResult{finders: true, key: key}, 4),
			ShortError{Captures: 4, Located: 4, HeadersRead: 4}, false},
		// Two transfers, none located: the one seen most is the transfer, the other is other.
		{"two transfers, none located", cat(rep(captureResult{finders: true, key: other}, 2),
			rep(captureResult{finders: true, key: key}, 3)),
			ShortError{Captures: 5, Located: 5, HeadersRead: 5, Other: 2}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := diagRun(t, tc.rs)
			if (len(got.Blocks) > 0) != tc.blocks {
				t.Errorf("blocks %+v, want some: %v", got.Blocks, tc.blocks)
			}
			got.Blocks = nil
			if (got.Reject == nil) != (tc.want.Reject == nil) || got.Reject != nil && *got.Reject != *tc.want.Reject {
				t.Errorf("reject %+v, want %+v", got.Reject, tc.want.Reject)
			}
			got.Reject, tc.want.Reject = nil, nil
			if !reflect.DeepEqual(*got, tc.want) {
				t.Errorf("got %+v, want %+v", *got, tc.want)
			}
		})
	}
}

// TestLockedRateRule: the decoder reports a camera locked to the display rate when, for the frames
// seen in more than one pass, the best clean fraction of a later pass differs from the first
// pass's by at most lockTolerance in magnitude, on average over the comparisons, of which there are
// at least lockMinPairs, and the first passes' mean is under lockMixed: the same part of every
// capture is mixed pass after pass. Captures are stubbed (processCapture); the filmed cases are
// TestLockedRateGuidance and TestLockedRateRuleFilmed (long only).
func TestLockedRateRule(t *testing.T) {
	key, _ := fixture(t)
	f := int(key.frames)
	// passes returns n passes over the loop, perFrame captures per frame, the clean share of each
	// given by clean(pass, frame, capture).
	passes := func(n, perFrame int, clean func(p, fr, c int) int) []captureResult {
		var out []captureResult
		for p := range n {
			for fr := range f {
				for c := range perFrame {
					out = append(out, captureResult{located: true, finders: true, key: key, first: uint32(fr), cells: 1000,
						clean: clean(p, fr, c)})
				}
			}
		}
		return out
	}
	for _, tc := range []struct {
		name string
		rs   []captureResult
		want bool
	}{
		{"locked", passes(3, 1, func(p, fr, _ int) int { return 350 + 10*fr + 7*(p%2) }), true},
		{"locked, two passes", passes(2, 1, func(_, fr, _ int) int { return 600 + 10*fr }), true},
		// Each capture's clean fraction is noisy, by up to 0.1 from pass to pass on a 212 x 120
		// grid (TestLockedRateSafety), but the noise averages out over the frames compared.
		{"locked, noisy", passes(3, 1, func(p, fr, _ int) int { return 300 + 80*((p+fr)%2) }), true},
		// A camera whose phase rotates each pass (24 against 30 fps): each frame's clean fraction
		// changes from pass to pass though their mean does not (review M1).
		{"phase rotates", passes(3, 1, func(p, fr, _ int) int { return 300 + 300*((p+fr)%2) }), false},
		{"one pass", passes(1, 1, func(int, int, int) int { return 350 }), false},
		{"clean fraction moves", passes(3, 1, func(p, _, _ int) int { return 300 + 150*p }), false},
		{"clean", passes(3, 1, func(int, int, int) int { return 960 }), false},
		{"twice the display rate", passes(3, 2, func(_, _, c int) int { return 350 + 650*c }), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := diagRun(t, tc.rs); got.LockedRate != tc.want {
				t.Errorf("locked rate %v, want %v (%+v)", got.LockedRate, tc.want, *got)
			}
		})
	}
}
