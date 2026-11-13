package ui

import (
	"testing"

	qrvt "github.com/platten/qrvideotransfer"
)

func TestDemoScript(t *testing.T) {
	info, ev, sum := DemoInfo(), DemoEvents(t0), DemoSummary()
	if info.Command != "decode" || !info.Signal || len(info.Stages) == 0 || sum == nil || sum.Title == "" {
		t.Fatalf("demo info %+v, summary %+v", info, sum)
	}
	if len(ev) < 50 {
		t.Fatalf("only %d demo events", len(ev))
	}
	seen := map[qrvt.EventKind]bool{}
	for i, e := range ev {
		seen[e.Kind] = true
		if i > 0 && e.Time.Before(ev[i-1].Time) {
			t.Errorf("event %d goes back in time", i)
		}
		if e.Kind == qrvt.KindStats && e.Stats == nil || e.Kind == qrvt.KindWarning && e.Warn == nil {
			t.Errorf("event %d lacks its payload", i)
		}
	}
	for k := qrvt.KindStarted; k <= qrvt.KindWarning; k++ {
		if !seen[k] {
			t.Errorf("no %s event in the demo", KindName(k))
		}
	}
	if last := ev[len(ev)-1]; last.Kind != qrvt.KindFinished || last.Stage != qrvt.StageUnpack {
		t.Errorf("demo ends with %+v", last)
	}
}
