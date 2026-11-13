package payload

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
)

// allocated runs f and returns the bytes allocated and the growth of memory obtained from the system.
func allocated(f func()) (alloc, sys uint64) {
	runtime.GC()
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	f()
	runtime.ReadMemStats(&b)
	return b.TotalAlloc - a.TotalAlloc, b.Sys - a.Sys
}

// xzBody returns an xz body for archive bytes, with its first block header edited by edit and its
// CRC-32 fixed up.
func xzBody(t *testing.T, a []byte, edit func(hdr []byte)) []byte {
	t.Helper()
	body := compressForTest(t, CompressXZ, a)
	const bh = 12 // the stream header
	hdr := body[bh : bh+(int(body[bh])+1)*4]
	edit(hdr)
	binary.LittleEndian.PutUint32(hdr[len(hdr)-4:], crc32.ChecksumIEEE(hdr[:len(hdr)-4]))
	return body
}

// dictByte returns the index of the LZMA2 dictionary byte in a block header written by the library.
func dictByte(t *testing.T, hdr []byte) int {
	t.Helper()
	i := bytes.Index(hdr, []byte{0x21, 0x01})
	if i < 0 {
		t.Fatal("no LZMA2 filter in the block header")
	}
	return i + 2
}

func TestXZDictionaryLimit(t *testing.T) {
	es := []tarEntry{fileHdr("a", "content")}
	a := archive(t, MarshalManifest(manifestFor(es...)), es...)
	h := Header{Method: MethodXZ, ArchiveLen: int64(len(a))}
	for _, code := range []byte{40, 23, 24} { // 4 GiB - 1, 12 MiB, 16 MiB
		body := xzBody(t, a, func(hdr []byte) { hdr[dictByte(t, hdr)] = code })
		var err error
		alloc, _ := allocated(func() { _, err = unpack(rawWithHeader(h, body), UnpackOptions{OutputDir: t.TempDir()}) })
		var l *LimitError
		if !errors.As(err, &l) || l.Limit != "xz dictionary" || l.Max != XZDictCap {
			t.Fatalf("dictionary code %d: %v", code, err)
		}
		if alloc > XZDictCap {
			t.Fatalf("dictionary code %d: allocated %d MiB", code, alloc>>20)
		}
	}
	// 8 MiB exactly (code 22) is what the encoder writes and decodes.
	body := xzBody(t, a, func(hdr []byte) {
		if hdr[dictByte(t, hdr)] != 22 {
			t.Fatalf("encoder dictionary code %d, want 22", hdr[dictByte(t, hdr)])
		}
	})
	if _, err := unpack(rawWithHeader(h, body), UnpackOptions{OutputDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
}

func TestXZStructureRejects(t *testing.T) {
	es := []tarEntry{fileHdr("a", "content")}
	a := archive(t, MarshalManifest(manifestFor(es...)), es...)
	h := Header{Method: MethodXZ, ArchiveLen: int64(len(a))}
	good := compressForTest(t, CompressXZ, a)
	cases := map[string][]byte{
		"dictionary code 41": xzBody(t, a, func(hdr []byte) { hdr[dictByte(t, hdr)] = 41 }),
		"two filters":        xzBody(t, a, func(hdr []byte) { hdr[1] |= 1 }),
		"other filter":       xzBody(t, a, func(hdr []byte) { hdr[dictByte(t, hdr)-2] = 0x03 }),
		"filter props size":  xzBody(t, a, func(hdr []byte) { hdr[dictByte(t, hdr)-1] = 2 }),
		"bad magic":          append([]byte("not xz at all"), good[13:]...),
		"check type":         append(append(bytes.Clone(good[:7]), 0x02), good[8:]...),
		"truncated header":   good[:5],
		"truncated block":    good[:15],
		"bad chunk":          append(bytes.Clone(good[:12+(int(good[12])+1)*4]), 0x05, 0, 0),
		"chunk cut short":    append(bytes.Clone(good[:12+(int(good[12])+1)*4]), 0x01, 0, 9, 'x'),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			out := t.TempDir()
			if _, err := unpack(rawWithHeader(h, body), UnpackOptions{OutputDir: out}); !isFormat(err) {
				t.Fatalf("Unpack: %v", err)
			}
			expectEmpty(t, out)
		})
	}
	// Optional size fields in the block header are skipped correctly.
	withSizes := xzBody(t, a, func(hdr []byte) {
		i := dictByte(t, hdr) - 2
		// Rebuild: flags with both sizes present, sizes 1 byte each would be wrong values, so the
		// library must refuse, but only after the guard has parsed the filter.
		copy(hdr[2:], append([]byte{0x7f, 0x7f}, hdr[i:i+3]...))
		hdr[1] |= 0xc0
	})
	if _, err := unpack(rawWithHeader(h, withSizes), UnpackOptions{OutputDir: t.TempDir()}); err == nil || isLimit("xz dictionary")(err) {
		t.Fatalf("block with size fields: %v", err)
	}
}

func TestXZIncompressibleRoundTrip(t *testing.T) {
	src := t.TempDir()
	random := make([]byte, 300<<10)
	var x uint64 = 7
	for i := range random {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		random[i] = byte(x)
	}
	must(t, os.WriteFile(filepath.Join(src, "r"), random, 0o600))
	stream, _ := pack(t, PackOptions{Inputs: []string{filepath.Join(src, "r")}, Compression: CompressXZ})
	out := t.TempDir()
	if _, err := unpack(stream, UnpackOptions{OutputDir: out}); err != nil {
		t.Fatal(err)
	}
}

// zstdFrame returns a zstd frame with the given header bytes (after the magic) and one raw last block.
func zstdFrame(hdr []byte, data []byte) []byte {
	f := append([]byte{0x28, 0xB5, 0x2F, 0xFD}, hdr...)
	bh := uint32(1) | uint32(len(data))<<3 // last block, raw
	f = append(f, byte(bh), byte(bh>>8), byte(bh>>16))
	return append(f, data...)
}

func TestZstdWindowLimit(t *testing.T) {
	es := []tarEntry{fileHdr("a", "content")}
	a := archive(t, MarshalManifest(manifestFor(es...)), es...)
	h := Header{Method: MethodZstd, ArchiveLen: int64(len(a))}
	fcs := func(n uint64) []byte {
		b := binary.LittleEndian.AppendUint64(nil, n)
		return append([]byte{0xE0}, b...) // single segment, 8-byte content size
	}
	cases := map[string][]byte{
		"window 16 MiB":         zstdFrame([]byte{0x00, 14 << 3}, a), // window log 24
		"window 8 MiB + 1/8":    zstdFrame([]byte{0x00, 13<<3 | 1}, a),
		"single segment 9 MiB":  zstdFrame(fcs(9<<20), a),
		"single segment 12 GiB": zstdFrame(fcs(12<<30), a),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			var err error
			alloc, _ := allocated(func() { _, err = unpack(rawWithHeader(h, body), UnpackOptions{OutputDir: t.TempDir()}) })
			if !isFormat(err) || alloc > MaxZstdWindow {
				t.Fatalf("Unpack: %v, allocated %d MiB", err, alloc>>20)
			}
		})
	}
	// A window of exactly 8 MiB decodes.
	if _, err := unpack(rawWithHeader(h, zstdFrame([]byte{0x00, 13 << 3}, a)), UnpackOptions{OutputDir: t.TempDir()}); err != nil {
		t.Fatalf("window 8 MiB: %v", err)
	}
}

func TestFoldIdempotentAndOrbitInvariant(t *testing.T) {
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if !utf8.ValidRune(r) {
			continue
		}
		k := foldKey(string(r))
		if foldKey(k) != k {
			t.Fatalf("U+%04X: fold %q is not idempotent (%q)", r, k, foldKey(k))
		}
		if o := unicode.SimpleFold(r); foldKey(string(o)) != k {
			t.Fatalf("U+%04X and U+%04X fold differently: %q, %q", r, o, k, foldKey(string(o)))
		}
	}
	for _, pair := range [][2]string{{"Ꭰ", "ꭰ"}, {"Ᏸ", "ᏸ"}, {"Straße", "STRASSE"}, {"K", "k"}} {
		if foldKey(pair[0]) != foldKey(pair[1]) {
			t.Errorf("%q and %q do not collide", pair[0], pair[1])
		}
	}
}

func TestValidatePathWindows(t *testing.T) {
	for _, p := range []string{
		"COM¹", "com².txt", "LPT³", "CONIN$", "conout$.log", "CON .txt", "nul  ", "x/AUX  .c",
		"a<b", "a>b", "a\"b", "a|b", "a?b", "a*b",
	} {
		if !isUnsafe(ValidatePath(p)) {
			t.Errorf("ValidatePath(%q) accepted", p)
		}
	}
	for _, p := range []string{"COM⁴", "CONIN", "conout", "con-x", "nul_"} {
		if err := ValidatePath(p); err != nil {
			t.Errorf("ValidatePath(%q) = %v", p, err)
		}
	}
}

func TestKeyLoneCR(t *testing.T) {
	if _, err := ParsePrivateKey([]byte(testPriv + "\r")); !errors.Is(err, ErrKeyFormat) {
		t.Fatalf("lone CR: %v", err)
	}
	if _, err := ParsePublicKey([]byte(testPub + "\r")); !errors.Is(err, ErrKeyFormat) {
		t.Fatalf("lone CR: %v", err)
	}
}

// deepManifest returns the folders a, a/a, ... to the given depth and then n files with distinct
// names in the deepest one, as tar entries in manifest order.
func deepManifest(n, depth int) []tarEntry {
	var es []tarEntry
	p := "a"
	for range depth {
		es = append(es, dirHdr(p))
		p += "/a"
	}
	p = strings.TrimSuffix(p, "a")
	for i := range n {
		es = append(es, fileHdr(p+"f"+string(rune('A'+i%26))+string(rune('A'+i/26%26))+string(rune('A'+i/676%26)), ""))
	}
	return es
}

// TestDeepManifestResources decodes manifests near the limits of depth and size within the bounds
// of SECURITY.md section 9.5: 60 s and 1 GiB.
func TestDeepManifestResources(t *testing.T) {
	if testing.Short() {
		t.Skip("manifests near the limits; skipped with -short")
	}
	if raceEnabled {
		t.Skip("the time bound does not apply under the race detector; it runs in the ordinary test task")
	}
	start := time.Now()
	m := MarshalManifest(manifestFor(deepManifest(14500, 2000)...)) // about 64 MB of manifest
	if len(m) < 59<<20 || len(m) > MaxManifestBytes {
		t.Fatalf("manifest is %d bytes", len(m))
	}
	var b bytes.Buffer
	must(t, binary.Write(&b, binary.BigEndian, uint32(len(m))))
	b.Write(m)
	b.Write(make([]byte, 1024)) // an empty tar: every entry is missing
	out := t.TempDir()
	var err error
	_, sys := allocated(func() { _, err = unpack(rawStream(b.Bytes()), UnpackOptions{OutputDir: out}) })
	if !isUnsafe(err) {
		t.Fatalf("Unpack: %v", err)
	}
	expectEmpty(t, out)
	if d := time.Since(start); d > 60*time.Second || sys > 1<<30 {
		t.Fatalf("deep manifest took %v and %d MiB", d, sys>>20)
	}

	// A real extraction of a 2000-deep chain of folders, twice into the same folder (the second time
	// every folder exists and every file is replaced). Folders cost the same at any depth; each file
	// costs one resolution of its path, so a few suffice here.
	start = time.Now()
	tes := deepManifest(20, 2000)
	a := archive(t, MarshalManifest(manifestFor(tes...)), tes...)
	out = t.TempDir()
	for _, overwrite := range []bool{false, true} {
		_, sys = allocated(func() {
			_, err = unpackArchive(context.Background(), bytes.NewReader(a), int64(len(a)), UnpackOptions{OutputDir: out, Overwrite: overwrite})
		})
		if err != nil || sys > 1<<30 {
			t.Fatalf("deep extraction (overwrite %v): %v, %d MiB", overwrite, err, sys>>20)
		}
	}
	if d := time.Since(start); d > 60*time.Second {
		t.Fatalf("deep extraction took %v", d)
	}
}

// commitTree is a stream whose files are placed in tree order: d/new, d/old, e/x.
func commitTree(t *testing.T) []byte {
	stream, _ := pack(t, PackOptions{Inputs: tree{"d/": "", "d/old": "new content", "d/new": "n", "e/": "", "e/x": "x"}.write(t, t.TempDir())})
	return stream
}

func withHook(t *testing.T, hook func(root *os.Root, path string) error) {
	t.Cleanup(func() { testHookPlace = nil })
	testHookPlace = hook
}

func TestCommitRace(t *testing.T) {
	stream := commitTree(t)
	out := t.TempDir()
	withHook(t, func(root *os.Root, p string) error {
		if p == "d/new" {
			return root.WriteFile(p, []byte("planted"), 0o600)
		}
		return nil
	})
	var ee *ExistsError
	if _, err := unpack(stream, UnpackOptions{OutputDir: out}); !errors.As(err, &ee) || ee.Path != "d/new" {
		t.Fatalf("file planted before commit: %v", err)
	}
	// Rolled back: only the planted file and its folder remain.
	if got := read(t, out); !maps.Equal(got, tree{"d/": "", "d/new": "planted"}) {
		t.Fatalf("after the failed commit: %v", got)
	}

	// With Overwrite, a planted folder or link is refused, and a planted regular file is replaced.
	out = t.TempDir()
	withHook(t, func(root *os.Root, p string) error {
		if p == "e/x" {
			return root.Mkdir(p, 0o700)
		}
		return nil
	})
	if _, err := unpack(stream, UnpackOptions{OutputDir: out, Overwrite: true}); !errors.As(err, &ee) || ee.Path != "e/x" {
		t.Fatalf("folder planted before commit: %v", err)
	}
	if got := read(t, out); !maps.Equal(got, tree{"e/": "", "e/x/": ""}) {
		t.Fatalf("after the failed commit: %v", got)
	}
	out = t.TempDir()
	withHook(t, func(root *os.Root, p string) error {
		if p == "d/new" {
			return root.WriteFile(p, []byte("planted"), 0o600)
		}
		return nil
	})
	if _, err := unpack(stream, UnpackOptions{OutputDir: out, Overwrite: true}); err != nil {
		t.Fatalf("regular file planted, overwrite: %v", err)
	}
	if got := read(t, out); got["d/new"] != "n" {
		t.Fatalf("planted file not replaced: %v", got)
	}
}

func TestCommitAliasOfCommittedFile(t *testing.T) {
	stream := commitTree(t)
	out := t.TempDir()
	withHook(t, func(root *os.Root, p string) error {
		if p == "e/x" { // make e/x the same file as d/old, which this transfer placed
			if err := root.Mkdir("e", 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			return root.Link("d/old", "e/x")
		}
		return nil
	})
	var ee *ExistsError
	if _, err := unpack(stream, UnpackOptions{OutputDir: out, Overwrite: true}); !errors.As(err, &ee) || ee.Path != "e/x" {
		t.Fatalf("alias of a committed file: %v", err)
	}
}

func TestCommitRollback(t *testing.T) {
	stream := commitTree(t)
	out := t.TempDir()
	before := tree{"d/": "", "d/old": "old content", "keep": "keep"}
	before.write(t, out)
	boom := errors.New("disk on fire")
	withHook(t, func(root *os.Root, p string) error {
		if p == "e/x" {
			return boom
		}
		return nil
	})
	if _, err := unpack(stream, UnpackOptions{OutputDir: out, Overwrite: true}); !errors.Is(err, boom) {
		t.Fatalf("failing commit: %v", err)
	}
	if got := read(t, out); !maps.Equal(got, before) {
		t.Fatalf("not rolled back: %v", got)
	}
}

func TestCommitWithoutHardLinks(t *testing.T) {
	t.Cleanup(func() { linkFile = defaultLinkFile })
	linkFile = func(*os.Root, string, string) error { return &os.LinkError{Op: "link", Err: errors.ErrUnsupported} }
	stream := commitTree(t)
	out := t.TempDir()
	if _, err := unpack(stream, UnpackOptions{OutputDir: out}); err != nil {
		t.Fatalf("without hard links: %v", err)
	}
	if got := read(t, out); got["d/old"] != "new content" || got["e/x"] != "x" {
		t.Fatalf("unpacked %v", got)
	}
	out = t.TempDir()
	withHook(t, func(root *os.Root, p string) error {
		if p == "d/new" {
			return root.WriteFile(p, []byte("planted"), 0o600)
		}
		return nil
	})
	var ee *ExistsError
	if _, err := unpack(stream, UnpackOptions{OutputDir: out}); !errors.As(err, &ee) {
		t.Fatalf("planted file without hard links: %v", err)
	}
}
