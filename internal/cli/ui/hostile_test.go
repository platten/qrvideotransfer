package ui

import (
	"encoding/json"
	"strings"
	"testing"

	qrvt "github.com/platten/qrvideotransfer"
)

// hostileJob returns a job whose every string is hostile.
func hostileJob() (Info, []qrvt.Event, *Summary) {
	info := Info{
		Command: "decode" + hostile[0],
		Sources: hostile,
		Output:  strings.Join(hostile, "/"),
		Stages:  []qrvt.Stage{qrvt.StageDecodeVideo, qrvt.StageUnpack},
		Signal:  true,
	}
	sum := &Summary{Title: "Decoded " + hostile[1]}
	for i, h := range hostile {
		info.Video = append(info.Video, Field{h, h})
		sum.Fields = append(sum.Fields, Field{h, hostile[len(hostile)-1-i]})
		sum.Warnings = append(sum.Warnings, qrvt.Warning{Code: qrvt.WarningCode(h), Text: h})
	}
	events := []qrvt.Event{at(0, qrvt.StageDecodeVideo, qrvt.KindStarted, 0, 100, qrvt.UnitFrames)}
	for _, h := range hostile {
		events = append(events, qrvt.Event{Time: t0, Stage: qrvt.StageUnpack, Kind: qrvt.KindWarning,
			Warn: &qrvt.Warning{Code: qrvt.WarnSkippedFile, Text: "skipped " + h}})
		events = append(events, qrvt.Event{Time: t0, Stage: qrvt.StageUnpack, Kind: qrvt.KindWarning,
			Warn: &qrvt.Warning{Code: qrvt.WarningCode(h), Text: h}})
	}
	events = append(events, qrvt.Event{Time: t0, Kind: qrvt.KindStats, Stats: &qrvt.Stats{Captures: 5, BlocksTotal: 4, BlocksReady: 1}})
	return info, events, sum
}

func TestPlainStripsControls(t *testing.T) {
	info, events, sum := hostileJob()
	for _, verbose := range []bool{false, true} {
		out, errOut := play(t, Options{Mode: Plain, Verbose: verbose}, info, events, sum, nil)
		assertClean(t, "plain stdout", out)
		assertClean(t, "plain stderr", errOut)
		if !strings.Contains(errOut, "evil.exe") {
			t.Errorf("the visible part of a hostile name is lost:\n%s", errOut)
		}
	}
}

func TestJSONStripsControls(t *testing.T) {
	info, events, sum := hostileJob()
	for _, f := range []*Failure{nil, {Message: strings.Join(hostile, " "), Guidance: strings.Join(hostile, " "), Exit: 2}} {
		s := sum
		if f != nil {
			s = nil
		}
		out, _ := play(t, Options{Mode: JSON}, info, events, s, f)
		assertClean(t, "json raw", out)
		if strings.Contains(out, `\u001b`) || strings.Contains(out, `‮`) || strings.Contains(out, `\u0000`) {
			t.Errorf("escaped control characters in JSON output:\n%s", out)
		}
		for _, l := range lines(t, out) {
			walkStrings(l, func(s string) { assertClean(t, "json value", s) })
		}
	}
}

// walkStrings calls fn for every string key and value in a decoded JSON value.
func walkStrings(v any, fn func(string)) {
	switch x := v.(type) {
	case string:
		fn(x)
	case []any:
		for _, e := range x {
			walkStrings(e, fn)
		}
	case map[string]any:
		for k, e := range x {
			fn(k)
			walkStrings(e, fn)
		}
	}
}

func TestHostileJSONStaysValid(t *testing.T) {
	info, events, sum := hostileJob()
	out, _ := play(t, Options{Mode: JSON}, info, events, sum, nil)
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if !json.Valid([]byte(l)) {
			t.Errorf("invalid JSON line %q", l)
		}
	}
}
