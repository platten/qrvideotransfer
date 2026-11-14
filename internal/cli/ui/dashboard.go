package ui

import (
	"cmp"
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/progress"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	qrvt "github.com/platten/qrvideotransfer"
)

// eventMsg carries one event into the dashboard model.
type eventMsg qrvt.Event

// doneMsg tells the dashboard model that the job has finished.
type doneMsg struct{}

// syncMsg does nothing; sending it waits until the program's event loop runs.
type syncMsg struct{}

// Dashboard colours and styles. Colours are fixed values so that the output is the same on every
// terminal of a given colour profile.
var (
	accent      = lipgloss.Color("#7D56F4")
	good        = lipgloss.Color("#3FB950")
	dim         = lipgloss.Color("#6E7681")
	caution     = lipgloss.Color("#D29922")
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(accent)
	headStyle   = lipgloss.NewStyle().Bold(true)
	keyStyle    = lipgloss.NewStyle().Foreground(dim)
	warnStyle   = lipgloss.NewStyle().Foreground(caution)
	readyStyle  = lipgloss.NewStyle().Foreground(good)
	emptyStyle  = lipgloss.NewStyle().Foreground(dim)
	panelBorder = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(dim).Padding(0, 1)
)

// labelWidth is the width of the stage names, the longest being "decode-video".
const labelWidth = 12

// warnCount is a distinct warning and how often it was reported.
type warnCount struct {
	w qrvt.Warning
	n int
}

// model is the dashboard's Bubble Tea model: source, video and signal panels, one bar per stage
// with rate and time remaining, a block map when reading a recording, and the latest warnings.
type model struct {
	info     Info // with every string cleaned
	verbose  bool
	cancel   func()
	width    int
	t        *tracker
	stats    *qrvt.Stats
	warns    []warnCount
	stopping bool
	done     bool
}

func newModel(info Info, verbose bool, cancel func(), width int) *model {
	clean := Info{Command: Clean(info.Command), Sources: cleanAll(info.Sources), Output: Clean(info.Output), Stages: info.Stages, Signal: info.Signal}
	for _, f := range info.Video {
		clean.Video = append(clean.Video, Field{Clean(f.Key), Clean(f.Value)})
	}
	if width <= 0 {
		width = 80
	}
	return &model{info: clean, verbose: verbose, cancel: cancel, width: width, t: newTracker(info.Stages)}
}

// Init implements tea.Model.
func (m *model) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
	case eventMsg:
		m.apply(qrvt.Event(msg))
	case doneMsg:
		m.done = true
		return m, tea.Quit
	case tea.KeyPressMsg:
		if s := msg.String(); s == "q" || s == "ctrl+c" {
			return m, m.stop()
		}
	case tea.InterruptMsg:
		return m, m.stop()
	}
	return m, nil
}

// stop asks the job to stop; asked again, it quits the dashboard without waiting.
func (m *model) stop() tea.Cmd {
	if m.stopping {
		return tea.Quit
	}
	m.stopping = true
	if m.cancel != nil {
		m.cancel()
	}
	return nil
}

// apply records one event.
func (m *model) apply(e qrvt.Event) {
	switch e.Kind {
	case qrvt.KindStats:
		if e.Stats != nil {
			s := *e.Stats
			m.stats = &s
		}
	case qrvt.KindWarning:
		if e.Warn != nil {
			w := qrvt.Warning{Code: qrvt.WarningCode(Clean(string(e.Warn.Code))), Text: Clean(e.Warn.Text)}
			for i := range m.warns {
				if m.warns[i].w == w {
					m.warns[i].n++
					return
				}
			}
			m.warns = append(m.warns, warnCount{w, 1})
		}
	case qrvt.KindStarted, qrvt.KindProgress, qrvt.KindFinished:
		m.t.update(e)
	}
}

// View implements tea.Model.
func (m *model) View() tea.View {
	w := m.width
	var b strings.Builder
	title := titleStyle.Render("qrvt " + m.info.Command)
	if m.stopping && !m.done {
		title += "  " + warnStyle.Render("Stopping: waiting for the work in progress to end")
	}
	b.WriteString(title + "\n")
	b.WriteString(m.panels(w) + "\n")
	for _, p := range m.t.stages {
		b.WriteString(m.bar(p, w) + "\n")
	}
	if blocks := m.blockMap(w); blocks != "" {
		b.WriteString(blocks)
	}
	shown := m.warns
	if !m.verbose && len(shown) > 3 {
		shown = shown[len(shown)-3:]
	}
	for _, wc := range shown {
		line := "! " + string(wc.w.Code) + ": " + wc.w.Text
		if wc.n > 1 {
			line += fmt.Sprintf(" (%d times)", wc.n)
		}
		b.WriteString(warnStyle.Render(ansi.Truncate(line, w, "…")) + "\n")
	}
	if !m.done && !m.stopping {
		b.WriteString(keyStyle.Render("q: stop") + "\n")
	}
	return tea.NewView(strings.TrimSuffix(b.String(), "\n"))
}

// panels renders the source, video and signal panels side by side, or stacked when narrow.
func (m *model) panels(w int) string {
	src := []string{headStyle.Render("Source")}
	if len(m.info.Sources) > 4 {
		src = append(src, m.info.Sources[:3]...)
		src = append(src, fmt.Sprintf("and %d more", len(m.info.Sources)-3))
	} else {
		src = append(src, m.info.Sources...)
	}
	if m.info.Output != "" {
		src = append(src, "to "+m.info.Output)
	}
	video := append([]string{headStyle.Render("Video")}, fieldLines(m.info.Video)...)
	if len(m.info.Video) == 0 {
		video = append(video, keyStyle.Render("not known yet"))
	}
	cols := [][]string{src, video}
	if m.info.Signal {
		cols = append(cols, m.signal())
	}
	n := len(cols)
	stack := w < 24*n
	pw := w / n
	if stack {
		pw = w
	}
	height := 0
	for _, c := range cols {
		height = max(height, len(c))
	}
	boxes := make([]string, n)
	for i, c := range cols {
		inner := max(pw-4, 1)
		for j := range c {
			c[j] = ansi.Truncate(c[j], inner, "…")
		}
		h := height
		if stack {
			h = len(c)
		}
		boxes[i] = panelBorder.Width(pw).Height(h + 2).Render(strings.Join(c, "\n"))
	}
	if stack {
		return lipgloss.JoinVertical(lipgloss.Left, boxes...)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, boxes...)
}

// fieldLines renders fields as aligned "key  value" lines.
func fieldLines(fs []Field) []string {
	kw := 0
	for _, f := range fs {
		kw = max(kw, ansi.StringWidth(f.Key))
	}
	r := make([]string, len(fs))
	for i, f := range fs {
		r[i] = keyStyle.Render(f.Key+strings.Repeat(" ", kw-ansi.StringWidth(f.Key))) + "  " + f.Value
	}
	return r
}

// signal is the content of the signal panel.
func (m *model) signal() []string {
	lines := []string{headStyle.Render("Signal")}
	s := m.stats
	if s == nil {
		return append(lines, keyStyle.Render("waiting for the first capture"))
	}
	return append(lines, fieldLines([]Field{
		{"Captures", fmt.Sprint(s.Captures)},
		{"Frames", fmt.Sprintf("%d of %d", s.FramesSeen, s.FramesTotal)},
		{"Clean", fmt.Sprintf("%.1f%%", 100*s.CleanFraction)},
		{"Bit errors", fmt.Sprintf("%.2f%%", 100*s.RawBitErrors)},
		{"Codewords", fmt.Sprint(s.CodewordsOK)},
	})...)
}

// bar renders one stage: name, bar, then percentage, rate and time remaining.
func (m *model) bar(p *stageProgress, w int) string {
	var status string
	switch {
	case p.finished:
		status = "done  " + formatAmount(p.unit, p.done) + " in " + FormatDuration(p.elapsed())
	case !p.started:
		status = keyStyle.Render("waiting")
	default:
		status = fmt.Sprintf("%3d%%", int(p.fraction()*100))
		if r := p.rate(); r > 0 {
			status += "  " + formatRate(p.unit, r)
		}
		if d, ok := p.remaining(); ok {
			status += "  " + FormatDuration(d) + " left"
		}
	}
	const statusWidth = 32
	bw := max(w-labelWidth-statusWidth-2, 8)
	pb := progress.New(progress.WithWidth(bw), progress.WithoutPercentage(), progress.WithFillCharacters('█', '░'),
		progress.WithColors(accent))
	if p.finished {
		pb = progress.New(progress.WithWidth(bw), progress.WithoutPercentage(), progress.WithFillCharacters('█', '░'),
			progress.WithColors(good))
	}
	name := StageName(p.stage)
	line := name + strings.Repeat(" ", max(labelWidth-len(name), 0)) + " " + pb.ViewAs(p.fraction()) + " " + status
	return ansi.Truncate(line, w, "…")
}

// blockMap shows how many outer-code blocks can be reconstructed, one glyph per block (or per group
// of blocks when there are more than three lines' worth).
func (m *model) blockMap(w int) string {
	s := m.stats
	if !m.info.Signal || s == nil || s.BlocksTotal <= 0 {
		return ""
	}
	cols := max(w-2, 1)
	n := min(s.BlocksTotal, 3*cols)
	ready := min(max(s.BlocksReady, 0), s.BlocksTotal) * n / s.BlocksTotal
	var b strings.Builder
	b.WriteString(headStyle.Render("Blocks") + fmt.Sprintf("  %d of %d ready", s.BlocksReady, s.BlocksTotal) + "\n")
	for row := 0; row*cols < n; row++ {
		lo, hi := row*cols, min(row*cols+cols, n)
		r := min(max(ready-lo, 0), hi-lo)
		b.WriteString(readyStyle.Render(strings.Repeat("█", r)) + emptyStyle.Render(strings.Repeat("░", hi-lo-r)) + "\n")
	}
	return b.String()
}

// dashboard runs the model in a Bubble Tea program. It forwards every event that changes what is
// shown and drops progress that changes nothing visible (less than a thousandth of a stage).
type dashboard struct {
	o       Options
	p       *tea.Program
	send    func(tea.Msg)
	done    chan struct{}
	runErr  error
	permil  map[qrvt.Stage]int64
	sent    *qrvt.Stats // last statistics forwarded
	pending *qrvt.Stats // newer statistics not forwarded yet
}

func newDashboard(o Options, info Info) *dashboard {
	// The size applies only when the output is not a terminal, whose own size is used otherwise.
	opts := []tea.ProgramOption{tea.WithOutput(o.Out), tea.WithInput(o.In), tea.WithWindowSize(cmp.Or(o.Width, 80), 100)}
	d := &dashboard{o: o, done: make(chan struct{})}
	d.p = tea.NewProgram(newModel(info, o.Verbose, o.Cancel, o.Width), opts...)
	d.send = d.p.Send
	go func() {
		defer close(d.done)
		if _, err := d.p.Run(); err != nil && !errors.Is(err, tea.ErrInterrupted) && !errors.Is(err, tea.ErrProgramKilled) {
			d.runErr = err
		}
	}()
	return d
}

// Event implements Renderer.
func (d *dashboard) Event(e qrvt.Event) {
	if d.permil == nil {
		d.permil = map[qrvt.Stage]int64{}
	}
	switch e.Kind {
	case qrvt.KindProgress:
		pm := int64(-1)
		if e.Total > 0 {
			pm = e.Done * 1000 / e.Total
		}
		if last, ok := d.permil[e.Stage]; ok && last == pm && e.Done != e.Total {
			return
		}
		d.permil[e.Stage] = pm
	case qrvt.KindStarted, qrvt.KindFinished:
		delete(d.permil, e.Stage)
	case qrvt.KindStats:
		if e.Stats == nil {
			return
		}
		s := *e.Stats
		if d.sent != nil && !statsChanged(*d.sent, s) && d.sent.Captures/32 == s.Captures/32 {
			d.pending = &s
			return
		}
		d.sent, d.pending = &s, nil
		e.Stats = &s
	}
	d.send(eventMsg(e))
}

// Pause implements Renderer.
func (d *dashboard) Pause(fn func() error) error {
	// The program must be past its start-up before the terminal is released: a message taken by
	// its event loop (the channel is unbuffered) proves it.
	d.send(syncMsg{})
	_ = d.p.ReleaseTerminal()
	defer func() { _ = d.p.RestoreTerminal() }()
	return fn()
}

// Finish implements Renderer.
func (d *dashboard) Finish(s *Summary, _ *Failure) error {
	if d.pending != nil {
		d.send(eventMsg(qrvt.Event{Kind: qrvt.KindStats, Stats: d.pending}))
	}
	d.send(doneMsg{})
	<-d.done
	if d.runErr != nil || s == nil {
		return d.runErr
	}
	pl := &plain{o: d.o, t: newTracker(nil)}
	return pl.Finish(s, nil)
}
