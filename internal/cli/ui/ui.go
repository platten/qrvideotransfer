package ui

import (
	"io"

	qrvt "github.com/platten/qrvideotransfer"
)

// Mode selects a renderer.
type Mode int

// Renderers.
const (
	Dashboard Mode = iota // interactive panels and bars; stdout is a terminal
	Plain                 // one line per step; stdout is not a terminal, or --plain
	JSON                  // one JSON object per line on stdout (--json)
	Quiet                 // nothing but errors (--quiet)
)

// Field is one labelled value of a panel or a summary.
type Field struct {
	Key, Value string
}

// Info describes a job before it starts: what the dashboard's source and video panels show.
type Info struct {
	Command string       // for example "encode"
	Sources []string     // input files or recordings
	Output  string       // output file or folder
	Video   []Field      // video settings, when known
	Stages  []qrvt.Stage // the stages expected, shown as pending bars from the start
	Signal  bool         // show the signal panel and the block map (reading a recording)
}

// Summary is what a finished job prints.
type Summary struct {
	Title    string
	Fields   []Field
	Warnings []qrvt.Warning
}

// Failure is a failed job as a renderer shows it.
type Failure struct {
	Message  string // what went wrong
	Guidance string // what to do about it; may be empty
	Exit     int    // the process exit code
}

// Renderer shows the progress of one job. Event is called from one goroutine; Finish is called
// once, after the last Event.
type Renderer interface {
	// Event shows one progress event.
	Event(e qrvt.Event)
	// Pause stops live output while fn runs, for example to prompt for a passphrase.
	Pause(fn func() error) error
	// Finish stops live output and prints the summary (when s is not nil) or the failure (when f
	// is not nil, in the JSON renderer only: the others leave errors to the caller).
	Finish(s *Summary, f *Failure) error
}

// Options configure a renderer.
type Options struct {
	Mode    Mode
	Verbose bool
	Out     io.Writer // summaries, the dashboard and JSON
	Err     io.Writer // plain progress lines and warnings
	In      io.Reader // the dashboard's keyboard; nil for none
	Cancel  func()    // called when the user asks the dashboard to stop; nil allowed
	Width   int       // dashboard width when the terminal does not report one; 0 for 80
}

// New returns the renderer o selects for the job info describes.
func New(o Options, info Info) Renderer {
	switch o.Mode {
	case JSON:
		return newJSON(o, info)
	case Dashboard:
		return newDashboard(o, info)
	default:
		return newPlain(o, info)
	}
}
