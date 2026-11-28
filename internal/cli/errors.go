package cli

import (
	"context"
	"errors"
	"fmt"

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

// guideFor is guide for the command named; not yet built.
func guideFor(_ string, err error, last *qrvt.Stats, o *qrvt.DecodeOptions) error { return guide(err, last, o) }

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
		g = fmt.Sprintf("The video uses %s %d, which this qrvt cannot read: it was made by a newer qrvt or another program. "+
			"Update qrvt, or use the program that made the video.", ver.What, ver.Value)
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

// otherMin is the fewest captures of another transfer that make a shortfall's class "another
// transfer", as the decoder's agreement on a refusal needs 3 captures.
const otherMin = 3

// shortGuidance says what a decode lacks, with its numbers, in one sentence, and what to do in
// another (DESIGN.md section 13), by failure class: no code found, finders but no header read,
// another transfer (when no blocks are known; beside a blocks shortfall it is a clause of the
// shortfall's sentence), headers but no stream descriptor, and blocks short, for example "2.1%
// short: 21 shards missing in 1 of 1 blocks (96 of 96 code frames seen). Record one more loop and
// pass both files to qrvt decode." When the camera is locked to the display rate, the last two
// ask for a faster camera instead. An error without diagnostics (no captures counted) gets the
// blocks it names, if any, and the generic action.
func shortGuidance(e *qrvt.NotEnoughDataError, last *qrvt.Stats, o *qrvt.DecodeOptions) string {
	more := "Record one more loop and pass all the recordings to qrvt decode."
	switch {
	case o != nil && o.StateDir != "":
		more = "Record one more loop and decode it with the same --state-dir."
	case o != nil && len(o.Recordings) == 1:
		more = "Record one more loop and pass both files to qrvt decode."
	}
	frames := ""
	if e.FramesTotal > 0 {
		frames = fmt.Sprintf(" (%d of %d code frames seen)", e.FramesSeen, e.FramesTotal)
	}
	var missing string
	switch {
	case e.Captures > 0 && e.Located == 0:
		return fmt.Sprintf("No code was found in any of the %d captures. "+
			"Film the whole code, in focus and filling most of the picture, and run qrvt decode again.", e.Captures)
	case e.Captures > 0 && e.HeadersRead == 0:
		why := ""
		if e.Reject != "" {
			why = " (" + e.Reject + ")"
		}
		return fmt.Sprintf("The code's corners were found in %d of %d captures, but no frame header could be read%s. "+
			"Film closer and steadier, with the screen sharp and evenly lit, and check that the video was made by qrvt.",
			e.Located, e.Captures, why)
	case e.Other >= otherMin && len(e.Blocks) == 0:
		return fmt.Sprintf("%d of the %d captures with a frame header showed another transfer, which was ignored: "+
			"a decode follows the first transfer it reads. Decode each transfer's recordings separately.", e.Other, e.HeadersRead)
	case len(e.Blocks) > 0:
		shards, need := 0, 0
		for _, s := range e.Blocks {
			shards += max(s.Need-s.Have, 0)
			need = max(need, s.Need)
		}
		total := len(e.Blocks)
		if last != nil && last.BlocksTotal > total {
			total = last.BlocksTotal
		}
		pct := float64(shards) * 100 / float64(max(need*total, 1))
		missing = fmt.Sprintf("%.1f%% short: %d shards missing in %d of %d blocks%s", pct, shards, len(e.Blocks), total, frames)
		if e.Other >= otherMin {
			missing += fmt.Sprintf(", and %d of the %d captures with a frame header showed another transfer, which was ignored",
				e.Other, e.HeadersRead)
		}
	case e.Captures > 0:
		missing = fmt.Sprintf("Frame headers were read in %d of %d captures%s, but not the stream descriptor", e.HeadersRead,
			e.Captures, frames)
	default:
		return more
	}
	if e.LockedRate {
		clean := ""
		if last != nil && last.CleanFraction > 0 {
			clean = fmt.Sprintf(", %.0f%% of each capture clean on average", 100*last.CleanFraction)
		}
		return missing + ", and every pass repeated the same frame mixing" + clean +
			": the camera is locked to the display rate. Film again with a camera at least twice the display rate, " +
			"such as 60 fps for a 30 fps video: more passes at this rate repeat the same mixing."
	}
	return missing + ". " + more
}
