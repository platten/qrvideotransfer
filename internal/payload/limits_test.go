package payload

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// flatManifest returns n empty files named f0, f1, ... with names padded to width bytes.
func flatManifest(n, width int) []Entry {
	es := make([]Entry, n)
	for i := range es {
		name := fmt.Sprintf("f%d", i)
		name += strings.Repeat("x", max(0, width-len(name)))
		es[i] = Entry{Path: name, Mode: 0o644, MTime: testTime}
	}
	return es
}

// manifestOnly returns archive bytes holding the manifest of es and an empty tar: a decoder that
// accepts the manifest stops at the first missing tar entry, before extracting anything.
func manifestOnly(es []Entry) []byte {
	m := MarshalManifest(es)
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, uint32(len(m)))
	b.Write(m)
	b.Write(make([]byte, 1024))
	return b.Bytes()
}

func limitErr(err error) *LimitError {
	var l *LimitError
	if errors.As(err, &l) {
		return l
	}
	return nil
}

func TestManifestLimits(t *testing.T) {
	if got := manifestLimits(UnpackOptions{}); got != (limits{DefaultMaxEntries, DefaultMaxManifestBytes}) {
		t.Errorf("defaults %+v", got)
	}
	if DefaultMaxEntries != 100_000 || DefaultMaxManifestBytes != 6_710_886 {
		t.Errorf("defaults %d and %d differ from the root package's", DefaultMaxEntries, DefaultMaxManifestBytes)
	}
	// N entries and N x 64 MiB / 1,000,000 bytes, rounded down, at most the format limits.
	for _, c := range []struct {
		in   int64
		want limits
	}{
		{DefaultMaxEntries, limits{DefaultMaxEntries, DefaultMaxManifestBytes}},
		{200_000, limits{200_000, 13_421_772}}, // 13,421,772.8
		{123_457, limits{123_457, 8_285_059}},  // 8,285,059.02
		{10, limits{10, 671}},                  // below the defaults too
		{1, limits{1, 67}},
		{MaxManifestEntries, limits{MaxManifestEntries, MaxManifestBytes}},
		{5_000_000, limits{MaxManifestEntries, MaxManifestBytes}},
	} {
		if got := manifestLimits(UnpackOptions{MaxEntries: c.in}); got != c.want {
			t.Errorf("MaxEntries %d: %+v, want %+v", c.in, got, c.want)
		}
	}
}

func run(a []byte, o UnpackOptions) error {
	_, err := unpackArchive(context.Background(), bytes.NewReader(a), int64(len(a)), o)
	return err
}

// TestDefaultEntryLimit: one entry over the default is refused before extraction with the flag that
// raises it; raised, the manifest is accepted (and the empty tar then refused). The limits allow
// about 67 bytes per entry, less than any file entry takes, so such a manifest reaches the size
// limit first; the entry limit itself is checked on the parser with a large size limit.
func TestDefaultEntryLimit(t *testing.T) {
	a := manifestOnly(flatManifest(DefaultMaxEntries+1, 0))
	out := t.TempDir()
	l := limitErr(run(a, UnpackOptions{OutputDir: out}))
	if l == nil || l.Flag != "--max-entries" || (l.Limit != "manifest entries" && l.Limit != "manifest size") {
		t.Fatalf("over the default: %+v", l)
	}
	expectEmpty(t, out)
	if err := run(a, UnpackOptions{OutputDir: out, MaxEntries: 3 * DefaultMaxEntries}); !isUnsafe(err) {
		t.Fatalf("raised: %v", err)
	}
	m := MarshalManifest(flatManifest(11, 0))
	_, err := parseManifest(m, limits{10, MaxManifestBytes})
	if l := limitErr(err); l == nil || l.Limit != "manifest entries" || l.Max != 10 || l.Flag != "--max-entries" {
		t.Fatalf("11 entries over a limit of 10: %v", err)
	}
	if _, err := parseManifest(m, limits{11, MaxManifestBytes}); err != nil {
		t.Fatalf("11 entries at a limit of 11: %v", err)
	}
}

// TestDefaultManifestSizeLimit: a manifest of few entries but more bytes than the default.
func TestDefaultManifestSizeLimit(t *testing.T) {
	es := flatManifest(DefaultMaxManifestBytes/250, 240)
	a := manifestOnly(es)
	l := limitErr(run(a, UnpackOptions{OutputDir: t.TempDir()}))
	if l == nil || l.Limit != "manifest size" || l.Max != DefaultMaxManifestBytes || l.Flag != "--max-entries" {
		t.Fatalf("over the default: %+v", l)
	}
	if err := run(a, UnpackOptions{OutputDir: t.TempDir(), MaxEntries: 2 * DefaultMaxEntries}); !isUnsafe(err) {
		t.Fatalf("raised: %v", err)
	}
}

// TestLimitFlags: the format limits cannot be raised, so their errors name no flag; the unpacked
// size names --max-size.
func TestLimitFlags(t *testing.T) {
	big := []Entry{entry("a", false), entry("b", false)}
	big[0].Size, big[1].Size = 60, 41
	if l := limitErr(ValidateManifest(big, 100)); l == nil || l.Flag != "--max-size" {
		t.Errorf("unpacked size: %+v", l)
	}
	if l := limitErr(run(manifestOnly(flatManifest(1, 0))[:4], UnpackOptions{})); l != nil {
		t.Errorf("short archive: %+v", l)
	}
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, uint32(MaxManifestBytes+1))
	b.Write(make([]byte, 16))
	if l := limitErr(run(b.Bytes(), UnpackOptions{MaxEntries: MaxManifestEntries})); l == nil || l.Flag != "" || l.Max != MaxManifestBytes {
		t.Errorf("manifest over the format limit: %+v", l)
	}
	// A declared size above the format limit cannot be admitted by any --max-entries, so even at
	// lower limits the error names no flag; one below it names the flag.
	for _, n := range []int64{0, 200_000} {
		lim := manifestLimits(UnpackOptions{MaxEntries: n})
		if l := limitErr(run(b.Bytes(), UnpackOptions{MaxEntries: n})); l == nil || l.Flag != "" || l.Max != lim.bytes {
			t.Errorf("MaxEntries %d, declared size over the format limit: %+v", n, l)
		}
		var c bytes.Buffer
		_ = binary.Write(&c, binary.BigEndian, uint32(MaxManifestBytes))
		c.Write(make([]byte, 16))
		if l := limitErr(run(c.Bytes(), UnpackOptions{MaxEntries: n})); l == nil || l.Flag != "--max-entries" {
			t.Errorf("MaxEntries %d, declared size at the format limit: %+v", n, l)
		}
	}
	if !strings.Contains((&LimitError{"manifest entries", 2, 1, "--max-entries"}).Error(), "--max-entries") {
		t.Error("the message does not name the flag")
	}
}

// TestReceiverWarning: a transfer over the decoder's default limits warns that the receiver needs
// --max-entries, with a value that is enough.
func TestReceiverWarning(t *testing.T) {
	if w := receiverWarning(DefaultMaxEntries, DefaultMaxManifestBytes); w != nil {
		t.Fatalf("at the defaults: %+v", w)
	}
	for _, c := range []struct {
		entries, size int
		need          int64
	}{
		{DefaultMaxEntries + 1, 1000, DefaultMaxEntries + 1},
		{10, DefaultMaxManifestBytes + 1, DefaultMaxEntries + 1},
		{10, MaxManifestBytes / 2, MaxManifestEntries / 2},
	} {
		w := receiverWarning(c.entries, c.size)
		if w == nil || w.Code != WarnReceiverLimit || !strings.Contains(w.Text, fmt.Sprintf("--max-entries %d", c.need)) {
			t.Errorf("%d entries, %d bytes: %+v", c.entries, c.size, w)
			continue
		}
		if got := manifestLimits(UnpackOptions{MaxEntries: c.need}); int64(c.entries) > got.entries || int64(c.size) > got.bytes {
			t.Errorf("%d entries, %d bytes: --max-entries %d is not enough (%+v)", c.entries, c.size, c.need, got)
		}
	}
}

// exactManifest returns manifest entries whose manifest is exactly size bytes.
func exactManifest(t *testing.T, size int) []Entry {
	t.Helper()
	empty := len(MarshalManifest(nil))
	one := func(name string) int {
		return len(MarshalManifest([]Entry{{Path: name, Mode: 0o644, MTime: testTime}})) - empty
	}
	per := one(fmt.Sprintf("f%07d", 0)+strings.Repeat("x", 92)) + 1 // with its comma
	es := flatManifest((size-empty-200)/per, 100)
	r := size - len(MarshalManifest(es)) - 1 // the last entry's bytes, without its comma
	if r-one("g") > 254 {
		es = append(es, flatManifest(len(es)+1, 100)[len(es)])
		r -= per
	}
	es = append(es, Entry{Path: "g" + strings.Repeat("y", r-one("g")), Mode: 0o644, MTime: testTime})
	if n := len(MarshalManifest(es)); n != size {
		t.Fatalf("manifest of %d bytes, want %d", n, size)
	}
	return es
}

// TestManifestSizeBoundary: a manifest of exactly the limit is accepted and one byte more is
// refused, at the default and at a raised limit (N x 64 MiB / 1,000,000, rounded down).
func TestManifestSizeBoundary(t *testing.T) {
	for _, n := range []int64{0, 200_000} {
		lim := manifestLimits(UnpackOptions{MaxEntries: n})
		at := exactManifest(t, int(lim.bytes))
		if err := run(manifestOnly(at), UnpackOptions{OutputDir: t.TempDir(), MaxEntries: n}); !isUnsafe(err) {
			t.Errorf("MaxEntries %d, manifest of exactly %d bytes: %v", n, lim.bytes, err)
		}
		over := exactManifest(t, int(lim.bytes)+1)
		l := limitErr(run(manifestOnly(over), UnpackOptions{OutputDir: t.TempDir(), MaxEntries: n}))
		if l == nil || l.Limit != "manifest size" || l.Value != lim.bytes+1 || l.Max != lim.bytes {
			t.Errorf("MaxEntries %d, one byte over: %+v", n, l)
		}
	}
}
