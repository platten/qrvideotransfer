package payload

import (
	"errors"
	"fmt"
	"strings"
)

// Built-in limits (SECURITY.md section 5). Each is enforced with a [LimitError] naming it.
const (
	DefaultMaxSize     = 16 << 30  // unpacked size when UnpackOptions.MaxSize is 0 (--max-size)
	MaxStreamLen       = 64 << 30  // stream length L
	MaxManifestBytes   = 64 << 20  // manifest JSON: the format limit (FORMAT.md section 12)
	MaxManifestEntries = 1_000_000 // manifest entries: the format limit

	// The manifest limits when UnpackOptions.MaxEntries is 0, a tenth of the format limits, rounded
	// down (SECURITY.md section 5). They equal the root package's constants of the same names, which
	// this package may not import.
	DefaultMaxEntries       = 100_000
	DefaultMaxManifestBytes = 6_710_886

	ScryptWorkFactor = 18      // log2 of the scrypt cost written, and the largest accepted
	MaxZstdWindow    = 8 << 20 // largest zstd window accepted, and the window SpeedBestCompression uses
	XZDictCap        = 8 << 20 // xz dictionary written and the largest accepted
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

// InputsError lists every input path that cannot be packed, each with the rule of FORMAT.md section
// 12 it breaks, so that all of them can be renamed at once. errors.As finds each [InputError] in it.
type InputsError struct {
	Inputs []*InputError
}

// Error implements the error interface: one line per path.
func (e *InputsError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "payload: cannot pack %d paths that break the path rules:", len(e.Inputs))
	for _, ie := range e.Inputs {
		fmt.Fprintf(&b, "\n\t%q: %s", ie.Path, ie.Reason)
	}
	return b.String()
}

// Unwrap returns the errors for errors.Is and errors.As.
func (e *InputsError) Unwrap() []error {
	errs := make([]error, len(e.Inputs))
	for i, ie := range e.Inputs {
		errs[i] = ie
	}
	return errs
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
	Flag       string // the command-line flag that raises the limit; empty when it cannot be raised
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
	WarnSkippedFile   WarningCode = "SkippedFile"   // a link or special file was not packed
	WarnKeyReadable   WarningCode = "KeyReadable"   // a private key file is readable by others
	WarnReceiverLimit WarningCode = "ReceiverLimit" // the receiver must raise a default limit
)

// Warning is a condition the caller should report that does not stop the work.
type Warning struct {
	Code WarningCode
	Path string
	Text string // one sentence for a person
}
