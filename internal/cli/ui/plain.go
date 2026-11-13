package ui

import qrvt "github.com/platten/qrvideotransfer"

// plain prints one line per step to Err and the summary to Out; in Quiet mode it prints nothing.
type plain struct{}

func newPlain(o Options, info Info) *plain { return &plain{} }

// Event implements Renderer.
func (p *plain) Event(e qrvt.Event) {}

// Pause implements Renderer.
func (p *plain) Pause(fn func() error) error { return nil }

// Finish implements Renderer.
func (p *plain) Finish(s *Summary, f *Failure) error { return nil }
