package ui

import (
	"bytes"
	"errors"
	"testing"

	qrvt "github.com/platten/qrvideotransfer"
)

// play runs events and a summary through a renderer of the given options and returns what it
// printed to Out and Err.
func play(t *testing.T, o Options, info Info, events []qrvt.Event, s *Summary, f *Failure) (out, errOut string) {
	t.Helper()
	var ob, eb bytes.Buffer
	o.Out, o.Err = &ob, &eb
	r := New(o, info)
	for _, e := range events {
		r.Event(e)
	}
	if err := r.Finish(s, f); err != nil {
		t.Fatal(err)
	}
	return ob.String(), eb.String()
}

func TestPlainGolden(t *testing.T) {
	tests := []struct {
		name    string
		verbose bool
		info    Info
		events  []qrvt.Event
		sum     *Summary
	}{
		{"cli-plain-decode.txt", false, DemoInfo(), DemoEvents(t0), DemoSummary()},
		{"cli-plain-decode-verbose.txt", true, DemoInfo(), DemoEvents(t0), DemoSummary()},
		{"cli-plain-encode.txt", false, encodeInfo(), encodeScript(), &Summary{Title: "Wrote out.mp4", Fields: []Field{{"Frames", "840"}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, errOut := play(t, Options{Mode: Plain, Verbose: tc.verbose}, tc.info, tc.events, tc.sum, nil)
			golden(t, tc.name, "--- stderr\n"+errOut+"--- stdout\n"+out)
		})
	}
}

func TestPlainFailurePrintsNothing(t *testing.T) {
	out, errOut := play(t, Options{Mode: Plain}, Info{Command: "decode"}, nil, nil, &Failure{Message: "boom", Exit: 1})
	if out != "" || errOut != "qrvt decode\n" {
		t.Errorf("out %q, err %q", out, errOut)
	}
}

func TestQuiet(t *testing.T) {
	out, errOut := play(t, Options{Mode: Quiet, Verbose: true}, DemoInfo(), DemoEvents(t0), DemoSummary(), nil)
	if out != "" || errOut != "" {
		t.Errorf("quiet printed out %q, err %q", out, errOut)
	}
}

func TestPause(t *testing.T) {
	want := errors.New("fn")
	for _, m := range []Mode{Plain, Quiet, JSON} {
		r := New(Options{Mode: m, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}, Info{})
		ran := false
		if err := r.Pause(func() error { ran = true; return want }); !errors.Is(err, want) || !ran {
			t.Errorf("mode %d: Pause = %v, ran %v", m, err, ran)
		}
	}
}

// failWriter fails every write.
type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestWriteErrors(t *testing.T) {
	for _, m := range []Mode{Plain, JSON} {
		r := New(Options{Mode: m, Out: failWriter{}, Err: failWriter{}}, DemoInfo())
		for _, e := range DemoEvents(t0) {
			r.Event(e)
		}
		if err := r.Finish(DemoSummary(), nil); err == nil {
			t.Errorf("mode %d: Finish reports no write error", m)
		}
	}
}
