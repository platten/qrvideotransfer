package payload

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The hostile corpus of SECURITY.md section 9.2 lives in testdata/hostile/ at the module's root:
// each fixture names its input and the exact error it must produce. This file runs the paths,
// manifests, archives and stream headers; internal/frame runs the descriptors and frame headers.

// hostileFixture reads testdata/hostile/name into v.
func hostileFixture(t *testing.T, name string, v any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "hostile", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// wantUnsafe fails unless err is an UnsafeArchiveError for path with exactly reason.
func wantUnsafe(t *testing.T, name string, err error, path, reason string) {
	t.Helper()
	var u *UnsafeArchiveError
	if !errors.As(err, &u) || u.Path != path || u.Reason != reason {
		t.Errorf("%s: got %v, want %q refused for %q", name, err, path, reason)
	}
}

// TestHostilePaths: every path of paths.json is refused with its reason, alone and in a manifest.
func TestHostilePaths(t *testing.T) {
	var cases []struct {
		Path   string `json:"path"`
		Repeat int    `json:"repeat"`
		Reason string `json:"reason"`
	}
	hostileFixture(t, "paths.json", &cases)
	for _, c := range cases {
		p := c.Path
		if c.Repeat > 0 {
			p = strings.Repeat(p, c.Repeat) + "a"
		}
		wantUnsafe(t, strconv.Quote(p), ValidatePath(p), p, c.Reason)
		wantUnsafe(t, strconv.Quote(p), ValidateManifest([]Entry{entry(p, false)}, 1), p, c.Reason)
	}
}

// corpusEntries turns a fixture's [path, "file"|"dir", data] triples into manifest entries.
func corpusEntries(es [][]string) []Entry {
	var out []Entry
	for _, e := range es {
		x := entry(e[0], e[1] == "dir")
		if len(e) > 2 {
			x.Size, x.SHA256 = int64(len(e[2])), sha256.Sum256([]byte(e[2]))
		}
		out = append(out, x)
	}
	return out
}

// TestHostileManifests: every manifest of manifests.json is refused at the entry named, with its
// reason.
func TestHostileManifests(t *testing.T) {
	var cases []struct {
		Name    string     `json:"name"`
		Entries [][]string `json:"entries"`
		Path    string     `json:"path"`
		Reason  string     `json:"reason"`
	}
	hostileFixture(t, "manifests.json", &cases)
	for _, c := range cases {
		wantUnsafe(t, c.Name, ValidateManifest(corpusEntries(c.Entries), 1<<20), c.Path, c.Reason)
	}
}

// TestHostileArchives: every archive of archives.json is refused at the entry named, with its
// reason, and nothing is written.
func TestHostileArchives(t *testing.T) {
	var cases []struct {
		Name     string     `json:"name"`
		Manifest [][]string `json:"manifest"`
		Tar      []struct {
			Type, Name, Data, Link string
		} `json:"tar"`
		Path   string `json:"path"`
		Reason string `json:"reason"`
	}
	hostileFixture(t, "archives.json", &cases)
	types := map[string]byte{"reg": tar.TypeReg, "dir": tar.TypeDir, "symlink": tar.TypeSymlink, "link": tar.TypeLink,
		"char": tar.TypeChar, "block": tar.TypeBlock, "fifo": tar.TypeFifo}
	for _, c := range cases {
		var es []tarEntry
		for _, e := range c.Tar {
			typ, ok := types[e.Type]
			if !ok {
				t.Fatalf("%s: unknown tar type %q", c.Name, e.Type)
			}
			es = append(es, tarEntry{tar.Header{Typeflag: typ, Name: e.Name, Linkname: e.Link, Size: int64(len(e.Data)),
				Mode: 0o644, ModTime: testTime, Format: tar.FormatPAX}, e.Data})
		}
		a := archive(t, marshal(corpusEntries(c.Manifest)), es...)
		out := t.TempDir()
		_, err := unpackArchive(context.Background(), bytes.NewReader(a), int64(len(a)), UnpackOptions{OutputDir: out})
		wantUnsafe(t, c.Name, err, c.Path, c.Reason)
		expectEmpty(t, out)
	}
}

// TestHostileStreamHeaders: every change of stream-headers.json to a valid header is refused as
// named: "format" (ErrFormat), "unsupported:<what>:<value>" or "limit:<name>".
func TestHostileStreamHeaders(t *testing.T) {
	var corpus struct {
		Base  string `json:"base"`
		Cases []struct {
			Name  string `json:"name"`
			Byte  int    `json:"byte"`
			Xor   byte   `json:"xor"`
			Set   string `json:"set"`
			Error string `json:"error"`
		} `json:"cases"`
	}
	hostileFixture(t, "stream-headers.json", &corpus)
	base, err := hex.DecodeString(corpus.Base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseHeader(base); err != nil {
		t.Fatalf("the base header is refused: %v", err)
	}
	for _, c := range corpus.Cases {
		b := bytes.Clone(base)
		b[c.Byte] ^= c.Xor
		if c.Set != "" {
			s, err := hex.DecodeString(c.Set)
			if err != nil {
				t.Fatal(err)
			}
			copy(b[c.Byte:], s)
		}
		_, err := ParseHeader(b)
		kind, rest, _ := strings.Cut(c.Error, ":")
		var (
			version *UnsupportedVersionError
			limit   *LimitError
			ok      bool
		)
		switch kind {
		case "format":
			ok = errors.Is(err, ErrFormat)
		case "unsupported":
			what, v, _ := strings.Cut(rest, ":")
			ok = errors.As(err, &version) && version.What == what && strconv.FormatInt(version.Value, 10) == v
		case "limit":
			ok = errors.As(err, &limit) && limit.Limit == rest
		default:
			t.Fatalf("%s: unknown expected error %q", c.Name, c.Error)
		}
		if !ok {
			t.Errorf("%s: got %v, want %s", c.Name, err, c.Error)
		}
	}
}
