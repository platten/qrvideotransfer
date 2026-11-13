package payload

import (
	"errors"
	"fmt"
)

// Built-in limits (SECURITY.md section 5). Each is enforced with a [LimitError] naming it.
const (
	DefaultMaxSize     = 16 << 30  // unpacked size when UnpackOptions.MaxSize is 0 (--max-size)
	MaxStreamLen       = 64 << 30  // stream length L
	MaxManifestBytes   = 64 << 20  // manifest JSON
	MaxManifestEntries = 1_000_000 // manifest entries
	ScryptWorkFactor   = 18        // log2 of the scrypt cost written, and the largest accepted
	MaxZstdWindow      = 8 << 20   // largest zstd window accepted, and the window SpeedBestCompression uses
	XZDictCap          = 8 << 20   // xz dictionary written and the largest accepted
)

// Sentinel errors. Test with errors.Is.
var (
	ErrFormat             = errors.New("payload: malformed stream")
	ErrWrongPassphrase    = errors.New("payload: wrong passphrase")
	ErrPassphraseRequired = errors.New("payload: the transfer is encrypted and no passphrase was given")
	ErrBadSignature       = errors.New("payload: signature does not verify")
	ErrMissingSignature   = errors.New("payload: transfer is not signed by a trusted key")
	ErrKeyFormat          = errors.New("payload: not a qrvt key")
	ErrInputChanged       = errors.New("payload: an input changed while it was being packed")
)

// UnsafeArchiveError reports a manifest or archive entry that breaks a rule of FORMAT.md section 12;
// nothing was written to the output folder.
type UnsafeArchiveError struct {
	Path   string
	Reason string
}

// Error implements the error interface.
func (e *UnsafeArchiveError) Error() string {
	return fmt.Sprintf("payload: unsafe archive entry %q: %s", e.Path, e.Reason)
}

// InputError reports an input that cannot be packed (FORMAT.md section 12, "What an encoder writes").
type InputError struct {
	Path   string // the archive path, or the input as given
	Reason string
}

// Error implements the error interface.
func (e *InputError) Error() string {
	return fmt.Sprintf("payload: cannot pack %q: %s", e.Path, e.Reason)
}

// ExistsError reports an output path that already exists and may not be replaced.
type ExistsError struct {
	Path   string
	Reason string
}

// Error implements the error interface.
func (e *ExistsError) Error() string {
	return fmt.Sprintf("payload: %q: %s", e.Path, e.Reason)
}

// LimitError reports that a value exceeds a limit of SECURITY.md section 5.
type LimitError struct {
	Limit      string
	Value, Max int64
}

// Error implements the error interface.
func (e *LimitError) Error() string {
	return fmt.Sprintf("payload: %s %d exceeds the limit %d", e.Limit, e.Value, e.Max)
}

// UnsupportedVersionError reports an unknown stream version, compression or manifest version.
type UnsupportedVersionError struct {
	What  string
	Value int64
}

// Error implements the error interface.
func (e *UnsupportedVersionError) Error() string {
	return fmt.Sprintf("payload: unsupported %s %d", e.What, e.Value)
}

// WarningCode identifies a kind of [Warning]. The values match the root package's warning codes
// where one exists.
type WarningCode string

// Warning codes.
const (
	WarnSkippedFile WarningCode = "SkippedFile" // a link or special file was not packed
	WarnKeyReadable WarningCode = "KeyReadable" // a private key file is readable by others
)

// Warning is a condition the caller should report that does not stop the work.
type Warning struct {
	Code WarningCode
	Path string
	Text string // one sentence for a person
}
