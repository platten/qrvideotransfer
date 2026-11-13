package payload

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

var update = flag.Bool("update", false, "rewrite testdata/golden/stream.json (a format change: review by hand)")

const goldenFile = "../../testdata/golden/stream.json"

type goldenInput struct {
	Path    string `json:"path"` // ends in "/" for a directory
	Mode    uint32 `json:"mode"`
	Content string `json:"content,omitempty"`
}

type goldenStream struct {
	Stream string `json:"stream"`
	SHA256 string `json:"sha256"`
}

type goldenDecode struct {
	Name       string `json:"name"`
	Passphrase string `json:"passphrase,omitempty"`
	Stream     string `json:"stream"`
}

// goldenManifest is a manifest a decoder must refuse, or one at the edge of a rule that it must accept.
type goldenManifest struct {
	Rule        string `json:"rule"`
	Manifest    string `json:"manifest,omitempty"`
	ManifestHex string `json:"manifest_hex,omitempty"` // a manifest that is not valid UTF-8
}

// bytes returns the manifest's bytes.
func (r goldenManifest) bytes(t *testing.T) []byte {
	if r.ManifestHex == "" {
		return []byte(r.Manifest)
	}
	b, err := hex.DecodeString(r.ManifestHex)
	must(t, err)
	return b
}

type golden struct {
	Note        string           `json:"note"`
	MTime       string           `json:"mtime"`
	Inputs      []goldenInput    `json:"inputs"`
	KeySeed     string           `json:"key_seed"`
	Fingerprint string           `json:"fingerprint"`
	Unsigned    goldenStream     `json:"unsigned"`
	Signed      goldenStream     `json:"signed"`
	Decode      []goldenDecode   `json:"decode"`
	Refused     []goldenManifest `json:"refused"`
	Accepted    []goldenManifest `json:"accepted"`
}

var goldenInputs = []goldenInput{
	{"docs/", 0o755, ""},
	{"docs/a.txt", 0o644, "hello, qrvt\n"},
	{"docs/sub/", 0o700, ""},
	{"docs/ünïcode.txt", 0o600, "nfc\n"},
	{"docs/" + strings.Repeat("n", 120) + ".txt", 0o644, "long name\n"},
	{"empty.bin", 0o600, ""},
	{"notes.txt", 0o640, "line one\nline two\n"},
}

// goldenRule is the manifest of a golden vector: the entry path and, if not empty, second; or, when
// depth is not zero, a chain of folders d, d/d, ... ending in a file f at that many components.
type goldenRule struct {
	rule, path, second string
	depth              int
}

// entries returns the vector's manifest entries.
func (r goldenRule) entries() []Entry {
	file := func(p string) Entry { return Entry{Path: p, Mode: 0o644, MTime: testTime, SHA256: sha256.Sum256(nil)} }
	if r.depth > 0 {
		var es []Entry
		p := ""
		for range r.depth - 1 {
			p += "d"
			es = append(es, Entry{Path: p, Dir: true, Mode: 0o755, MTime: testTime})
			p += "/"
		}
		return append(es, file(p+"f"))
	}
	es := []Entry{file(r.path)}
	if r.second != "" {
		es = append(es, file(r.second))
	}
	return es
}

// acceptedRules are manifests at the edge of a path rule of FORMAT.md section 12 that a decoder must
// accept.
var acceptedRules = []goldenRule{
	{rule: "exactly 64 components", depth: 64},
}

// refusedRules is one manifest per path rule of FORMAT.md section 12 that a decoder must refuse.
var refusedRules = []goldenRule{
	{"absolute", "/etc/passwd", "", 0},
	{"empty component", "a//b", "", 0},
	{"trailing separator", "a/", "", 0},
	{"dot component", "a/./b", "", 0},
	{"dot-dot component", "../b", "", 0},
	{"invalid UTF-8", "a\xffb", "", 0},
	{"not NFC", "é", "", 0},
	{"NUL", "a\x00b", "", 0},
	{"control character", "a\x1b[2Jb", "", 0},
	{"DEL", "a\x7fb", "", 0},
	{"backslash", "a\\b", "", 0},
	{"colon", "c:b", "", 0},
	{"trailing space", "a /b", "", 0},
	{"trailing dot", "a.", "", 0},
	{"device name", "con", "", 0},
	{"device name with extension", "LPT3.txt.gz", "", 0},
	{"component over 255 bytes", strings.Repeat("c", 256), "", 0},
	{"path over 4096 bytes", strings.Repeat(strings.Repeat("p", 250)+"/", 16) + strings.Repeat("p", 81), "", 0},
	{"equal after case folding", "Straße", "STRASSE", 0},
	{"file under a file", "a", "a/b", 0},
	{"equal after case folding (Cherokee)", "Ꭰ", "ꭰ", 0},
	{"less-than", "a<b", "", 0},
	{"greater-than", "a>b", "", 0},
	{"double quote", "a\"b", "", 0},
	{"vertical bar", "a|b", "", 0},
	{"question mark", "a?b", "", 0},
	{"asterisk", "a*b", "", 0},
	{"device name with superscript digit", "COM¹", "", 0},
	{"console device name", "CONIN$.txt", "", 0},
	{"device name with spaces before the dot", "NUL  .txt", "", 0},
	{"file under a file at depth", "a", "a/b/c", 0},
	{"folder not an entry", "a/b", "", 0},
	{rule: "more than 64 components", depth: 65},
}

func goldenTree() (tree, map[string]uint32) {
	t, modes := tree{}, map[string]uint32{}
	for _, in := range goldenInputs {
		t[in.Path] = in.Content
		modes[strings.TrimSuffix(in.Path, "/")] = in.Mode
	}
	return t, modes
}

// goldenSources creates the golden inputs and collects them with the golden modes.
func goldenSources(t *testing.T) []source {
	tr, modes := goldenTree()
	inputs := tr.write(t, t.TempDir())
	srcs, warns, err := collect(inputs)
	if err != nil || len(warns) != 0 {
		t.Fatalf("collect: %v %v", warns, err)
	}
	for i := range srcs {
		srcs[i].Mode = modes[srcs[i].Path] // the file system cannot hold these modes on every platform
	}
	return srcs
}

func goldenPack(t *testing.T, srcs []source, o PackOptions) []byte {
	var s spool
	if _, err := writeStream(context.Background(), &s, srcs, o); err != nil {
		t.Fatal(err)
	}
	return s.b
}

func TestGoldenStream(t *testing.T) {
	srcs := goldenSources(t)
	unsigned := goldenPack(t, srcs, PackOptions{Compression: CompressNone})
	signed := goldenPack(t, srcs, PackOptions{Compression: CompressNone, SignKey: testKey()})
	if *update {
		writeGolden(t, srcs, unsigned, signed)
	}
	b, err := os.ReadFile(goldenFile)
	must(t, err)
	var g golden
	must(t, json.Unmarshal(b, &g))
	if g.MTime != testTime.Format("2006-01-02T15:04:05Z") || g.KeySeed != hex.EncodeToString(testKey().Seed()) ||
		g.Fingerprint != testFP {
		t.Fatal("golden file does not describe the test's inputs")
	}
	for i, in := range g.Inputs {
		if in != goldenInputs[i] {
			t.Fatalf("golden input %d = %+v, test uses %+v", i, in, goldenInputs[i])
		}
	}
	for name, c := range map[string]struct {
		got  []byte
		want goldenStream
	}{"unsigned": {unsigned, g.Unsigned}, "signed": {signed, g.Signed}} {
		if hex.EncodeToString(c.got) != c.want.Stream {
			t.Errorf("%s stream differs from the golden vector:\n got %x\nwant %s", name, c.got, c.want.Stream)
		}
		sum := sha256.Sum256(c.got)
		if hex.EncodeToString(sum[:]) != c.want.SHA256 {
			t.Errorf("%s stream hash differs", name)
		}
	}

	// Every frozen stream must still decode to the golden inputs.
	want, _ := goldenTree()
	decode := append([]goldenDecode{{"unsigned", "", g.Unsigned.Stream}, {"signed", "", g.Signed.Stream}}, g.Decode...)
	for _, d := range decode {
		t.Run("decode "+d.Name, func(t *testing.T) {
			s, err := hex.DecodeString(d.Stream)
			must(t, err)
			out := t.TempDir()
			res, err := unpack(s, UnpackOptions{OutputDir: out, Passphrase: passphrase(d.Passphrase, nil)})
			must(t, err)
			if got := read(t, out); !maps.Equal(got, want) {
				t.Fatalf("decoded %v", got)
			}
			if res.Header.Signed != (d.Name == "signed") || (res.Signer != nil) != res.Header.Signed {
				t.Fatalf("signer %x", res.Signer)
			}
		})
	}

	if len(g.Refused) != len(refusedRules) {
		t.Fatalf("golden file has %d refused manifests, test has %d rules", len(g.Refused), len(refusedRules))
	}
	for _, r := range g.Refused {
		t.Run("refuse "+r.Rule, func(t *testing.T) {
			// The manifest itself must be refused, not merely its missing tar entries. A manifest that
			// is not valid UTF-8 is refused by the parser.
			m := r.bytes(t)
			es, err := ParseManifest(m)
			if r.ManifestHex != "" {
				if !isFormat(err) {
					t.Fatalf("ParseManifest(%q) = %v", m, err)
				}
			} else if err := errors.Join(err, ValidateManifest(es, DefaultMaxSize)); !isUnsafe(err) {
				t.Fatalf("ValidateManifest(%s) = %v", m, err)
			}
			a := archive(t, m)
			out := t.TempDir()
			_, err = unpackArchive(context.Background(), bytes.NewReader(a), int64(len(a)), UnpackOptions{OutputDir: out})
			if !isUnsafe(err) && (r.ManifestHex == "" || !isFormat(err)) {
				t.Fatalf("%q: %v", m, err)
			}
			expectEmpty(t, out)
		})
	}

	if len(g.Accepted) != len(acceptedRules) {
		t.Fatalf("golden file has %d accepted manifests, test has %d rules", len(g.Accepted), len(acceptedRules))
	}
	for _, r := range g.Accepted {
		t.Run("accept "+r.Rule, func(t *testing.T) {
			m := r.bytes(t)
			es, err := ParseManifest(m)
			must(t, err)
			must(t, ValidateManifest(es, DefaultMaxSize))
			var (
				tes  []tarEntry
				want []string
			)
			for _, e := range es {
				te := fileHdr(e.Path, "")
				if e.Dir {
					te = dirHdr(e.Path)
				}
				tes, want = append(tes, te), append(want, e.Path)
			}
			a := archive(t, m, tes...)
			out := t.TempDir()
			res, err := unpackArchive(context.Background(), bytes.NewReader(a), int64(len(a)), UnpackOptions{OutputDir: out})
			must(t, err)
			if !slices.Equal(res.Paths, want) {
				t.Fatalf("extracted %q", res.Paths)
			}
		})
	}
}

func writeGolden(t *testing.T, srcs []source, unsigned, signed []byte) {
	sum := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	g := golden{
		Note: "FORMAT.md section 12 stream vectors, checked by internal/payload TestGoldenStream. Inputs are " +
			"created with the given modes and every mtime set to the value below; the key's seed is the bytes " +
			"0 to 31. unsigned and signed use compression 0 and must be reproduced byte for byte; the decode " +
			"streams (zstd, xz, age) must still decode to the inputs. Each refused manifest must be refused, and each " +
			"accepted manifest accepted.",
		MTime:       testTime.Format("2006-01-02T15:04:05Z"),
		Inputs:      goldenInputs,
		KeySeed:     hex.EncodeToString(testKey().Seed()),
		Fingerprint: testFP,
		Unsigned:    goldenStream{hex.EncodeToString(unsigned), sum(unsigned)},
		Signed:      goldenStream{hex.EncodeToString(signed), sum(signed)},
	}
	// Decode vectors once written are kept: the age vector is random and must not churn.
	if b, err := os.ReadFile(goldenFile); err == nil {
		var old golden
		must(t, json.Unmarshal(b, &old))
		g.Decode = old.Decode
	}
	for _, d := range []struct {
		name string
		o    PackOptions
		pw   string
	}{
		{"zstd", PackOptions{Compression: CompressZstd}, ""},
		{"xz", PackOptions{Compression: CompressXZ}, ""},
		{"zstd, encrypted", PackOptions{Compression: CompressZstd, Passphrase: passphrase("golden", nil)}, "golden"},
	} {
		if len(g.Decode) < 3 {
			g.Decode = append(g.Decode, goldenDecode{d.name, d.pw, hex.EncodeToString(goldenPack(t, srcs, d.o))})
		}
	}
	for _, r := range acceptedRules {
		g.Accepted = append(g.Accepted, goldenManifest{Rule: r.rule, Manifest: string(MarshalManifest(r.entries()))})
	}
	for _, r := range refusedRules {
		es := r.entries()
		m := MarshalManifest(es)
		if !utf8.ValidString(r.path) { // JSON encoding would replace the invalid bytes: splice them in
			es[0].Path = "PLACEHOLDER"
			m = bytes.Replace(MarshalManifest(es), []byte("PLACEHOLDER"), []byte(r.path), 1)
		}
		if utf8.Valid(m) {
			g.Refused = append(g.Refused, goldenManifest{Rule: r.rule, Manifest: string(m)})
		} else {
			g.Refused = append(g.Refused, goldenManifest{Rule: r.rule, ManifestHex: hex.EncodeToString(m)})
		}
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	must(t, enc.Encode(g))
	must(t, os.MkdirAll(filepath.Dir(goldenFile), 0o750))
	must(t, os.WriteFile(goldenFile, b.Bytes(), 0o600))
}

func FuzzUnpackArchive(f *testing.F) {
	es := []tarEntry{dirHdr("d"), fileHdr("d/f", "content")}
	f.Add(archive(f, MarshalManifest(manifestFor(es...)), es...))
	f.Add(archive(f, MarshalManifest(manifestFor(es[0])), es[0]))
	f.Add([]byte{0, 0, 0, 2, '{', '}'})
	f.Fuzz(func(t *testing.T, a []byte) {
		parent := t.TempDir()
		out := filepath.Join(parent, "out")
		must(t, os.Mkdir(out, 0o750))
		_, err := unpackArchive(context.Background(), bytes.NewReader(a), int64(len(a)),
			UnpackOptions{OutputDir: out, MaxSize: 1 << 20})
		es, rerr := os.ReadDir(parent)
		must(t, rerr)
		if len(es) != 1 {
			t.Fatalf("files created outside the output folder: %v", es)
		}
		if err != nil {
			expectEmpty(t, out)
		}
	})
}
