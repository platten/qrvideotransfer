package cli

import (
	"context"
	"errors"
	"io"

	qrvt "github.com/platten/qrvideotransfer"
	"github.com/spf13/cobra"
)

// API is the part of the root package the commands call. The real one is [Root]; tests use fakes.
type API interface {
	Encode(ctx context.Context, o qrvt.EncodeOptions) (*qrvt.EncodeReport, error)
	Decode(ctx context.Context, o qrvt.DecodeOptions) (*qrvt.DecodeReport, error)
	CalibrationVideo(ctx context.Context, o qrvt.CalibrationOptions) error
	AnalyzeCalibration(ctx context.Context, o qrvt.AnalyzeOptions) (*qrvt.Recommendation, error)
	Inspect(ctx context.Context, o qrvt.InspectOptions) (*qrvt.StreamInfo, error)
	GenerateKey() (public, private []byte, err error)
}

// Root is the [API] of the root package.
type Root struct{}

// Encode calls [qrvt.Encode].
func (Root) Encode(ctx context.Context, o qrvt.EncodeOptions) (*qrvt.EncodeReport, error) {
	return qrvt.Encode(ctx, o)
}

// Decode calls [qrvt.Decode].
func (Root) Decode(ctx context.Context, o qrvt.DecodeOptions) (*qrvt.DecodeReport, error) {
	return qrvt.Decode(ctx, o)
}

// CalibrationVideo calls [qrvt.CalibrationVideo].
func (Root) CalibrationVideo(ctx context.Context, o qrvt.CalibrationOptions) error {
	return qrvt.CalibrationVideo(ctx, o)
}

// AnalyzeCalibration calls [qrvt.AnalyzeCalibration].
func (Root) AnalyzeCalibration(ctx context.Context, o qrvt.AnalyzeOptions) (*qrvt.Recommendation, error) {
	return qrvt.AnalyzeCalibration(ctx, o)
}

// Inspect calls [qrvt.Inspect].
func (Root) Inspect(ctx context.Context, o qrvt.InspectOptions) (*qrvt.StreamInfo, error) {
	return qrvt.Inspect(ctx, o)
}

// GenerateKey calls [qrvt.GenerateKey].
func (Root) GenerateKey() (public, private []byte, err error) { return qrvt.GenerateKey() }

// Env is everything the commands touch outside the process: the API, the standard streams and the
// terminal.
type Env struct {
	API     API
	In      io.Reader
	Out     io.Writer
	Err     io.Writer
	Version string
	// IsTerminal reports whether a stream (In, Out or Err) is a terminal.
	IsTerminal func(stream any) bool
	// ReadPassword prints prompt to the terminal and reads a line without echo.
	ReadPassword func(prompt string) (string, error)
}

// NewEnv returns the environment of the running process (stub).
func NewEnv(version string) *Env { return &Env{} }

// Execute runs the command line (stub).
func Execute(ctx context.Context, env *Env, args []string) error { return nil }

// ExitCode maps an error to the process exit code (stub).
func ExitCode(err error) int { return 0 }

func usagef(format string, a ...any) error { return errors.New(format) }

func guide(err error, last *qrvt.Stats, o *qrvt.DecodeOptions) error { return err }

func newRoot(env *Env) *cobra.Command { return &cobra.Command{} }

func parseSize(s string) (int64, error) { return 0, nil }

func parseResolution(s string) (w, h int, err error) { return 0, 0, nil }
