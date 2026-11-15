package payload

import (
	"errors"
	"io/fs"
	"math"
	"strings"
	"testing"
	"time"
)

// The encoder never writes a manifest its decoder refuses (FORMAT.md section 12, "What an encoder
// writes"): these tests hold MarshalManifest to ParseManifest field by field.

var (
	minMTime = time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC)
	maxMTime = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
)

// TestManifestFuzzMTime is the input FuzzManifest found (testdata/fuzz/FuzzManifest/0ead51c862907840):
// year 0000 at +01:00 is -0001-12-31T23:00:00Z, which the parser accepts and RFC 3339 cannot write.
// The encoder clamps it to the earliest time it can write.
func TestManifestFuzzMTime(t *testing.T) {
	in := `{"version":1,"entries":[{"path":"a","type":"dir","mode":0,"mtime":"0000-01-01T0:00:00+01:00"}]}`
	es, err := ParseManifest([]byte(in))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	m, err := MarshalManifest(es)
	if err != nil {
		t.Fatalf("MarshalManifest: %v", err)
	}
	want := `{"version":1,"entries":[{"path":"a","type":"dir","mode":0,"mtime":"0000-01-01T00:00:00Z"}]}`
	if string(m) != want {
		t.Fatalf("MarshalManifest = %s, want %s", m, want)
	}
	if _, err := ParseManifest(m); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
}

func TestClampMTime(t *testing.T) {
	cet := time.FixedZone("CET", 3600)
	for _, c := range []struct {
		in, want time.Time
		clamped  bool
	}{
		{testTime, testTime, false},
		{testTime.Add(999 * time.Millisecond).In(cet), testTime, false},
		{minMTime, minMTime, false},
		{maxMTime, maxMTime, false},
		{maxMTime.Add(999 * time.Millisecond), maxMTime, false},
		{time.Time{}, time.Time{}, false}, // year 1
		{minMTime.Add(-time.Second), minMTime, true},
		{minMTime.Add(-time.Nanosecond), minMTime, true},
		{time.Date(0, 1, 1, 0, 0, 0, 0, cet), minMTime, true},
		{maxMTime.Add(time.Second), maxMTime, true},
		{time.Date(9999, 12, 31, 23, 59, 59, 0, time.FixedZone("", -3600)), maxMTime, true},
		{time.Unix(math.MinInt64/2, 0), minMTime, true},
		{time.Unix(math.MaxInt64/2, 0), maxMTime, true},
	} {
		got, clamped := clampMTime(c.in)
		if !got.Equal(c.want) || got.Location() != time.UTC || clamped != c.clamped {
			t.Errorf("clampMTime(%v) = %v, %v; want %v, %v", c.in, got, clamped, c.want, c.clamped)
		}
		m, err := MarshalManifest([]Entry{{Path: "a", Dir: true, MTime: c.in}})
		if err != nil {
			t.Fatalf("MarshalManifest(%v): %v", c.in, err)
		}
		es, err := ParseManifest(m)
		if err != nil || !es[0].MTime.Equal(c.want) {
			t.Errorf("mtime %v: wrote %s, parsed %v, %v", c.in, m, es, err)
		}
	}
}

// TestMarshalManifestRefuses: a field the parser would refuse, or a path JSON encoding would change,
// stops MarshalManifest with an InputError naming the entry.
func TestMarshalManifestRefuses(t *testing.T) {
	file := Entry{Path: "ok", Size: 1, Mode: 0o644, MTime: testTime}
	for name, c := range map[string]struct {
		e      Entry
		reason string
	}{
		"negative size":  {Entry{Path: "neg", Size: -1, Mode: 0o644, MTime: testTime}, "negative size"},
		"invalid UTF-8":  {Entry{Path: "a\xffb", Mode: 0o644, MTime: testTime}, "not valid UTF-8"},
		"invalid in dir": {Entry{Path: "d\xc3", Dir: true, Mode: 0o755, MTime: testTime}, "not valid UTF-8"},
	} {
		t.Run(name, func(t *testing.T) {
			m, err := MarshalManifest([]Entry{file, c.e})
			var ie *InputError
			if !errors.As(err, &ie) || ie.Path != c.e.Path || !strings.Contains(ie.Reason, c.reason) || m != nil {
				t.Fatalf("MarshalManifest = %q, %v; want an InputError for %q: %s", m, err, c.e.Path, c.reason)
			}
		})
	}
	// A folder's size is not written, so it cannot be refused; mode is written as its low nine bits.
	m, err := MarshalManifest([]Entry{{Path: "d", Dir: true, Size: -1, Mode: math.MaxUint32, MTime: testTime}})
	if err != nil {
		t.Fatalf("folder with a size: %v", err)
	}
	if es, err := ParseManifest(m); err != nil || es[0].Mode != 0o777 {
		t.Fatalf("re-parse %s: %v %v", m, es, err)
	}
}

// fakeInfo is an fs.FileInfo with a chosen modification time.
type fakeInfo struct {
	mode  fs.FileMode
	mtime time.Time
}

func (f fakeInfo) Name() string       { return "f" }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return f.mtime }
func (f fakeInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeInfo) Sys() any           { return nil }

// TestNewEntryClampsMTime: an input whose modification time is outside years 0000-9999 is packed with
// the clamped time and a warning naming it, never refused and never silently changed.
func TestNewEntryClampsMTime(t *testing.T) {
	e, w := newEntry("d/f", fakeInfo{0o640, testTime.Add(500 * time.Millisecond)})
	if w != nil || !e.MTime.Equal(testTime) || e.Mode != 0o640 || e.Path != "d/f" {
		t.Fatalf("in range: %+v, %+v", e, w)
	}
	far := time.Date(30828, 9, 14, 2, 48, 5, 0, time.UTC) // the largest Windows FILETIME
	e, w = newEntry("d/f", fakeInfo{fs.ModeDir | 0o755, far})
	if w == nil || w.Code != WarnMTimeClamped || w.Path != "d/f" || !strings.Contains(w.Text, "9999-12-31T23:59:59Z") ||
		!strings.Contains(w.Text, "30828") || !e.MTime.Equal(maxMTime) {
		t.Fatalf("year 30828: %+v, %+v", e, w)
	}
	e, w = newEntry("old", fakeInfo{0o644, time.Date(-5, 1, 1, 0, 0, 0, 0, time.UTC)})
	if w == nil || w.Path != "old" || !e.MTime.Equal(minMTime) {
		t.Fatalf("year -5: %+v, %+v", e, w)
	}
}
