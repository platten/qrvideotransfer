package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"charm.land/fang/v2"
	qrvt "github.com/platten/qrvideotransfer"
	"golang.org/x/term"
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

// NewEnv returns the environment of the running process.
func NewEnv(version string) *Env {
	env := &Env{API: Root{}, In: os.Stdin, Out: os.Stdout, Err: os.Stderr, Version: version, IsTerminal: isTerminal}
	env.ReadPassword = func(prompt string) (string, error) {
		f, ok := env.In.(*os.File)
		if !ok || !term.IsTerminal(int(f.Fd())) {
			return "", errors.New("standard input is not a terminal")
		}
		if _, err := fmt.Fprint(env.Err, prompt); err != nil {
			return "", err
		}
		b, err := term.ReadPassword(int(f.Fd()))
		_, _ = fmt.Fprintln(env.Err)
		return string(b), err
	}
	return env
}

// isTerminal reports whether a stream is a terminal.
func isTerminal(stream any) bool {
	f, ok := stream.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(int(f.Fd()))
}

// Execute runs the command line args (without the program name) and returns the error, already
// printed on env.Err, that the process should exit with; see [ExitCode]. An interrupt signal
// cancels the running command.
func Execute(ctx context.Context, env *Env, args []string) error {
	a, root := newApp(env)
	root.SetArgs(args)
	root.SetIn(env.In)
	root.SetOut(env.Out)
	root.SetErr(env.Err)
	return fang.Execute(ctx, root, fang.WithVersion(env.Version), fang.WithoutManpage(),
		fang.WithErrorHandler(a.errorHandler(args)), fang.WithNotifySignal(os.Interrupt))
}
