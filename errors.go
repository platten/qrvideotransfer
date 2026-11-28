package qrvt

import (
	"errors"
	"fmt"
)

// Sentinel errors. Test with errors.Is.
var (
	ErrNotImplemented = errors.New("qrvt: not implemented yet")
	// ErrNoCalibrationPlan: no step of the calibration video fits the monitor resolution given
	// (DESIGN.md section 13: no cell of 4, 8, 12 or 16 pixels divides it into a grid of at least
	// 120 x 90).
	ErrNoCalibrationPlan = errors.New("qrvt: no calibration video fits that resolution")
	ErrWrongPassphrase   = errors.New("qrvt: wrong passphrase")
	ErrBadSignature      = errors.New("qrvt: signature does not verify")
	ErrMissingSignature  = errors.New("qrvt: transfer is not signed by a trusted key")
)

// Shortfall says how many shards a block lacks.
type Shortfall struct {
	Block      int
	Have, Need int
}

// NotEnoughDataError reports that some blocks cannot be reconstructed yet. Blocks is empty when no
// stream descriptor was read. The other fields say what the recordings showed, for the failure
// guidance of DESIGN.md section 13; they are zero when the error did not come from a decode.
type NotEnoughDataError struct {
	Blocks      []Shortfall // only the blocks that are short
	Captures    int64       // captures read
	Located     int64       // captures in which the code's finders were found (a header was tried, whether or not it was read)
	HeadersRead int64       // captures whose frame header was read, of any transfer
	Other       int64       // of those, captures of a transfer other than the one decoded (or, when none was decoded, the one read most often), which were ignored
	FramesSeen  int         // code frames of the decoded transfer seen
	FramesTotal int         // its code frames; 0 when no header was read
	Descriptor  bool        // the stream descriptor was read
	// Calibration is set when the frame headers read are a calibration video's (qrvt calibrate
	// make, DESIGN.md section 13), all of one video size, and no stream descriptor was read: the
	// recordings are to be measured with AnalyzeCalibration, not decoded.
	Calibration bool
	// Reject names the header refusal the captures agree on, when no header was read: at least 3
	// captures, half of those whose finders were found, gave the same reason with the same value,
	// in headers naming at least two frame indices, such as "header reserved bits are not zero".
	// An unsupported version or registry id is an UnsupportedVersionError instead.
	Reject     string
	LockedRate bool // the clean fraction did not change from pass to pass: the camera is locked to the display rate
}

// Error implements the error interface.
func (e *NotEnoughDataError) Error() string {
	missing := 0
	for _, s := range e.Blocks {
		missing += s.Need - s.Have
	}
	return fmt.Sprintf("qrvt: not enough data: %d blocks short by %d shards in total", len(e.Blocks), missing)
}

// UnsafeArchiveError reports an archive entry that breaks a path rule; nothing was written.
type UnsafeArchiveError struct {
	Path   string
	Reason string
}

// Error implements the error interface.
func (e *UnsafeArchiveError) Error() string {
	return fmt.Sprintf("qrvt: unsafe archive entry %q: %s", e.Path, e.Reason)
}

// LimitError reports that a value exceeds a configured or built-in limit.
type LimitError struct {
	Limit      string
	Value, Max int64
	Flag       string // the command-line flag that raises the limit, such as "--max-size"; "" when none can, including a limit already at its ceiling
}

// Error implements the error interface.
func (e *LimitError) Error() string {
	return fmt.Sprintf("qrvt: %s %d exceeds the limit %d", e.Limit, e.Value, e.Max)
}

// UnsupportedVersionError reports an unknown version or registry id met in a video or stream.
type UnsupportedVersionError struct {
	What  string // for example "format version" or "modulation"
	Value int
}

// Error implements the error interface.
func (e *UnsupportedVersionError) Error() string {
	return fmt.Sprintf("qrvt: unsupported %s %d", e.What, e.Value)
}

// CalibrationReason says why [AnalyzeCalibration] found no calibration video it could measure.
type CalibrationReason uint8

// The reasons, in the order AnalyzeCalibration checks them.
const (
	// CalibrationNoCode: no capture showed a code's finders.
	CalibrationNoCode CalibrationReason = iota + 1
	// CalibrationNoHeader: finders, but no frame header was read.
	CalibrationNoHeader
	// CalibrationTransfer: frame headers were read, but none is a calibration frame's: the
	// recordings show a transfer.
	CalibrationTransfer
	// CalibrationMixedSizes: the frame headers recover to more than one video size, which no
	// calibration video does.
	CalibrationMixedSizes
	// CalibrationNothingPassed: the headers are the calibration video's, but no step passed: a poor
	// recording, or a transfer that happens to match the calibration's header pattern.
	CalibrationNothingPassed
)

// CalibrationError is returned by [AnalyzeCalibration] when it can recommend nothing.
type CalibrationError struct {
	Reason      CalibrationReason
	Captures    int64 // captures read
	Located     int64 // captures in which the code's finders were found
	HeadersRead int64 // captures whose frame header was read
	Calibration int64 // of those, captures whose header is a calibration frame's
	Sizes       int   // the video sizes the calibration headers recover to
}

func (e *CalibrationError) Error() string {
	switch e.Reason {
	case CalibrationNoCode:
		return fmt.Sprintf("qrvt: no calibration video found: no code in any of the %d captures", e.Captures)
	case CalibrationNoHeader:
		return fmt.Sprintf("qrvt: no calibration video found: the code was found in %d of %d captures, but no frame header was read",
			e.Located, e.Captures)
	case CalibrationTransfer:
		return fmt.Sprintf("qrvt: no calibration video found: the %d captures with a frame header show a transfer", e.HeadersRead)
	case CalibrationMixedSizes:
		return fmt.Sprintf("qrvt: not a calibration video: its frame headers recover to %d video sizes", e.Sizes)
	}
	return fmt.Sprintf("qrvt: no step of the calibration video passed in its %d captures", e.Calibration)
}
