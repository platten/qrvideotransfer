package ui

import (
	tea "charm.land/bubbletea/v2"
	qrvt "github.com/platten/qrvideotransfer"
)

// eventMsg carries one event into the dashboard model.
type eventMsg qrvt.Event

// doneMsg tells the dashboard model that the job has finished.
type doneMsg struct{}

// model is the dashboard's Bubble Tea model.
type model struct {
	width    int
	stopping bool
}

func newModel(info Info, verbose bool, cancel func(), width int) *model { return &model{} }

// Init implements tea.Model.
func (m *model) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) { return m, nil }

// View implements tea.Model.
func (m *model) View() tea.View { return tea.NewView("") }

// dashboard runs the model in a Bubble Tea program.
type dashboard struct {
	send func(tea.Msg)
}

func newDashboard(o Options, info Info) *dashboard { return &dashboard{} }

// Event implements Renderer.
func (d *dashboard) Event(e qrvt.Event) {}

// Pause implements Renderer.
func (d *dashboard) Pause(fn func() error) error { return nil }

// Finish implements Renderer.
func (d *dashboard) Finish(s *Summary, f *Failure) error { return nil }
