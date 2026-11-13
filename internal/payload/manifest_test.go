package payload

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidatePath(t *testing.T) {
	ok := []string{
		"a", "a/b.txt", "docs/ünïcode.txt", "a b/c", ".hidden", "a.b.c", "CONSOLE", "com0", "lpt10",
		"COM1x", "auxiliary.txt", "x/nulx", strings.Repeat("a", 255), strings.Repeat("a/", 2047) + "aa",
		"日本語/ファイル", "semi;colon", "a~b",
	}
	for _, p := range ok {
		if err := ValidatePath(p); err != nil {
			t.Errorf("ValidatePath(%q) = %v, want nil", p, err)
		}
	}
	bad := []string{
		"", "/a", "a/", "a//b", ".", "./a", "a/./b", "..", "../a", "a/..", "a/../b",
		"a\\b", "c:x", "C:", "a:b/c", "a\x00b", "a\x01", "\x1f", "a\x7fb", "a\x1b[31m",
		"a\xffb", "\xc3", "é", // invalid UTF-8; NFD
		"a ", "a /b", "a.", "a./b", "...", "x/b.",
		"CON", "con", "Con.txt", "prn", "AUX", "nul", "NUL.tar.gz", "x/com1", "COM9.x", "lpt1", "LPT9",
		strings.Repeat("a", 256), strings.Repeat("a/", 2048) + "a",
	}
	for _, p := range bad {
		err := ValidatePath(p)
		if !isUnsafe(err) {
			t.Errorf("ValidatePath(%q) = %v, want an UnsafeArchiveError", p, err)
		}
	}
}

func entry(p string, dir bool) Entry {
	e := Entry{Path: p, Dir: dir, Mode: 0o644, MTime: testTime}
	if dir {
		e.Mode = 0o755
	}
	return e
}

func TestValidateManifest(t *testing.T) {
	good := []Entry{entry("a", true), entry("a/b", false), entry("a/c", true), entry("a/c/d", false), entry("ß", false)}
	if err := ValidateManifest(good, 100); err != nil {
		t.Fatalf("ValidateManifest: %v", err)
	}
	cases := map[string][]Entry{
		"bad path":             {entry("a/../b", false)},
		"duplicate":            {entry("a", false), entry("a", false)},
		"file and dir":         {entry("a", true), entry("a", false)},
		"case":                 {entry("README", false), entry("readme", false)},
		"full case folding":    {entry("STRASSE", false), entry("straße", false)},
		"kelvin sign":          {entry("K", false), entry("k", false)},
		"case in parent":       {entry("A/x", false), entry("a/x", false)},
		"file under file":      {entry("a", false), entry("a/b", false)},
		"deep file under file": {entry("a", false), entry("a/b/c", false)},
		"under file, folded":   {entry("A", false), entry("a/b", false)},
	}
	for name, es := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateManifest(es, 100); !isUnsafe(err) {
				t.Fatalf("ValidateManifest = %v, want an UnsafeArchiveError", err)
			}
		})
	}
	big := []Entry{entry("a", false), entry("b", false)}
	big[0].Size, big[1].Size = 60, 41
	if err := ValidateManifest(big, 100); !isLimit("unpacked size")(err) {
		t.Fatalf("over max size: %v", err)
	}
	big[1].Size = 40
	if err := ValidateManifest(big, 100); err != nil {
		t.Fatalf("at max size: %v", err)
	}
	big[1].Size = -1
	if err := ValidateManifest(big, 100); !isUnsafe(err) {
		t.Fatalf("negative size: %v", err)
	}
}

func TestManifestRoundTrip(t *testing.T) {
	es := []Entry{
		{Path: "docs", Dir: true, Mode: 0o755, MTime: testTime},
		{Path: "docs/a.txt", Size: 1234, Mode: 0o644, MTime: testTime, SHA256: sha256.Sum256([]byte("x"))},
		{Path: "e<&>\"q", Size: 0, Mode: 0o600, MTime: testTime.Add(time.Hour), SHA256: sha256.Sum256(nil)},
	}
	want := `{"version":1,"entries":[{"path":"docs","type":"dir","mode":493,"mtime":"2026-11-13T10:00:00Z"},` +
		`{"path":"docs/a.txt","type":"file","size":1234,"mode":420,"mtime":"2026-11-13T10:00:00Z","sha256":"2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881"},` +
		`{"path":"e<&>\"q","type":"file","size":0,"mode":384,"mtime":"2026-11-13T11:00:00Z","sha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}]}`
	b := MarshalManifest(es)
	if string(b) != want {
		t.Fatalf("MarshalManifest =\n%s\nwant\n%s", b, want)
	}
	got, err := ParseManifest(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(es) {
		t.Fatalf("got %d entries", len(got))
	}
	for i := range es {
		if !got[i].MTime.Equal(es[i].MTime) {
			t.Fatalf("entry %d mtime %v", i, got[i].MTime)
		}
		got[i].MTime = es[i].MTime
		if got[i] != es[i] {
			t.Fatalf("entry %d = %+v, want %+v", i, got[i], es[i])
		}
	}
	if !bytes.Equal(MarshalManifest(got), b) {
		t.Fatal("re-marshalled manifest differs")
	}
	if es, err := ParseManifest([]byte(`{"entries":[],"version":1}`)); err != nil || len(es) != 0 {
		t.Fatalf("empty manifest, keys reordered: %v %v", es, err)
	}
}

func TestParseManifestRejects(t *testing.T) {
	const h = `"sha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"`
	const f = `{"path":"a","type":"file","size":0,"mode":420,"mtime":"2026-11-13T10:00:00Z",` + h + `}`
	wrap := func(e string) string { return `{"version":1,"entries":[` + e + `]}` }
	if _, err := ParseManifest([]byte(wrap(f))); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	cases := map[string]string{
		"not JSON":          `{`,
		"empty":             ``,
		"array":             `[]`,
		"trailing":          wrap(f) + `{}`,
		"trailing garbage":  wrap(f) + `x`,
		"invalid UTF-8":     wrap(strings.Replace(f, `"a"`, "\"a\xff\"", 1)),
		"no version":        `{"entries":[]}`,
		"no entries":        `{"version":1}`,
		"extra top key":     `{"version":1,"entries":[],"x":1}`,
		"duplicate key":     `{"version":1,"version":1,"entries":[]}`,
		"key case":          `{"Version":1,"entries":[]}`,
		"version string":    `{"version":"1","entries":[]}`,
		"version float":     `{"version":1.0,"entries":[]}`,
		"entries null":      `{"version":1,"entries":null}`,
		"entry not object":  wrap(`1`),
		"unknown entry key": wrap(strings.Replace(f, `"size"`, `"uid":0,"size"`, 1)),
		"dup entry key":     wrap(strings.Replace(f, `"size":0`, `"size":0,"size":0`, 1)),
		"type link":         wrap(strings.Replace(f, `"file"`, `"link"`, 1)),
		"no type":           wrap(strings.Replace(f, `"type":"file",`, ``, 1)),
		"no path":           wrap(strings.Replace(f, `"path":"a",`, ``, 1)),
		"no mode":           wrap(strings.Replace(f, `"mode":420,`, ``, 1)),
		"no mtime":          wrap(strings.Replace(f, `,"mtime":"2026-11-13T10:00:00Z"`, ``, 1)),
		"file no size":      wrap(strings.Replace(f, `"size":0,`, ``, 1)),
		"file no sha":       wrap(strings.Replace(f, ","+h, ``, 1)),
		"dir with size":     wrap(`{"path":"a","type":"dir","size":0,"mode":493,"mtime":"2026-11-13T10:00:00Z"}`),
		"dir with sha":      wrap(`{"path":"a","type":"dir","mode":493,"mtime":"2026-11-13T10:00:00Z",` + h + `}`),
		"size negative":     wrap(strings.Replace(f, `"size":0`, `"size":-1`, 1)),
		"size float":        wrap(strings.Replace(f, `"size":0`, `"size":0.5`, 1)),
		"size exponent":     wrap(strings.Replace(f, `"size":0`, `"size":1e3`, 1)),
		"size huge":         wrap(strings.Replace(f, `"size":0`, `"size":99999999999999999999`, 1)),
		"size null":         wrap(strings.Replace(f, `"size":0`, `"size":null`, 1)),
		"mode negative":     wrap(strings.Replace(f, `"mode":420`, `"mode":-1`, 1)),
		"mode huge":         wrap(strings.Replace(f, `"mode":420`, `"mode":4294967296`, 1)),
		"mode string":       wrap(strings.Replace(f, `"mode":420`, `"mode":"420"`, 1)),
		"mtime bad":         wrap(strings.Replace(f, `2026-11-13T10:00:00Z`, `yesterday`, 1)),
		"mtime number":      wrap(strings.Replace(f, `"2026-11-13T10:00:00Z"`, `0`, 1)),
		"sha short":         wrap(strings.Replace(f, `b855"`, `b85"`, 1)),
		"sha not hex":       wrap(strings.Replace(f, `b855"`, `b85g"`, 1)),
		"sha upper":         wrap(strings.Replace(f, `e3b0`, `E3B0`, 1)),
		"path number":       wrap(strings.Replace(f, `"path":"a"`, `"path":1`, 1)),
		"path nested":       wrap(strings.Replace(f, `"path":"a"`, `"path":{"a":1}`, 1)),
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseManifest([]byte(m)); !isFormat(err) {
				t.Fatalf("ParseManifest(%s) = %v, want ErrFormat", m, err)
			}
		})
	}
	for _, v := range []string{"0", "2"} {
		_, err := ParseManifest([]byte(`{"version":` + v + `,"entries":[]}`))
		var u *UnsupportedVersionError
		if !errors.As(err, &u) || u.What != "manifest version" {
			t.Fatalf("version %s: %v", v, err)
		}
	}
}

func TestManifestEntryLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 67 MB manifest; skipped with -short")
	}
	const e = `{"path":"a","type":"dir","mode":0,"mtime":"2026-11-13T10:00:00Z"},`
	var b bytes.Buffer
	b.WriteString(`{"version":1,"entries":[`)
	for range MaxManifestEntries + 1 {
		b.WriteString(e)
	}
	m := b.Bytes()
	m[len(m)-1] = ']'
	m = append(m, '}')
	if len(m) > MaxManifestBytes {
		t.Fatalf("test manifest is %d bytes, over the byte limit", len(m))
	}
	_, err := ParseManifest(m)
	var l *LimitError
	if !errors.As(err, &l) || l.Limit != "manifest entries" || l.Max != MaxManifestEntries {
		t.Fatalf("ParseManifest: %v", err)
	}
}

func TestManifestByteLimit(t *testing.T) {
	_, err := ParseManifest(make([]byte, MaxManifestBytes+1))
	var l *LimitError
	if !errors.As(err, &l) || l.Limit != "manifest size" || l.Max != MaxManifestBytes {
		t.Fatalf("ParseManifest: %v", err)
	}
}

func FuzzManifest(f *testing.F) {
	f.Add(MarshalManifest([]Entry{entry("a", true), {Path: "a/b", Size: 3, Mode: 0o644, MTime: testTime}}))
	f.Add([]byte(`{"version":1,"entries":[]}`))
	f.Add([]byte(`{"version":1,"entries":[{"path":"A","type":"dir","mode":1,"mtime":"2026-11-13T10:00:00+01:00"}]}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		es, err := ParseManifest(b)
		if err != nil {
			return
		}
		if len(es) > MaxManifestEntries {
			t.Fatal("too many entries accepted")
		}
		for _, e := range es {
			if e.Size < 0 || (e.Dir && (e.Size != 0 || e.SHA256 != [32]byte{})) {
				t.Fatalf("bad entry accepted: %+v", e)
			}
		}
		if ValidateManifest(es, DefaultMaxSize) != nil {
			return
		}
		again, err := ParseManifest(MarshalManifest(es))
		if err != nil || len(again) != len(es) {
			t.Fatalf("re-parse: %v", err)
		}
	})
}
