package qrvt

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/platten/qrvideotransfer/internal/calib"
	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/payload"
	"github.com/platten/qrvideotransfer/internal/pipeline"
	"github.com/platten/qrvideotransfer/internal/video"
)

// testBackends maps backend names that only tests register to their backends; nil in a program.
var testBackends map[string]video.Backend

// testRandom, when a test sets it, replaces crypto/rand as the source of an encode's session id
// and outer order seed, so that a test run can be repeated; nil in a program.
var testRandom io.Reader

// backend returns the video backend that spec names (DESIGN.md section 13, --ffmpeg).
func backend(spec string, unsandboxedHEVC bool) (video.Backend, error) {
	if b, ok := testBackends[spec]; ok {
		return b, nil
	}
	return video.New(spec, video.Options{UnsandboxedHEVC: unsandboxedHEVC})
}

// progress adapts a root callback to the pipeline's events.
func progress(f func(Event)) func(pipeline.Event) {
	if f == nil {
		return nil
	}
	return func(e pipeline.Event) { f(convertEvent(e)) }
}

// convertEvent turns a pipeline event into the root package's; the enumerations have equal values.
func convertEvent(e pipeline.Event) Event {
	out := Event{Time: e.Time, Stage: Stage(e.Stage), Kind: EventKind(e.Kind), Done: e.Done, Total: e.Total, Unit: Unit(e.Unit)}
	if e.Stats != nil {
		s := Stats(*e.Stats)
		out.Stats = &s
	}
	if e.Warn != nil {
		out.Warn = &Warning{Code: WarningCode(e.Warn.Code), Text: e.Warn.Text}
	}
	return out
}

func convertWarnings(ws []pipeline.Warning) []Warning {
	var out []Warning
	for _, w := range ws {
		out = append(out, Warning{Code: WarningCode(w.Code), Text: w.Text})
	}
	return out
}

// convertError turns the pipeline's and payload's errors into the typed errors of this package,
// keeping the original's message.
func convertError(err error) error {
	var (
		short   *pipeline.ShortError
		unsafe  *payload.UnsafeArchiveError
		version *payload.UnsupportedVersionError
		limit   *payload.LimitError
	)
	switch {
	case err == nil:
		return nil
	case errors.As(err, &short):
		return convertShort(short)
	case errors.As(err, &unsafe):
		return &UnsafeArchiveError{Path: unsafe.Path, Reason: unsafe.Reason}
	case errors.As(err, &version):
		return &UnsupportedVersionError{What: version.What, Value: int(version.Value)}
	case errors.As(err, &limit):
		return &LimitError{Limit: limit.Limit, Value: limit.Value, Max: limit.Max, Flag: limit.Flag}
	case errors.Is(err, payload.ErrWrongPassphrase), errors.Is(err, payload.ErrPassphraseRequired):
		return fmt.Errorf("%w: %w", ErrWrongPassphrase, err)
	case errors.Is(err, payload.ErrBadSignature):
		return fmt.Errorf("%w: %w", ErrBadSignature, err)
	case errors.Is(err, payload.ErrMissingSignature):
		return fmt.Errorf("%w: %w", ErrMissingSignature, err)
	}
	return err
}

// convertShort turns the pipeline's ShortError into a NotEnoughDataError with its diagnostics, or,
// when the captures agree that the header names an unsupported format version, modulation or inner
// code, into an UnsupportedVersionError naming the value (FORMAT.md section 14).
func convertShort(short *pipeline.ShortError) error {
	if r := short.Reject; r != nil {
		switch r.Reason {
		case frame.HeaderVersion:
			return &UnsupportedVersionError{What: "format version", Value: int(r.Header.Version)}
		case frame.HeaderModulation:
			return &UnsupportedVersionError{What: "modulation", Value: int(r.Header.Modulation)}
		case frame.HeaderInner:
			return &UnsupportedVersionError{What: "inner code", Value: int(r.Header.InnerCode)}
		}
	}
	e := &NotEnoughDataError{Captures: short.Captures, Located: short.Located, HeadersRead: short.HeadersRead,
		Other: short.Other, FramesSeen: short.FramesSeen, FramesTotal: short.FramesTotal, Descriptor: short.Descriptor,
		LockedRate: short.LockedRate}
	for _, s := range short.Blocks {
		e.Blocks = append(e.Blocks, Shortfall(s))
	}
	if short.Reject != nil {
		e.Reject = strings.TrimPrefix(short.Reject.Error(), "frame: ")
	}
	e.Calibration = !short.Descriptor && calibrationHeaders(short.Headers)
	return e
}

// calibrationHeaders reports whether headers, at least one, are all a calibration video's
// (calib.Recover) and recover to one video size (DESIGN.md section 13).
func calibrationHeaders(hs []frame.Header) bool {
	var size calib.Size
	for i, h := range hs {
		p, _, err := calib.Recover(h)
		if err != nil {
			return false
		}
		if sz := (calib.Size{W: p.Width, H: p.Height}); i == 0 {
			size = sz
		} else if sz != size {
			return false
		}
	}
	return len(hs) > 0
}
