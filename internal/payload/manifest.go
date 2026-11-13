package payload

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Entry is one manifest entry (FORMAT.md section 12).
type Entry struct {
	Path   string
	Dir    bool
	Size   int64 // files only
	Mode   uint32
	MTime  time.Time
	SHA256 [32]byte // files only
}

// perm returns the meaningful mode bits: the low nine.
func (e Entry) perm() uint32 { return e.Mode & 0o777 }

// MarshalManifest encodes entries as the manifest an encoder writes: keys in the order of FORMAT.md
// section 12, no whitespace, mode limited to the low nine bits, mtime in whole seconds UTC.
func MarshalManifest(entries []Entry) []byte {
	var b bytes.Buffer
	b.WriteString(`{"version":1,"entries":[`)
	for i, e := range entries {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"path":`)
		writeString(&b, e.Path)
		if e.Dir {
			b.WriteString(`,"type":"dir"`)
		} else {
			b.WriteString(`,"type":"file","size":`)
			b.WriteString(strconv.FormatInt(e.Size, 10))
		}
		b.WriteString(`,"mode":`)
		b.WriteString(strconv.FormatUint(uint64(e.perm()), 10))
		b.WriteString(`,"mtime":"`)
		b.WriteString(e.MTime.UTC().Truncate(time.Second).Format(time.RFC3339))
		b.WriteByte('"')
		if !e.Dir {
			b.WriteString(`,"sha256":"`)
			b.WriteString(hex.EncodeToString(e.SHA256[:]))
			b.WriteByte('"')
		}
		b.WriteByte('}')
	}
	b.WriteString(`]}`)
	return b.Bytes()
}

// writeString writes s as a JSON string without HTML escaping.
func writeString(b *bytes.Buffer, s string) {
	var t bytes.Buffer
	enc := json.NewEncoder(&t)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a string always encodes
	b.Write(bytes.TrimSuffix(t.Bytes(), []byte("\n")))
}

// ParseManifest decodes a manifest strictly: valid UTF-8 JSON with exactly the keys of FORMAT.md
// section 12, each once, integers without fraction or exponent, and version 1. It stops with a
// [LimitError] beyond [MaxManifestBytes] or [MaxManifestEntries]. It does not apply the path rules;
// see [ValidateManifest].
func ParseManifest(b []byte) ([]Entry, error) {
	if len(b) > MaxManifestBytes {
		return nil, &LimitError{"manifest size", int64(len(b)), MaxManifestBytes}
	}
	if !utf8.Valid(b) {
		return nil, manifestErr("not valid UTF-8")
	}
	p := parser{json.NewDecoder(bytes.NewReader(b))}
	p.d.UseNumber()
	if err := p.delim('{'); err != nil {
		return nil, err
	}
	var (
		version    int64
		entries    []Entry
		seen       = map[string]bool{}
		hasEntries bool
	)
	for p.d.More() {
		key, err := p.key(seen)
		if err != nil {
			return nil, err
		}
		switch key {
		case "version":
			version, err = p.int(0, math.MaxInt64)
		case "entries":
			entries, err = p.entries()
			hasEntries = true
		default:
			err = manifestErr("unknown key %q", key)
		}
		if err != nil {
			return nil, err
		}
	}
	if err := p.delim('}'); err != nil {
		return nil, err
	}
	if _, err := p.d.Token(); !errors.Is(err, io.EOF) {
		return nil, manifestErr("data after the manifest")
	}
	if !seen["version"] || !hasEntries {
		return nil, manifestErr("version or entries missing")
	}
	if version != 1 {
		return nil, &UnsupportedVersionError{"manifest version", version}
	}
	return entries, nil
}

func manifestErr(format string, a ...any) error {
	return fmt.Errorf("%w: manifest: %s", ErrFormat, fmt.Sprintf(format, a...))
}

// parser reads the manifest token by token, so that key spelling and repetition can be checked.
type parser struct{ d *json.Decoder }

func (p parser) token() (json.Token, error) {
	t, err := p.d.Token()
	if err != nil {
		return nil, manifestErr("%v", err)
	}
	return t, nil
}

func (p parser) delim(want json.Delim) error {
	t, err := p.token()
	if err != nil {
		return err
	}
	if t != want {
		return manifestErr("want %v, found %v", want, t)
	}
	return nil
}

// key reads an object key and refuses one already in seen.
func (p parser) key(seen map[string]bool) (string, error) {
	t, err := p.token() // the decoder only returns strings in key position
	if err != nil {
		return "", err
	}
	k := t.(string)
	if seen[k] {
		return "", manifestErr("key %q repeated", k)
	}
	seen[k] = true
	return k, nil
}

func (p parser) string() (string, error) {
	t, err := p.token()
	if err != nil {
		return "", err
	}
	s, ok := t.(string)
	if !ok {
		return "", manifestErr("want a string, found %v", t)
	}
	return s, nil
}

// int reads an integer written without fraction or exponent, in [lo, hi].
func (p parser) int(lo, hi int64) (int64, error) {
	t, err := p.token()
	if err != nil {
		return 0, err
	}
	n, ok := t.(json.Number)
	if !ok {
		return 0, manifestErr("want a number, found %v", t)
	}
	v, err := strconv.ParseInt(string(n), 10, 64)
	if err != nil || v < lo || v > hi {
		return 0, manifestErr("bad integer %s", n)
	}
	return v, nil
}

func (p parser) entries() ([]Entry, error) {
	if err := p.delim('['); err != nil {
		return nil, err
	}
	var es []Entry
	for p.d.More() {
		if len(es) == MaxManifestEntries {
			return nil, &LimitError{"manifest entries", MaxManifestEntries + 1, MaxManifestEntries}
		}
		e, err := p.entry()
		if err != nil {
			return nil, err
		}
		es = append(es, e)
	}
	return es, p.delim(']')
}

func (p parser) entry() (Entry, error) {
	var e Entry
	if err := p.delim('{'); err != nil {
		return e, err
	}
	seen := map[string]bool{}
	var typ string
	for p.d.More() {
		key, err := p.key(seen)
		if err != nil {
			return e, err
		}
		var v int64
		var s string
		switch key {
		case "path":
			e.Path, err = p.string()
		case "type":
			typ, err = p.string()
		case "size":
			e.Size, err = p.int(0, math.MaxInt64)
		case "mode":
			v, err = p.int(0, math.MaxUint32)
			e.Mode = uint32(v) //nolint:gosec // range checked
		case "mtime":
			if s, err = p.string(); err == nil {
				e.MTime, err = time.Parse(time.RFC3339, s)
				if err != nil {
					err = manifestErr("bad mtime %q", s)
				}
			}
		case "sha256":
			if s, err = p.string(); err == nil {
				err = parseHash(&e.SHA256, s)
			}
		default:
			err = manifestErr("unknown entry key %q", key)
		}
		if err != nil {
			return e, err
		}
	}
	if err := p.delim('}'); err != nil {
		return e, err
	}
	switch {
	case !seen["path"] || !seen["mode"] || !seen["mtime"]:
		return e, manifestErr("entry %q lacks a path, mode or mtime", e.Path)
	case typ == "dir" && !seen["size"] && !seen["sha256"]:
		e.Dir = true
	case typ == "file" && seen["size"] && seen["sha256"]:
	default:
		return e, manifestErr("entry %q: type %q with the wrong fields", e.Path, typ)
	}
	return e, nil
}

// parseHash decodes 64 lowercase hexadecimal digits.
func parseHash(dst *[32]byte, s string) error {
	if len(s) != 64 || strings.ToLower(s) != s {
		return manifestErr("bad sha256 %q", s)
	}
	if _, err := hex.Decode(dst[:], []byte(s)); err != nil {
		return manifestErr("bad sha256 %q", s)
	}
	return nil
}

// ValidateManifest applies every path rule of FORMAT.md section 12 to the whole manifest and checks
// that the files' total size is at most maxSize. It returns an [UnsafeArchiveError] or a [LimitError].
//
// Every folder that contains an entry must itself be an entry of type dir. This also refuses an entry
// beneath a file at any depth, and it bounds the folders an extraction creates by the entry limit and
// the length of their paths by the manifest size. The cost is linear in the manifest's size.
func ValidateManifest(entries []Entry, maxSize int64) error {
	probs, err := manifestProblems(entries, maxSize, false)
	if err == nil && len(probs) > 0 {
		err = probs[0]
	}
	return err
}

// manifestProblems applies the rules of ValidateManifest. It stops at the first problem unless all
// is set, in which case it returns every entry that breaks a rule, each with the first rule it
// breaks. A total size beyond maxSize is returned as the error.
func manifestProblems(entries []Entry, maxSize int64, all bool) ([]*UnsafeArchiveError, error) {
	keys := make(map[string]int, len(entries)) // folded path to entry index
	folded := make([]string, len(entries))
	bad := make(map[int]bool)
	var (
		probs []*UnsafeArchiveError
		total int64
	)
	// refuse records a problem with entry i and reports whether to stop.
	refuse := func(i int, reason string) bool {
		probs, bad[i] = append(probs, &UnsafeArchiveError{entries[i].Path, reason}), true
		return !all
	}
	for i, e := range entries {
		if reason := pathRule(e.Path); reason != "" && refuse(i, reason) {
			return probs, nil
		}
		if e.Size < 0 {
			if refuse(i, "negative size") {
				return probs, nil
			}
			continue
		}
		if e.Size > maxSize-total {
			return probs, &LimitError{"unpacked size", satAdd(total, e.Size), maxSize}
		}
		total += e.Size
		k := foldKey(e.Path)
		if j, ok := keys[k]; ok {
			if !bad[i] && refuse(i, fmt.Sprintf("equal to %q after case folding", entries[j].Path)) {
				return probs, nil
			}
			continue
		}
		keys[k], folded[i] = i, k
	}
	for i, k := range folded {
		// '/' folds to itself and is a starter, so the key of the parent is a prefix of the key.
		if s := strings.LastIndexByte(k, '/'); s >= 0 && !bad[i] {
			if j, ok := keys[k[:s]]; (!ok || !entries[j].Dir) && refuse(i, "its folder is not an entry of type dir") {
				return probs, nil
			}
		}
	}
	return probs, nil
}

func satAdd(a, b int64) int64 {
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}
