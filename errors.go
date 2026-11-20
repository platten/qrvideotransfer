package qrvt

import (
	"errors"
	"fmt"
)

// Sentinel errors. Test with errors.Is.
var (
	ErrNotImplemented   = errors.New("qrvt: not implemented yet")
	ErrWrongPassphrase  = errors.New("qrvt: wrong passphrase")
	ErrBadSignature     = errors.New("qrvt: signature does not verify")
	ErrMissingSignature = errors.New("qrvt: transfer is not signed by a trusted key")
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
	Located     int64       // captures in which the code's finders were found and its header read
	HeadersRead int64       // captures whose frame header was read, of any transfer
	Other       int64       // of those, captures of a transfer other than the one decoded, which were ignored
	FramesSeen  int         // code frames of the decoded transfer seen
	FramesTotal int         // its code frames; 0 when no header was read
	Descriptor  bool        // the stream descriptor was read
	// Reject names the header refusal the captures agree on, when no header was read: at least 3
	// captures, half of those whose finders were found, gave the same reason with the same value,
	// such as "header reserved bits are not zero". An unsupported version or registry id is an
	// UnsupportedVersionError instead.
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
