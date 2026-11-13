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

// NotEnoughDataError reports that some blocks cannot be reconstructed yet.
type NotEnoughDataError struct {
	Blocks []Shortfall // only the blocks that are short
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
