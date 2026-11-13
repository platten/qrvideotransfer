package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"charm.land/fang/v2"
	qrvt "github.com/platten/qrvideotransfer"
	"github.com/platten/qrvideotransfer/internal/cli/ui"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// app holds the environment and the output flags shared by every command.
type app struct {
	env                         *Env
	json, plain, quiet, verbose bool
	warnings                    []qrvt.Warning // found while reading the flags, shown by the renderer
	reported                    bool           // the JSON renderer has printed the error line
}

// newRoot builds the command tree.
func newRoot(env *Env) *cobra.Command {
	_, root := newApp(env)
	return root
}

// newApp builds the command tree and the state its commands share.
func newApp(env *Env) (*app, *cobra.Command) {
	a := &app{env: env}
	root := &cobra.Command{
		Use:   "qrvt",
		Short: "Move files through a screen and a camera",
		Long: "qrvt turns files into a looping video of on-screen codes, and a phone's recording of that video back into the files.\n\n" +
			"Play the video full-screen, film the screen, copy the recording to the receiving computer and decode it. " +
			"Run qrvt calibrate first to find the fastest settings your screen and camera can manage.",
		Example: "qrvt encode report.pdf photos/ -o transfer.mp4\n" +
			"qrvt decode IMG_0412.MOV -o received/\n" +
			"qrvt calibrate make -o cal.mp4",
		Args:              cobra.ArbitraryArgs,
		RunE:              helpOrUnknown,
		PersistentPreRunE: func(*cobra.Command, []string) error { return a.checkOutputFlags() },
		SilenceUsage:      true,
		SilenceErrors:     true,
	}
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return &usageError{err} })
	pf := root.PersistentFlags()
	pf.BoolVar(&a.json, "json", false, "Print one JSON object per line: a start line, every progress event, then a summary or an error.\n"+
		"The format is internal/cli/ui/events.schema.json; for programs and other front ends.\nExample: --json")
	pf.BoolVar(&a.plain, "plain", false, "Print plain progress lines instead of the dashboard, even on a terminal.\nExample: --plain")
	pf.BoolVarP(&a.quiet, "quiet", "q", false, "Print nothing but errors.\nExample: -q")
	pf.BoolVarP(&a.verbose, "verbose", "v", false, "Print more: progress in 5% steps and decoding statistics, and every warning on the dashboard.\nExample: -v")
	root.AddCommand(a.encodeCmd(), a.decodeCmd(), a.calibrateCmd(), a.inspectCmd(), a.keygenCmd(),
		a.profilesCmd(), a.versionCmd(), a.manCmd(), a.demoCmd())
	return a, root
}

// helpOrUnknown is the action of a command that only groups others: it shows help, or refuses an
// unknown subcommand.
func helpOrUnknown(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return usagef("unknown command %q for %q", args[0], cmd.CommandPath())
	}
	return cmd.Help()
}

// noArgs refuses arguments for commands that take none.
func noArgs(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return usagef("%s takes no arguments", strings.TrimPrefix(cmd.CommandPath(), "qrvt "))
	}
	return nil
}

// checkOutputFlags refuses output flags that contradict each other.
func (a *app) checkOutputFlags() error {
	switch {
	case a.json && a.plain:
		return usagef("--json and --plain cannot be used together")
	case a.json && a.quiet:
		return usagef("--json and --quiet cannot be used together")
	case a.quiet && a.verbose:
		return usagef("--quiet and --verbose cannot be used together")
	}
	return nil
}

// terminal reports whether a stream is a terminal; without an IsTerminal function, none is.
func (a *app) terminal(stream any) bool { return a.env.IsTerminal != nil && a.env.IsTerminal(stream) }

// mode is the renderer the output flags and the terminal select.
func (a *app) mode() ui.Mode {
	switch {
	case a.json:
		return ui.JSON
	case a.quiet:
		return ui.Quiet
	case a.plain || !a.terminal(a.env.Out):
		return ui.Plain
	}
	return ui.Dashboard
}

// engine holds the flags every video command shares.
type engine struct {
	backend string
	jobs    int
	hevc    bool
}

// addEngineFlags adds --ffmpeg and --jobs, and --unsandboxed-hevc when the command reads recordings.
func addEngineFlags(fs *pflag.FlagSet, e *engine, reads bool) {
	fs.StringVar(&e.backend, "ffmpeg", "embedded", "Video engine: embedded (the sandboxed engine built into qrvt), system (the ffmpeg on the PATH) "+
		"or the path of an ffmpeg program.\nsystem or a path runs ffmpeg outside the sandbox: use it only with recordings you made yourself.\n"+
		"Example: --ffmpeg system")
	fs.IntVar(&e.jobs, "jobs", 0, "Number of workers, 0 to 1024; 0 uses the CPU count, reduced to fit memory.\nExample: --jobs 4")
	if reads {
		fs.BoolVar(&e.hevc, "unsandboxed-hevc", false, "Decode HEVC recordings with the faster pure-Go decoder inside the qrvt process.\n"+
			"Only for recordings you made yourself: it runs outside the sandbox, and its memory is not bounded against a crafted stream.\n"+
			"Example: --unsandboxed-hevc")
	}
}

// check validates the engine flags.
func (e *engine) check() error {
	if err := checkBackend(e.backend); err != nil {
		return err
	}
	return checkJobs(e.jobs)
}

// job is the work of a command: it gets the renderer and the progress callback, and returns the
// summary to print.
type job func(ctx context.Context, r ui.Renderer, progress func(qrvt.Event)) (*ui.Summary, error)

// run runs a job with the renderer the output flags select, adds failure guidance to its error
// (with the decode options d, when decoding) and shows the summary or the failure.
func (a *app) run(cmd *cobra.Command, info ui.Info, d *qrvt.DecodeOptions, do job) error {
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()
	var keys io.Reader
	if a.terminal(a.env.In) {
		keys = a.env.In
	}
	r := ui.New(ui.Options{Mode: a.mode(), Verbose: a.verbose, Out: a.env.Out, Err: a.env.Err, In: keys, Cancel: cancel}, info)
	for _, w := range a.warnings {
		r.Event(qrvt.Event{Time: time.Now(), Kind: qrvt.KindWarning, Warn: &w})
	}
	var last *qrvt.Stats
	sum, err := do(ctx, r, func(e qrvt.Event) {
		if e.Stats != nil {
			s := *e.Stats
			last = &s
		}
		r.Event(e)
	})
	err = guide(err, last, d)
	var f *ui.Failure
	if err != nil {
		sum, f = nil, failure(err)
		a.reported = a.json
	}
	if ferr := r.Finish(sum, f); err == nil {
		err = ferr
	}
	return err
}

// failure describes an error for a renderer.
func failure(err error) *ui.Failure {
	msg, g := splitGuidance(err)
	return &ui.Failure{Message: msg, Guidance: g, Exit: ExitCode(err)}
}

// jsonRequested reports whether the command line asks for --json, for errors met before or while
// the flags are parsed.
func (a *app) jsonRequested(args []string) bool {
	if a.json {
		return true
	}
	for _, s := range args {
		if s == "--" {
			break
		}
		if v, ok := strings.CutPrefix(s, "--json"); ok && (v == "" || v == "=true" || v == "=1") {
			return true
		}
	}
	return false
}

// errorHandler prints an error: in JSON mode as the single error line on stdout (unless the job's
// renderer printed it already) and nothing on stderr; otherwise as text on stderr.
func (a *app) errorHandler(args []string) fang.ErrorHandler {
	return func(w io.Writer, s fang.Styles, err error) {
		if !a.jsonRequested(args) {
			handleError(w, s, err)
			return
		}
		if !a.reported {
			_ = ui.WriteJSONFailure(a.env.Out, *failure(err))
		}
	}
}

// splitGuidance separates an error's message from its guidance.
func splitGuidance(err error) (msg, guidance string) {
	var g *guidedError
	if errors.As(err, &g) {
		return g.err.Error(), g.guidance
	}
	return err.Error(), ""
}

// handleError prints an error on stderr, without control characters, with its guidance.
func handleError(w io.Writer, _ fang.Styles, err error) {
	msg, g := splitGuidance(err)
	msg = ui.Clean(msg)
	if !strings.HasPrefix(msg, "qrvt: ") {
		msg = "qrvt: " + msg
	}
	_, _ = fmt.Fprintln(w, msg)
	if g != "" {
		_, _ = fmt.Fprintln(w, ui.Clean(g))
	}
	var u *usageError
	if errors.As(err, &u) {
		_, _ = fmt.Fprintln(w, "Run qrvt help or add --help for usage.")
	}
}
