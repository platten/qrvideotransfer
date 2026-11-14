package ui

import (
	"fmt"
	"time"

	qrvt "github.com/platten/qrvideotransfer"
)

var (
	stageNames = [...]string{"none", "pack", "protect", "render", "encode-video", "decode-video", "demodulate", "repair", "verify", "unpack"}
	kindNames  = [...]string{"unknown", "started", "progress", "finished", "stats", "warning"}
	unitNames  = [...]string{"", "bytes", "frames", "blocks"}
	unitOne    = [...]string{"", "byte", "frame", "block"}
)

// StageName is the stable name of a stage, as the JSON events carry it.
func StageName(s qrvt.Stage) string {
	if int(s) < len(stageNames) {
		return stageNames[s]
	}
	return "unknown"
}

// KindName is the stable name of an event kind.
func KindName(k qrvt.EventKind) string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "unknown"
}

// UnitName is the stable name of a unit; "" for none.
func UnitName(u qrvt.Unit) string {
	if int(u) < len(unitNames) {
		return unitNames[u]
	}
	return ""
}

// FormatBytes formats a byte count with binary units, for example "1.5 MiB".
func FormatBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", max(n, 0))
	}
	v, unit := float64(n)/1024, "KiB"
	for _, u := range []string{"MiB", "GiB", "TiB"} {
		if v < 1024 {
			break
		}
		v, unit = v/1024, u
	}
	return fmt.Sprintf("%.1f %s", v, unit)
}

// FormatDuration formats a duration as h:mm:ss or m:ss, rounded to the second.
func FormatDuration(d time.Duration) string {
	s := int64(max(d.Round(time.Second), 0) / time.Second)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// formatRate formats r units per second.
func formatRate(u qrvt.Unit, r float64) string {
	switch u {
	case qrvt.UnitBytes:
		return FormatBytes(int64(r)) + "/s"
	case qrvt.UnitFrames, qrvt.UnitBlocks:
		return fmt.Sprintf("%.1f %s/s", r, unitNames[u])
	}
	return fmt.Sprintf("%.1f/s", r)
}

// formatAmount formats n units.
func formatAmount(u qrvt.Unit, n int64) string {
	switch u {
	case qrvt.UnitBytes:
		return FormatBytes(n)
	case qrvt.UnitFrames, qrvt.UnitBlocks:
		if n == 1 {
			return "1 " + unitOne[u]
		}
		return fmt.Sprintf("%d %s", n, unitNames[u])
	}
	return fmt.Sprint(n)
}

// stageProgress tracks one stage, for the rate and time remaining that the plain and dashboard
// renderers show. Rates come from the event times, so a scripted job renders the same every time.
type stageProgress struct {
	stage       qrvt.Stage
	unit        qrvt.Unit
	done, total int64
	start, last time.Time // times of the first and latest events
	startDone   int64     // Done at start
	started     bool      // an event has been seen
	finished    bool
}

// update applies an event of this stage.
func (p *stageProgress) update(e qrvt.Event) {
	if !p.started {
		p.started, p.start, p.startDone = true, e.Time, e.Done
	}
	p.done, p.total, p.last = e.Done, e.Total, e.Time
	if e.Unit != 0 {
		p.unit = e.Unit
	}
	if e.Kind == qrvt.KindFinished {
		p.finished = true
	}
}

// fraction is done over total, in [0, 1].
func (p *stageProgress) fraction() float64 {
	if p.finished {
		return 1
	}
	if p.total <= 0 {
		return 0
	}
	return min(max(float64(p.done)/float64(p.total), 0), 1)
}

// rate is units per second since the stage started; 0 when unknown.
func (p *stageProgress) rate() float64 {
	secs := p.last.Sub(p.start).Seconds()
	if secs <= 0 || p.done <= p.startDone {
		return 0
	}
	return float64(p.done-p.startDone) / secs
}

// remaining is the estimated time left; ok is false when it cannot be estimated.
func (p *stageProgress) remaining() (d time.Duration, ok bool) {
	r := p.rate()
	if r <= 0 || p.total <= 0 || p.finished {
		return 0, false
	}
	return time.Duration(float64(max(p.total-p.done, 0)) / r * float64(time.Second)), true
}

// elapsed is the time from the first to the latest event.
func (p *stageProgress) elapsed() time.Duration { return p.last.Sub(p.start) }

// tracker keeps the progress of every stage of a job, in the order the stages appear.
type tracker struct {
	stages []*stageProgress
}

// newTracker starts with the stages expected.
func newTracker(expected []qrvt.Stage) *tracker {
	t := &tracker{}
	for _, s := range expected {
		t.get(s)
	}
	return t
}

// get returns the progress of a stage, adding it when new.
func (t *tracker) get(s qrvt.Stage) *stageProgress {
	for _, p := range t.stages {
		if p.stage == s {
			return p
		}
	}
	p := &stageProgress{stage: s}
	t.stages = append(t.stages, p)
	return p
}

// update applies a Started, Progress or Finished event and returns its stage.
func (t *tracker) update(e qrvt.Event) *stageProgress {
	p := t.get(e.Stage)
	p.update(e)
	return p
}
