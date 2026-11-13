package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"

	qrvt "github.com/platten/qrvideotransfer"
)

// schema is the committed JSON Schema of the --json output, parsed.
func schema(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile("events.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// validate checks v against the subset of JSON Schema 2020-12 that events.schema.json uses: $ref
// to #/$defs, oneOf, type, const, enum, required, properties, additionalProperties false, items,
// minimum and maximum. Annotations ($schema, $id, title, description, format) are ignored.
func validate(root, s map[string]any, v any, path string) error {
	if ref, ok := s["$ref"].(string); ok {
		name, found := strings.CutPrefix(ref, "#/$defs/")
		def, ok := root["$defs"].(map[string]any)[name].(map[string]any)
		if !found || !ok {
			return fmt.Errorf("%s: unresolvable $ref %q", path, ref)
		}
		return validate(root, def, v, path)
	}
	if alts, ok := s["oneOf"].([]any); ok {
		n := 0
		var errs []string
		for _, a := range alts {
			if err := validate(root, a.(map[string]any), v, path); err != nil {
				errs = append(errs, err.Error())
			} else {
				n++
			}
		}
		if n != 1 {
			return fmt.Errorf("%s: matches %d of oneOf (%s)", path, n, strings.Join(errs, "; "))
		}
	}
	if c, ok := s["const"]; ok && c != v {
		return fmt.Errorf("%s: %v is not %v", path, v, c)
	}
	if e, ok := s["enum"].([]any); ok {
		found := false
		for _, x := range e {
			found = found || x == v
		}
		if !found {
			return fmt.Errorf("%s: %v not in %v", path, v, e)
		}
	}
	if ty, ok := s["type"].(string); ok {
		okType := false
		switch x := v.(type) {
		case string:
			okType = ty == "string"
		case bool:
			okType = ty == "boolean"
		case float64:
			okType = ty == "number" || ty == "integer" && x == float64(int64(x))
		case map[string]any:
			okType = ty == "object"
		case []any:
			okType = ty == "array"
		case nil:
			okType = ty == "null"
		}
		if !okType {
			return fmt.Errorf("%s: %v is not of type %s", path, v, ty)
		}
	}
	if x, ok := v.(float64); ok {
		if m, ok := s["minimum"].(float64); ok && x < m {
			return fmt.Errorf("%s: %v below minimum %v", path, x, m)
		}
		if m, ok := s["maximum"].(float64); ok && x > m {
			return fmt.Errorf("%s: %v above maximum %v", path, x, m)
		}
	}
	if obj, ok := v.(map[string]any); ok {
		props, _ := s["properties"].(map[string]any)
		for _, r := range asSlice(s["required"]) {
			if _, ok := obj[r.(string)]; !ok {
				return fmt.Errorf("%s: missing %q", path, r)
			}
		}
		for k, x := range obj {
			ps, ok := props[k].(map[string]any)
			if !ok {
				if s["additionalProperties"] == false {
					return fmt.Errorf("%s: unexpected property %q", path, k)
				}
				continue
			}
			if err := validate(root, ps, x, path+"."+k); err != nil {
				return err
			}
		}
	}
	if arr, ok := v.([]any); ok {
		if items, ok := s["items"].(map[string]any); ok {
			for i, x := range arr {
				if err := validate(root, items, x, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func asSlice(v any) []any { s, _ := v.([]any); return s }

// lines decodes NDJSON output, failing on a line that is not one JSON object.
func lines(t *testing.T, out string) []map[string]any {
	t.Helper()
	var r []map[string]any
	for i, l := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("line %d %q: %v", i+1, l, err)
		}
		r = append(r, m)
	}
	return r
}

func TestJSONValidatesAgainstSchema(t *testing.T) {
	root := schema(t)
	events := append(DemoEvents(t0), encodeScript()...)
	events = append(events, qrvt.Event{Time: t0, Kind: qrvt.KindWarning, Warn: &qrvt.Warning{Code: qrvt.WarnSkippedFile, Text: "x"}},
		qrvt.Event{Time: t0, Stage: 77, Kind: 9, Done: 1, Total: 2, Unit: 5})
	for _, f := range []*Failure{nil, {Message: "not enough data", Guidance: "record one more loop", Exit: 3}} {
		var sum *Summary
		if f == nil {
			sum = DemoSummary()
		}
		out, errOut := play(t, Options{Mode: JSON}, DemoInfo(), events, sum, f)
		if errOut != "" {
			t.Errorf("JSON mode wrote to stderr: %q", errOut)
		}
		ls := lines(t, out)
		if len(ls) != len(events)+2 {
			t.Fatalf("%d lines for %d events", len(ls), len(events))
		}
		for i, l := range ls {
			if err := validate(root, root, l, fmt.Sprintf("line %d", i+1)); err != nil {
				t.Error(err)
			}
		}
		last := ls[len(ls)-1]["type"]
		if ls[0]["type"] != "start" || (f == nil) != (last == "summary") || (f != nil) != (last == "error") {
			t.Errorf("first line %v, last line %v", ls[0]["type"], last)
		}
	}
}

func TestJSONGolden(t *testing.T) {
	out, _ := play(t, Options{Mode: JSON}, DemoInfo(), DemoEvents(t0), DemoSummary(), nil)
	golden(t, "cli-events.ndjson", out)
}

func TestJSONFinishNothing(t *testing.T) {
	out, _ := play(t, Options{Mode: JSON}, Info{Command: "inspect"}, nil, nil, nil)
	if got := len(lines(t, out)); got != 1 {
		t.Errorf("%d lines, want only the start line", got)
	}
}

// TestValidator makes sure the schema check above rejects what it should.
func TestValidator(t *testing.T) {
	root := schema(t)
	good := `{"type":"event","time":"2026-11-13T12:00:00Z","stage":"pack","kind":"progress","done":1,"total":2,"unit":"bytes"}`
	bad := []string{
		`{"type":"event","time":"2026-11-13T12:00:00Z","stage":"pack","kind":"progress","done":1}`,
		`{"type":"event","time":"2026-11-13T12:00:00Z","stage":"pack","kind":"progress","done":1,"total":2,"extra":1}`,
		`{"type":"event","time":"2026-11-13T12:00:00Z","stage":"bogus","kind":"progress","done":1,"total":2}`,
		`{"type":"event","time":"2026-11-13T12:00:00Z","stage":"pack","kind":"progress","done":-1,"total":2}`,
		`{"type":"event","time":"2026-11-13T12:00:00Z","stage":"pack","kind":"progress","done":1.5,"total":2}`,
		`{"type":"event","time":5,"stage":"pack","kind":"progress","done":1,"total":2}`,
		`{"type":"start","command":"x","sources":[1]}`,
		`{"type":"nope"}`,
		`{"type":"error","message":"m","exit":0}`,
		`{"type":"event","time":"t","stage":"pack","kind":"stats","done":0,"total":0,"stats":{"captures":1,"frames_seen":1,"frames_total":1,"clean_fraction":1.5,"raw_bit_errors":0,"codewords_ok":0,"blocks_ready":0,"blocks_total":0}}`,
		`[1]`,
	}
	var v any
	_ = json.Unmarshal([]byte(good), &v)
	if err := validate(root, root, v, "good"); err != nil {
		t.Errorf("good event rejected: %v", err)
	}
	for _, b := range bad {
		var v any
		if err := json.Unmarshal([]byte(b), &v); err != nil {
			t.Fatal(err)
		}
		if validate(root, root, v, "bad") == nil {
			t.Errorf("accepted %s", b)
		}
	}
	if validate(root, map[string]any{"$ref": "#/$defs/missing"}, 1, "ref") == nil {
		t.Error("accepted an unresolvable $ref")
	}
}

func TestJSONEncodesEveryString(t *testing.T) {
	var b bytes.Buffer
	r := New(Options{Mode: JSON, Out: &b}, Info{Command: "c", Sources: []string{"a\"b"}})
	_ = r.Finish(nil, nil)
	if !strings.Contains(b.String(), `"sources":["a\"b"]`) {
		t.Errorf("got %s", b.String())
	}
}

// TestJSONClamps checks that values outside the schema are clamped, not printed or turned into an
// encoding failure.
func TestJSONClamps(t *testing.T) {
	root := schema(t)
	nan, inf := math.NaN(), math.Inf(1)
	events := []qrvt.Event{
		{Time: t0, Stage: qrvt.StagePack, Kind: qrvt.KindProgress, Done: -5, Total: -1, Unit: qrvt.UnitBytes},
		{Time: t0, Kind: qrvt.KindStats, Stats: &qrvt.Stats{Captures: -1, FramesSeen: -2, FramesTotal: -3, CleanFraction: nan,
			RawBitErrors: inf, CodewordsOK: -4, BlocksReady: -5, BlocksTotal: -6}},
		{Time: t0, Kind: qrvt.KindStats, Stats: &qrvt.Stats{CleanFraction: 1.5, RawBitErrors: -0.5}},
		{Time: t0, Kind: qrvt.KindStats, Stats: &qrvt.Stats{CleanFraction: math.Inf(-1), RawBitErrors: nan}},
	}
	var ob bytes.Buffer
	r := New(Options{Mode: JSON, Out: &ob}, Info{Command: "decode"})
	for _, e := range events {
		r.Event(e)
	}
	if err := r.Finish(DemoSummary(), nil); err != nil {
		t.Fatalf("Finish = %v", err)
	}
	out := ob.String()
	ls := lines(t, out)
	if len(ls) != len(events)+2 {
		t.Fatalf("%d lines for %d events:\n%s", len(ls), len(events), out)
	}
	for i, l := range ls {
		if err := validate(root, root, l, fmt.Sprintf("line %d", i+1)); err != nil {
			t.Error(err)
		}
	}
	if !strings.Contains(out, `"clean_fraction":1,`) || !strings.Contains(out, `"done":0,"total":0`) {
		t.Errorf("not clamped:\n%s", out)
	}
}
