package ui

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"
	qrvt "github.com/platten/qrvideotransfer"
)

// runDashboard plays events through the dashboard model in a test program of the given size, with
// the colour profile pinned, and returns the final view without styling.
func runDashboard(t *testing.T, info Info, events []qrvt.Event, w, h int, verbose bool) string {
	t.Helper()
	tm := teatest.NewTestModel(t, newModel(info, verbose, nil, 0),
		teatest.WithInitialTermSize(w, h),
		teatest.WithProgramOptions(tea.WithColorProfile(colorprofile.ANSI256)))
	for _, e := range events {
		tm.Send(eventMsg(e))
	}
	tm.Send(doneMsg{})
	m := tm.FinalModel(t, teatest.WithFinalTimeout(10*time.Second)).(*model)
	return ansi.Strip(m.View().Content) + "\n"
}

func TestDashboardGolden(t *testing.T) {
	demo := DemoEvents(t0)
	tests := []struct {
		name    string
		info    Info
		events  []qrvt.Event
		w, h    int
		verbose bool
	}{
		{"cli-dashboard-decode.txt", DemoInfo(), demo, 100, 30, false},
		{"cli-dashboard-decode-mid.txt", DemoInfo(), demo[:len(demo)/2], 100, 30, false},
		{"cli-dashboard-decode-verbose.txt", DemoInfo(), demo, 100, 30, true},
		{"cli-dashboard-encode.txt", encodeInfo(), encodeScript(), 80, 24, false},
		{"cli-dashboard-narrow.txt", DemoInfo(), demo, 50, 40, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			golden(t, tc.name, runDashboard(t, tc.info, tc.events, tc.w, tc.h, tc.verbose))
		})
	}
}

func TestDashboardDefaultWidth(t *testing.T) {
	m := newModel(DemoInfo(), false, nil, 0)
	for _, e := range DemoEvents(t0) {
		m.Update(eventMsg(e))
	}
	for i, l := range strings.Split(ansi.Strip(m.View().Content), "\n") {
		if w := ansi.StringWidth(l); w > 80 {
			t.Errorf("line %d is %d wide, more than the default 80", i, w)
		}
	}
	if m2 := newModel(DemoInfo(), false, nil, 64); m2.width != 64 {
		t.Errorf("width %d, want 64", m2.width)
	}
}

func TestDashboardKeys(t *testing.T) {
	for _, key := range []tea.Msg{
		tea.KeyPressMsg{Code: 'q', Text: "q"},
		tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl},
		tea.InterruptMsg{},
	} {
		var cancels atomic.Int32
		m := newModel(DemoInfo(), false, func() { cancels.Add(1) }, 80)
		_, cmd := m.Update(key)
		if cmd != nil || cancels.Load() != 1 {
			t.Errorf("%v: first press: cmd %v, cancels %d", key, cmd, cancels.Load())
		}
		if !strings.Contains(m.View().Content, "Stopping") {
			t.Errorf("%v: view does not say it is stopping", key)
		}
		_, cmd = m.Update(key)
		if cmd == nil || cancels.Load() != 1 {
			t.Errorf("%v: second press must quit without cancelling again", key)
		}
	}
	m := newModel(DemoInfo(), false, nil, 80)
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"}); cmd != nil || m.stopping {
		t.Error("an unrelated key stops the dashboard")
	}
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"}); cmd != nil || !m.stopping {
		t.Error("q without a cancel function must still mark the dashboard as stopping")
	}
	if m.Init() != nil {
		t.Error("Init returns a command")
	}
}

func TestDashboardHostile(t *testing.T) {
	info, events, _ := hostileJob()
	clean := Info{Command: Clean(info.Command), Output: Clean(info.Output), Stages: info.Stages, Signal: true}
	for _, s := range info.Sources {
		clean.Sources = append(clean.Sources, Clean(s))
	}
	for _, f := range info.Video {
		clean.Video = append(clean.Video, Field{Clean(f.Key), Clean(f.Value)})
	}
	for _, verbose := range []bool{false, true} {
		h, c := newModel(info, verbose, nil, 120), newModel(clean, verbose, nil, 120)
		for _, e := range events {
			h.Update(eventMsg(e))
			ce := e
			if e.Warn != nil {
				ce.Warn = &qrvt.Warning{Code: qrvt.WarningCode(Clean(string(e.Warn.Code))), Text: Clean(e.Warn.Text)}
			}
			c.Update(eventMsg(ce))
		}
		hv, cv := h.View().Content, c.View().Content
		if hv != cv {
			t.Errorf("hostile strings change the view:\n%s\n---\n%s", hv, cv)
		}
		assertClean(t, "dashboard", ansi.Strip(hv))
	}
}

func TestDashboardRenderer(t *testing.T) {
	var out, errOut bytes.Buffer
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	r := New(Options{Mode: Dashboard, Out: &out, Err: &errOut, In: pr, Width: 90}, DemoInfo())
	events := DemoEvents(t0)
	for _, e := range events[:len(events)/2] {
		r.Event(e)
	}
	want := errors.New("prompt")
	ran := false
	if err := r.Pause(func() error { ran = true; return want }); !errors.Is(err, want) || !ran {
		t.Errorf("Pause = %v, ran %v", err, ran)
	}
	for _, e := range events[len(events)/2:] {
		r.Event(e)
	}
	if err := r.Finish(DemoSummary(), nil); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "Signal") || !strings.Contains(got, DemoSummary().Title) {
		t.Errorf("dashboard output lacks the panels or the summary:\n%s", got)
	}
	if strings.Index(got, DemoSummary().Title) < strings.LastIndex(got, "Signal") {
		t.Error("summary printed before the dashboard ended")
	}
}

// TestDashboardPauseAtStart pauses before the program has finished starting; run with -race.
func TestDashboardPauseAtStart(t *testing.T) {
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	r := New(Options{Mode: Dashboard, Out: io.Discard, Err: io.Discard, In: pr}, DemoInfo())
	if err := r.Pause(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := r.Finish(nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestDashboardRendererFailure(t *testing.T) {
	var out bytes.Buffer
	r := New(Options{Mode: Dashboard, Out: &out, Err: io.Discard}, encodeInfo())
	for _, e := range encodeScript() {
		r.Event(e)
	}
	if err := r.Finish(nil, &Failure{Message: "boom", Exit: 1}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "boom") {
		t.Error("the dashboard prints failures; the caller does")
	}
}

// TestDashboardThrottle checks that the renderer forwards every event that changes what is shown
// and drops progress events that change nothing.
func TestDashboardThrottle(t *testing.T) {
	var sent []tea.Msg
	d := &dashboard{send: func(m tea.Msg) { sent = append(sent, m) }}
	d.Event(at(0, qrvt.StagePack, qrvt.KindStarted, 0, 1_000_000, qrvt.UnitBytes))
	for i := int64(1); i <= 1000; i++ {
		d.Event(at(0, qrvt.StagePack, qrvt.KindProgress, i*1000, 1_000_000, qrvt.UnitBytes))
		d.Event(at(0, qrvt.StagePack, qrvt.KindProgress, i*1000+1, 1_000_000, qrvt.UnitBytes))
	}
	d.Event(at(0, qrvt.StagePack, qrvt.KindFinished, 1_000_000, 1_000_000, qrvt.UnitBytes))
	if len(sent) != 1002 {
		t.Errorf("%d messages sent, want 1002 (start, one per permille, finish)", len(sent))
	}
	sent = nil
	st := &qrvt.Stats{Captures: 1}
	for range 10 {
		d.Event(qrvt.Event{Kind: qrvt.KindStats, Stats: st})
	}
	if len(sent) != 1 {
		t.Errorf("%d identical stats forwarded, want 1", len(sent))
	}
}
