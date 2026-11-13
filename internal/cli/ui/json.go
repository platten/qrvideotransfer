package ui

import qrvt "github.com/platten/qrvideotransfer"

// jsonOut prints one JSON object per line to Out, following events.schema.json.
type jsonOut struct{}

func newJSON(o Options, info Info) *jsonOut { return &jsonOut{} }

// Event implements Renderer.
func (j *jsonOut) Event(e qrvt.Event) {}

// Pause implements Renderer.
func (j *jsonOut) Pause(fn func() error) error { return nil }

// Finish implements Renderer.
func (j *jsonOut) Finish(s *Summary, f *Failure) error { return nil }
