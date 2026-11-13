package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	qrvt "github.com/platten/qrvideotransfer"
)

// t0 is the time the scripted jobs start.
var t0 = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

// golden compares got with testdata/golden/<name> at the module root. With QRVT_UPDATE_GOLDEN=1 it
// writes the file first.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("..", "..", "..", "testdata", "golden", name)
	if os.Getenv("QRVT_UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with QRVT_UPDATE_GOLDEN=1 to create it)", err)
	}
	if w := strings.ReplaceAll(string(want), "\r\n", "\n"); w != got {
		t.Errorf("output differs from %s\n--- want\n%s\n--- got\n%s", name, w, got)
	}
}

// at returns an event of the scripted job, s seconds after t0.
func at(s float64, st qrvt.Stage, k qrvt.EventKind, done, total int64, u qrvt.Unit) qrvt.Event {
	return qrvt.Event{Time: t0.Add(time.Duration(s * float64(time.Second))), Stage: st, Kind: k, Done: done, Total: total, Unit: u}
}

// encodeInfo and encodeScript describe a scripted encode.
func encodeInfo() Info {
	return Info{
		Command: "encode",
		Sources: []string{"photos", "notes.txt"},
		Output:  "out.mp4",
		Video:   []Field{{"Size", "1920x1080"}, {"Rate", "12 fps"}, {"Mode", "gray4, cell 4"}, {"Protection", "normal, parity 20%"}},
		Stages:  []qrvt.Stage{qrvt.StagePack, qrvt.StageProtect, qrvt.StageRender, qrvt.StageEncodeVideo},
	}
}

func encodeScript() []qrvt.Event {
	const mb = 1 << 20
	var ev []qrvt.Event
	ev = append(ev, at(0, qrvt.StagePack, qrvt.KindStarted, 0, 12*mb, qrvt.UnitBytes))
	for i := int64(1); i <= 12; i++ {
		ev = append(ev, at(float64(i)/4, qrvt.StagePack, qrvt.KindProgress, i*mb, 12*mb, qrvt.UnitBytes))
	}
	ev = append(ev, at(3, qrvt.StagePack, qrvt.KindFinished, 12*mb, 12*mb, qrvt.UnitBytes),
		at(3, qrvt.StageProtect, qrvt.KindStarted, 0, 10*mb, qrvt.UnitBytes),
		at(4, qrvt.StageProtect, qrvt.KindProgress, 5*mb, 10*mb, qrvt.UnitBytes),
		at(5, qrvt.StageProtect, qrvt.KindFinished, 10*mb, 10*mb, qrvt.UnitBytes),
		at(5, qrvt.StageRender, qrvt.KindStarted, 0, 840, qrvt.UnitFrames),
		at(15, qrvt.StageRender, qrvt.KindProgress, 300, 840, qrvt.UnitFrames),
		qrvt.Event{Time: t0.Add(16 * time.Second), Stage: qrvt.StageRender, Kind: qrvt.KindWarning,
			Warn: &qrvt.Warning{Code: qrvt.WarnDownscaled, Text: "The video was reduced to 1916x1080 to fit the cell size."}},
		at(20, qrvt.StageEncodeVideo, qrvt.KindStarted, 0, 840, qrvt.UnitFrames),
		at(25, qrvt.StageEncodeVideo, qrvt.KindProgress, 120, 840, qrvt.UnitFrames),
	)
	return ev
}
