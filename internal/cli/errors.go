package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	qrvt "github.com/platten/qrvideotransfer"
)

// Exit codes, returned by [ExitCode].
const (
	exitFailure     = 1   // any other failure
	exitUsage       = 2   // a bad command line
	exitShort       = 3   // not enough data yet: record more and try again
	exitAuth        = 4   // wrong passphrase, or a bad or missing signature
	exitRefused     = 5   // unsafe archive, a limit exceeded, or an unsupported version
	exitInterrupted = 130 // stopped by the user
)

// usageError is a bad command line.
type usageError struct{ err error }

func (e *usageError) Error() string { return e.err.Error() }
func (e *usageError) Unwrap() error { return e.err }

// usagef returns a usage error with a formatted message.
func usagef(format string, a ...any) error { return &usageError{fmt.Errorf(format, a...)} }

// guidedError is an error with advice on what to do about it.
type guidedError struct {
	err      error
	guidance string
}

func (e *guidedError) Error() string { return e.err.Error() + "\n" + e.guidance }
func (e *guidedError) Unwrap() error { return e.err }

// ExitCode maps an error returned by [Execute] to the process exit code: 0 for none, 2 for a bad
// command line, 3 when a decode lacks data (record more and try again), 4 for a wrong passphrase
// or a bad or missing signature, 5 when the input was refused (unsafe archive, a limit, an
// unsupported version), 130 when stopped by the user and 1 otherwise.
func ExitCode(err error) int {
	var (
		usage  *usageError
		short  *qrvt.NotEnoughDataError
		unsafe *qrvt.UnsafeArchiveError
		limit  *qrvt.LimitError
		ver    *qrvt.UnsupportedVersionError
	)
	switch {
	case err == nil:
		return 0
	case errors.As(err, &usage):
		return exitUsage
	case errors.Is(err, context.Canceled):
		return exitInterrupted
	case errors.As(err, &short):
		return exitShort
	case errors.Is(err, qrvt.ErrWrongPassphrase), errors.Is(err, qrvt.ErrBadSignature), errors.Is(err, qrvt.ErrMissingSignature):
		return exitAuth
	case errors.As(err, &unsafe), errors.As(err, &limit), errors.As(err, &ver):
		return exitRefused
	}
	return exitFailure
}

// guide adds failure guidance (DESIGN.md section 13) to the typed errors of the root package. last
// is the latest decoding statistics and o the decode options, both possibly nil.
func guide(err error, last *qrvt.Stats, o *qrvt.DecodeOptions) error {
	if err == nil {
		return nil
	}
	var (
		short  *qrvt.NotEnoughDataError
		unsafe *qrvt.UnsafeArchiveError
		limit  *qrvt.LimitError
		ver    *qrvt.UnsupportedVersionError
		g      string
	)
	switch {
	case errors.As(err, &short):
		g = shortGuidance(short, last, o)
	case errors.Is(err, qrvt.ErrWrongPassphrase):
		g = "Check the passphrase (and the --passphrase-file, if you used one) and try again."
	case errors.Is(err, qrvt.ErrBadSignature):
		g = "The video was altered or damaged after it was signed. Nothing was decrypted or unpacked."
	case errors.Is(err, qrvt.ErrMissingSignature):
		g = "The transfer is not signed by any key given with --verify-key. Nothing was decrypted or unpacked. " +
			"Check that you have the sender's current public key."
	case errors.As(err, &unsafe):
		g = "Nothing was written. The archive tries to do something unsafe; do not trust its sender."
	case errors.As(err, &limit):
		if limit.Flag != "" {
			g = "If you expect a transfer this large, raise the limit with " + limit.Flag + "."
		} else {
			g = "No flag raises it: the input is unusual, damaged or hostile."
		}
	case errors.As(err, &ver):
		g = "This video was made by a newer qrvt or another program. Update qrvt."
	case errors.Is(err, qrvt.ErrNotImplemented):
		g = "This part of qrvt is not implemented yet in this build."
	case errors.Is(err, context.Canceled):
		g = "Stopped."
		if o != nil && o.StateDir != "" {
			g = "Stopped. Progress is kept in --state-dir: run the same command again to resume."
		}
	default:
		return err
	}
	return &guidedError{err, g}
}

// shortGuidance says how much data a decode lacks and what to record next, for example "2.1%
// short: record one more loop and pass both files".
func shortGuidance(e *qrvt.NotEnoughDataError, last *qrvt.Stats, o *qrvt.DecodeOptions) string {
	next := "record one more loop and pass all the recordings to qrvt decode"
	switch {
	case o != nil && o.StateDir != "":
		next = "record one more loop and decode it with the same --state-dir"
	case o != nil && len(o.Recordings) == 1:
		next = "record one more loop and pass both files to qrvt decode"
	}
	missing, need := 0, 0
	for _, s := range e.Blocks {
		missing += max(s.Need-s.Have, 0)
		need = max(need, s.Need)
	}
	total := len(e.Blocks)
	if last != nil && last.BlocksTotal > total {
		total = last.BlocksTotal
	}
	if missing == 0 || need == 0 {
		return strings.ToUpper(next[:1]) + next[1:] + "."
	}
	pct := float64(missing) * 100 / float64(need*total)
	return fmt.Sprintf("%.1f%% short: %s (%d shards missing in %d blocks).", pct, next, missing, len(e.Blocks))
}
