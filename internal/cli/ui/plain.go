package ui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/x/ansi"
	qrvt "github.com/platten/qrvideotransfer"
)

// plain prints one line per step to Err and the summary to Out; in Quiet mode it prints nothing.
// Progress is printed in steps of 25%, or 5% with Verbose, which also prints decoding statistics.
type plain struct {
	o     Options
	t     *tracker
	steps map[qrvt.Stage]int // last step printed per stage
	stats qrvt.Stats         // last statistics printed
	err   error              // first write error
}

func newPlain(o Options, info Info) *plain {
	p := &plain{o: o, t: newTracker(nil), steps: map[qrvt.Stage]int{}}
	if o.Mode == Quiet {
		return p
	}
	line := "qrvt " + Clean(info.Command)
	if len(info.Sources) > 0 {
		line += ": " + listSources(info.Sources, 3)
	}
	if info.Output != "" {
		line += " -> " + Clean(info.Output)
	}
	p.printf(o.Err, "%s\n", line)
	return p
}

// listSources joins up to n cleaned sources and counts the rest.
func listSources(ss []string, n int) string {
	if len(ss) <= n {
		return strings.Join(cleanAll(ss), ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(cleanAll(ss[:n]), ", "), len(ss)-n)
}

// printf writes to w unless in Quiet mode, keeping the first error.
func (p *plain) printf(w io.Writer, format string, a ...any) {
	if p.o.Mode == Quiet {
		return
	}
	if _, err := fmt.Fprintf(w, format, a...); err != nil && p.err == nil {
		p.err = err
	}
}

// Event implements Renderer.
func (p *plain) Event(e qrvt.Event) {
	name := StageName(e.Stage)
	switch e.Kind {
	case qrvt.KindWarning:
		if e.Warn != nil {
			p.printf(p.o.Err, "warning: %s: %s\n", Clean(string(e.Warn.Code)), Clean(e.Warn.Text))
		}
	case qrvt.KindStats:
		if e.Stats != nil && p.o.Verbose && statsChanged(p.stats, *e.Stats) {
			p.stats = *e.Stats
			p.printf(p.o.Err, "stats: %s\n", statsLine(e.Stats))
		}
	case qrvt.KindStarted:
		pr := p.t.update(e)
		if e.Total > 0 {
			p.printf(p.o.Err, "%s: started (%s)\n", name, formatAmount(pr.unit, e.Total))
		} else {
			p.printf(p.o.Err, "%s: started\n", name)
		}
	case qrvt.KindProgress:
		pr := p.t.update(e)
		size := 25
		if p.o.Verbose {
			size = 5
		}
		pct := int(pr.fraction() * 100)
		if step := pct / size; step > p.steps[e.Stage] && pct < 100 {
			p.steps[e.Stage] = step
			p.printf(p.o.Err, "%s: %d%% (%s)\n", name, pct, progressDetail(pr))
		}
	case qrvt.KindFinished:
		pr := p.t.update(e)
		p.printf(p.o.Err, "%s: done (%s in %s)\n", name, formatAmount(pr.unit, pr.done), FormatDuration(pr.elapsed()))
	}
}

// progressDetail describes a stage in progress: amounts, rate and time remaining.
func progressDetail(pr *stageProgress) string {
	s := formatAmount(pr.unit, pr.done) + " of " + formatAmount(pr.unit, pr.total)
	if r := pr.rate(); r > 0 {
		s += ", " + formatRate(pr.unit, r)
	}
	if d, ok := pr.remaining(); ok {
		s += ", " + FormatDuration(d) + " left"
	}
	return s
}

// statsChanged reports whether statistics changed in a way worth showing: frames or blocks.
func statsChanged(a, b qrvt.Stats) bool {
	return a.FramesSeen != b.FramesSeen || a.FramesTotal != b.FramesTotal || a.BlocksReady != b.BlocksReady || a.BlocksTotal != b.BlocksTotal
}

// statsLine is the one-line form of decoding statistics.
func statsLine(s *qrvt.Stats) string {
	return fmt.Sprintf("captures %d, frames %d/%d, clean %.1f%%, raw bit errors %.2f%%, codewords %d, blocks %d/%d",
		s.Captures, s.FramesSeen, s.FramesTotal, 100*s.CleanFraction, 100*s.RawBitErrors, s.CodewordsOK, s.BlocksReady, s.BlocksTotal)
}

// Pause implements Renderer.
func (p *plain) Pause(fn func() error) error { return fn() }

// Finish implements Renderer.
func (p *plain) Finish(s *Summary, _ *Failure) error {
	if s != nil {
		p.printf(p.o.Out, "%s", summaryText(s))
		for _, w := range s.Warnings {
			p.printf(p.o.Err, "warning: %s: %s\n", Clean(string(w.Code)), Clean(w.Text))
		}
	}
	return p.err
}

// summaryText is a summary as lines: the title, then the fields aligned.
func summaryText(s *Summary) string {
	var b strings.Builder
	b.WriteString(Clean(s.Title) + "\n")
	width := 0
	for _, f := range s.Fields {
		width = max(width, ansi.StringWidth(Clean(f.Key)))
	}
	for _, f := range s.Fields {
		k := Clean(f.Key)
		fmt.Fprintf(&b, "  %s%s  %s\n", k, strings.Repeat(" ", width-ansi.StringWidth(k)), Clean(f.Value))
	}
	return b.String()
}
