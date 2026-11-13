package payload

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

var sample = tree{
	"docs/":                "",
	"docs/a.txt":           "hello, qrvt\n",
	"docs/empty/":          "",
	"docs/deep/":           "",
	"docs/deep/er/":        "",
	"docs/deep/er/x.bin":   strings.Repeat("\x00\x01\x02", 5000),
	"docs/deep/er/void/":   "",
	"docs/ünïcode.txt":     "nfc\n",
	"empty.bin":            "",
	"notes.txt":            strings.Repeat("line\n", 100),
	"docs/" + long + ".md": "long name\n",
}

var long = strings.Repeat("n", 150)

func passphrase(p string, calls *int) func() (string, error) {
	return func() (string, error) {
		if calls != nil {
			*calls++
		}
		return p, nil
	}
}

func TestRoundTrip(t *testing.T) {
	src := t.TempDir()
	inputs := sample.write(t, src)
	for _, c := range []struct {
		name string
		comp Compression
		want Method
		enc  bool
		sign bool
	}{
		{"auto", CompressAuto, MethodZstd, false, false},
		{"zstd", CompressZstd, MethodZstd, false, false},
		{"xz", CompressXZ, MethodXZ, false, false},
		{"none", CompressNone, MethodNone, false, false},
		{"encrypted", CompressZstd, MethodZstd, true, false},
		{"signed", CompressXZ, MethodXZ, false, true},
		{"encrypted and signed", CompressNone, MethodNone, true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := PackOptions{Inputs: inputs, Compression: c.comp}
			if c.enc {
				o.Passphrase = passphrase("correct horse", nil)
			}
			if c.sign {
				o.SignKey = testKey()
			}
			var last int64
			o.Progress = func(done, total int64) { last = done }
			stream, res := pack(t, o)
			h := res.Header
			if h.Method != c.want || h.Encrypted != c.enc || h.Signed != c.sign {
				t.Fatalf("header %+v", h)
			}
			if res.Files != 6 || res.Dirs != 5 || last != res.Bytes || res.Bytes != 15000+12+4+500+10 {
				t.Fatalf("result %+v, last progress %d", res, last)
			}
			out := t.TempDir()
			uo := UnpackOptions{OutputDir: out, Passphrase: passphrase("correct horse", nil)}
			var ulast int64
			uo.Progress = func(done, total int64) { ulast = done }
			ur, err := unpack(stream, uo)
			if err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(read(t, out), sample) {
				t.Fatalf("unpacked tree differs:\n%v", read(t, out))
			}
			if ur.Files != 6 || ur.Dirs != 5 || ur.Bytes != res.Bytes || ulast != res.Bytes || ur.Header != h {
				t.Fatalf("unpack result %+v", ur)
			}
			if c.sign != (ur.Signer != nil) || ur.Trusted {
				t.Fatalf("signer %x trusted %v", ur.Signer, ur.Trusted)
			}
			st, err := os.Stat(filepath.Join(out, "docs", "a.txt"))
			must(t, err)
			if !st.ModTime().Equal(testTime) {
				t.Fatalf("mtime %v", st.ModTime())
			}
			st, err = os.Stat(filepath.Join(out, "docs", "deep"))
			must(t, err)
			if !st.ModTime().Equal(testTime) {
				t.Fatalf("directory mtime %v", st.ModTime())
			}
		})
	}
}

func TestModes(t *testing.T) {
	if isWindows() {
		t.Skip("Windows keeps only a read-only bit, not Unix permission bits")
	}
	src := t.TempDir()
	in := tree{"m/": "", "m/x": "x", "m/ro/": "", "m/ro/y": "y"}.write(t, src)
	must(t, os.Chmod(filepath.Join(src, "m", "x"), 0o751))
	must(t, os.Chmod(filepath.Join(src, "m", "ro", "y"), 0o400))
	must(t, os.Chmod(filepath.Join(src, "m", "ro"), 0o500))
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(src, "m", "ro"), 0o755) })
	stream, _ := pack(t, PackOptions{Inputs: in})
	out := t.TempDir()
	_, err := unpack(stream, UnpackOptions{OutputDir: out})
	must(t, err)
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(out, "m", "ro"), 0o755) })
	for p, want := range map[string]os.FileMode{"m/x": 0o751, "m/ro/y": 0o400, "m/ro": 0o500} {
		st, err := os.Stat(filepath.Join(out, p))
		must(t, err)
		if st.Mode().Perm() != want {
			t.Errorf("%s: mode %v, want %v", p, st.Mode().Perm(), want)
		}
	}
}

func TestInputs(t *testing.T) {
	src := t.TempDir()
	tree{"a/b/c.txt": "c", "a/b/d/": "", "a/b/d/e": "e", "x/c.txt": "other", "y/": ""}.write(t, src)
	cases := []struct {
		inputs []string
		want   []string
	}{
		{[]string{"a/b/c.txt"}, []string{"c.txt"}},
		{[]string{"a/b/d"}, []string{"d", "d/e"}},
		{[]string{"a/b/d/"}, []string{"d", "d/e"}},
		{[]string{"a/b/d", "a/b/c.txt"}, []string{"c.txt", "d", "d/e"}},
		{[]string{"a"}, []string{"a", "a/b", "a/b/c.txt", "a/b/d", "a/b/d/e"}},
		{[]string{"y"}, []string{"y"}},
	}
	for _, c := range cases {
		var full []string
		for _, in := range c.inputs {
			full = append(full, filepath.Join(src, filepath.FromSlash(in)))
		}
		got, warns, err := collect(full)
		if err != nil || len(warns) != 0 {
			t.Fatalf("collect(%v): %v %v", c.inputs, warns, err)
		}
		var paths []string
		for _, s := range got {
			paths = append(paths, s.Path)
		}
		if fmt.Sprint(paths) != fmt.Sprint(c.want) {
			t.Errorf("collect(%v) = %v, want %v", c.inputs, paths, c.want)
		}
	}
	// "." is stored under the name of the folder it names.
	t.Chdir(filepath.Join(src, "a", "b"))
	got, _, err := collect([]string{"."})
	if err != nil || len(got) != 4 || got[0].Path != "b" || !got[0].Dir {
		t.Fatalf("collect(.) = %v, %v", got, err)
	}

	for name, inputs := range map[string][]string{
		"same base name":   {filepath.Join(src, "a/b/c.txt"), filepath.Join(src, "x/c.txt")},
		"same input twice": {filepath.Join(src, "y"), filepath.Join(src, "y")},
		"root":             {filepath.VolumeName(src) + string(filepath.Separator)},
		"no inputs":        nil,
	} {
		t.Run(name, func(t *testing.T) {
			var ie *InputError
			if _, _, err := collect(inputs); !errors.As(err, &ie) {
				t.Fatalf("collect = %v, want an InputError", err)
			}
		})
	}
	if _, _, err := collect([]string{filepath.Join(src, "missing")}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing input: %v", err)
	}
	if !isWindows() {
		for _, name := range []string{"back\\slash", "colon:", "dot.", "CON", "ctl\x1b"} {
			bad := filepath.Join(src, "bad")
			must(t, os.MkdirAll(bad, 0o755))
			must(t, os.WriteFile(filepath.Join(bad, name), nil, 0o644))
			var ie *InputError
			if _, _, err := collect([]string{bad}); !errors.As(err, &ie) {
				t.Errorf("name %q: %v, want an InputError", name, err)
			}
			must(t, os.RemoveAll(bad))
		}
	}
}

func TestInputsCaseCollision(t *testing.T) {
	src := t.TempDir()
	must(t, os.WriteFile(filepath.Join(src, "README"), nil, 0o644))
	must(t, os.Mkdir(filepath.Join(src, "d"), 0o755))
	must(t, os.WriteFile(filepath.Join(src, "d", "readme"), nil, 0o644))
	var ie *InputError
	if _, _, err := collect([]string{filepath.Join(src, "README"), filepath.Join(src, "d", "readme")}); !errors.As(err, &ie) {
		t.Fatalf("case collision: %v", err)
	}
}

func TestSkipsLinks(t *testing.T) {
	src := t.TempDir()
	in := tree{"d/": "", "d/f": "f"}.write(t, src)
	if err := os.Symlink("f", filepath.Join(src, "d", "link")); err != nil {
		t.Skipf("cannot create a symbolic link here: %v", err)
	}
	must(t, os.Symlink(filepath.Join(src, "d"), filepath.Join(src, "dirlink")))
	got, warns, err := collect(append(in, filepath.Join(src, "dirlink")))
	if err != nil || len(got) != 2 {
		t.Fatalf("collect: %v %v", got, err)
	}
	if len(warns) != 2 || warns[0].Code != WarnSkippedFile || warns[1].Code != WarnSkippedFile {
		t.Fatalf("warnings %+v", warns)
	}
	_, res := pack(t, PackOptions{Inputs: in})
	if len(res.Warnings) != 1 || res.Warnings[0].Path != "d/link" {
		t.Fatalf("Pack warnings %+v", res.Warnings)
	}
}

func TestWrongPassphrase(t *testing.T) {
	src := t.TempDir()
	stream, _ := pack(t, PackOptions{Inputs: tree{"f": "secret"}.write(t, src), Passphrase: passphrase("right", nil)})
	out := t.TempDir()
	if _, err := unpack(stream, UnpackOptions{OutputDir: out, Passphrase: passphrase("wrong", nil)}); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	if _, err := unpack(stream, UnpackOptions{OutputDir: out}); !errors.Is(err, ErrPassphraseRequired) {
		t.Fatalf("no passphrase: %v", err)
	}
	boom := errors.New("prompt failed")
	if _, err := unpack(stream, UnpackOptions{OutputDir: out, Passphrase: func() (string, error) { return "", boom }}); !errors.Is(err, boom) {
		t.Fatalf("failing prompt: %v", err)
	}
	expectEmpty(t, out)
	var s spool
	if _, err := Pack(context.Background(), &s, PackOptions{Inputs: []string{filepath.Join(src, "f")},
		Passphrase: func() (string, error) { return "", boom }}); !errors.Is(err, boom) {
		t.Fatalf("Pack with failing prompt: %v", err)
	}
	if _, err := Pack(context.Background(), &s, PackOptions{Inputs: []string{filepath.Join(src, "f")},
		Passphrase: passphrase("", nil)}); err == nil {
		t.Fatal("Pack with an empty passphrase succeeded")
	}
}

// TestNotPassphraseEncrypted checks that a body encrypted to an X25519 recipient is a format error,
// not a wrong passphrase, and that a hostile scrypt work factor is refused before scrypt runs.
func TestEncryptionFormatErrors(t *testing.T) {
	src := t.TempDir()
	stream, _ := pack(t, PackOptions{Inputs: tree{"f": "secret"}.write(t, src), Passphrase: passphrase("pw", nil)})
	h, err := ParseHeader(stream)
	must(t, err)
	body := stream[HeaderSize:]
	// Raise the scrypt work factor in the age header from 10 to 30.
	hostile := bytes.Replace(body, []byte(" 10\n"), []byte(" 30\n"), 1)
	if bytes.Equal(hostile, body) {
		t.Fatal("work factor not found in the age header")
	}
	start := time.Now()
	_, err = unpack(rawWithHeader(h, hostile), UnpackOptions{OutputDir: t.TempDir(), Passphrase: passphrase("pw", nil)})
	if !isFormat(err) || time.Since(start) > 5*time.Second {
		t.Fatalf("work factor 30: %v after %v", err, time.Since(start))
	}
	id, err := age.GenerateX25519Identity()
	must(t, err)
	var b bytes.Buffer
	w, err := age.Encrypt(&b, id.Recipient())
	must(t, err)
	_, _ = w.Write([]byte("x"))
	must(t, w.Close())
	_, err = unpack(rawWithHeader(h, b.Bytes()), UnpackOptions{OutputDir: t.TempDir(), Passphrase: passphrase("pw", nil)})
	if !isFormat(err) || errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("X25519 body: %v", err)
	}
	_, err = unpack(rawWithHeader(h, []byte("not age")), UnpackOptions{OutputDir: t.TempDir(), Passphrase: passphrase("pw", nil)})
	if !isFormat(err) {
		t.Fatalf("garbage body: %v", err)
	}
}

// rawWithHeader returns an unsigned stream with h's flags and archive length around body.
func rawWithHeader(h Header, body []byte) []byte {
	h.Signed, h.BodyLen = false, int64(len(body))
	m := h.Marshal()
	return append(m[:], body...)
}

func signedStream(t *testing.T, encrypt bool) []byte {
	t.Helper()
	o := PackOptions{Inputs: tree{"d/": "", "d/f": "signed content"}.write(t, t.TempDir()), SignKey: testKey(),
		Compression: CompressNone}
	if encrypt {
		o.Passphrase = passphrase("pw", nil)
	}
	stream, _ := pack(t, o)
	return stream
}

func TestSignatureBitFlips(t *testing.T) {
	stream := signedStream(t, false)
	pub := testKey().Public().(ed25519.PublicKey)
	v, err := Verify(bytes.NewReader(stream), int64(len(stream)), nil)
	if err != nil || !v.Signer.Equal(pub) || v.Trusted {
		t.Fatalf("Verify: %+v %v", v, err)
	}
	if v, err = Verify(bytes.NewReader(stream), int64(len(stream)), []ed25519.PublicKey{pub}); err != nil || !v.Trusted {
		t.Fatalf("Verify with the key: %+v %v", v, err)
	}
	for bit := range len(stream) * 8 {
		s := bytes.Clone(stream)
		s[bit/8] ^= 1 << (bit % 8)
		if _, err := Verify(bytes.NewReader(s), int64(len(s)), nil); err == nil {
			t.Fatalf("bit %d flipped: verified", bit)
		}
	}
	// Through Unpack: a flipped bit in each region writes nothing.
	for _, off := range []int{3, 10, HeaderSize + 5, len(stream) - 90, len(stream) - 1} {
		s := bytes.Clone(stream)
		s[off] ^= 0x10
		out := t.TempDir()
		if _, err := unpack(s, UnpackOptions{OutputDir: out}); err == nil {
			t.Fatalf("offset %d: unpacked", off)
		}
		expectEmpty(t, out)
	}
}

func TestVerifyBeforePassphrase(t *testing.T) {
	stream := signedStream(t, true)
	pub := testKey().Public().(ed25519.PublicKey)
	other, _, err := GenerateKey(nil)
	must(t, err)
	unsigned, _ := pack(t, PackOptions{Inputs: tree{"f": "x"}.write(t, t.TempDir()), Passphrase: passphrase("pw", nil)})
	tampered := bytes.Clone(stream)
	tampered[HeaderSize+40] ^= 1
	cases := []struct {
		name   string
		stream []byte
		keys   []ed25519.PublicKey
		want   error
	}{
		{"tampered", tampered, nil, ErrBadSignature},
		{"tampered, key given", tampered, []ed25519.PublicKey{pub}, ErrBadSignature},
		{"other key", stream, []ed25519.PublicKey{other}, ErrMissingSignature},
		{"unsigned, key given", unsigned, []ed25519.PublicKey{pub}, ErrMissingSignature},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			out := t.TempDir()
			_, err := unpack(c.stream, UnpackOptions{OutputDir: out, VerifyKeys: c.keys, Passphrase: passphrase("pw", &calls)})
			if !errors.Is(err, c.want) || calls != 0 {
				t.Fatalf("err %v, passphrase requested %d times", err, calls)
			}
			expectEmpty(t, out)
		})
	}
	calls := 0
	res, err := unpack(stream, UnpackOptions{OutputDir: t.TempDir(), VerifyKeys: []ed25519.PublicKey{other, pub},
		Passphrase: passphrase("pw", &calls)})
	if err != nil || calls != 1 || !res.Trusted || !res.Signer.Equal(pub) {
		t.Fatalf("trusted: %+v %v %d", res, err, calls)
	}
}

func TestStreamRejects(t *testing.T) {
	good, _ := pack(t, PackOptions{Inputs: tree{"f": "x"}.write(t, t.TempDir())})
	signed := signedStream(t, false)
	cases := map[string]struct {
		s    []byte
		want func(error) bool
	}{
		"empty":            {nil, isFormat},
		"short":            {good[:10], isFormat},
		"truncated":        {good[:len(good)-1], isFormat},
		"extended":         {append(bytes.Clone(good), 0), isFormat},
		"signed truncated": {signed[:len(signed)-1], isFormat},
		"version":          {flip(good, 4, 3), isUnsupported("stream version", 2)},
		"reserved":         {flip(good, 7, 1), isFormat},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			out := t.TempDir()
			if _, err := unpack(c.s, UnpackOptions{OutputDir: out}); !c.want(err) {
				t.Fatalf("Unpack: %v", err)
			}
			expectEmpty(t, out)
		})
	}
}

func flip(b []byte, off int, mask byte) []byte {
	b = bytes.Clone(b)
	b[off] ^= mask
	return b
}

func TestLimits(t *testing.T) {
	t.Run("stream length", func(t *testing.T) {
		_, err := Verify(bytes.NewReader(nil), MaxStreamLen+1, nil)
		var l *LimitError
		if !errors.As(err, &l) || l.Limit != "stream length" || l.Max != MaxStreamLen {
			t.Fatalf("Verify: %v", err)
		}
	})
	t.Run("archive length over max size", func(t *testing.T) {
		stream, _ := pack(t, PackOptions{Inputs: tree{"f": strings.Repeat("x", 5000)}.write(t, t.TempDir()), Passphrase: passphrase("pw", nil)})
		calls := 0
		out := t.TempDir()
		_, err := unpack(stream, UnpackOptions{OutputDir: out, MaxSize: 4000, Passphrase: passphrase("pw", &calls)})
		if !isLimit("archive length")(err) || calls != 0 {
			t.Fatalf("Unpack: %v (passphrase calls %d)", err, calls)
		}
		expectEmpty(t, out)
	})
	t.Run("unpacked size default", func(t *testing.T) {
		m := MarshalManifest([]Entry{{Path: "big", Size: DefaultMaxSize + 1, Mode: 0o644, MTime: testTime}})
		out := t.TempDir()
		_, err := unpack(rawStream(archive(t, m)), UnpackOptions{OutputDir: out})
		var l *LimitError
		if !errors.As(err, &l) || l.Limit != "unpacked size" || l.Max != DefaultMaxSize {
			t.Fatalf("Unpack: %v", err)
		}
		expectEmpty(t, out)
	})
	t.Run("unpacked size", func(t *testing.T) {
		es := []tarEntry{fileHdr("a", "12345"), fileHdr("b", "123456")}
		a := archive(t, MarshalManifest(manifestFor(es...)), es...)
		out := t.TempDir()
		_, err := unpackArchive(context.Background(), bytes.NewReader(a), int64(len(a)), UnpackOptions{OutputDir: out, MaxSize: 10})
		if !isLimit("unpacked size")(err) {
			t.Fatalf("unpackArchive: %v", err)
		}
		expectEmpty(t, out)
	})
	t.Run("manifest size", func(t *testing.T) {
		var a bytes.Buffer
		must(t, binary.Write(&a, binary.BigEndian, uint32(MaxManifestBytes+1)))
		a.Write(make([]byte, 1024))
		out := t.TempDir()
		_, err := unpackArchive(context.Background(), &a, MaxManifestBytes+2000, UnpackOptions{OutputDir: out})
		var l *LimitError
		if !errors.As(err, &l) || l.Limit != "manifest size" || l.Value != MaxManifestBytes+1 {
			t.Fatalf("unpackArchive: %v", err)
		}
	})
	t.Run("decompresses past its length", func(t *testing.T) {
		es := []tarEntry{fileHdr("a", "content")}
		a := archive(t, MarshalManifest(manifestFor(es...)), es...)
		for _, comp := range []Compression{CompressNone, CompressZstd, CompressXZ} {
			body := compressForTest(t, comp, a)
			h := Header{Method: methodOf(comp), ArchiveLen: int64(len(a)) - 1}
			out := t.TempDir()
			if _, err := unpack(rawWithHeader(h, body), UnpackOptions{OutputDir: out}); !isLimit("archive length")(err) {
				t.Fatalf("%v: %v", comp, err)
			}
			expectEmpty(t, out)
			h.ArchiveLen += 2
			if _, err := unpack(rawWithHeader(h, body), UnpackOptions{OutputDir: out}); !isFormat(err) {
				t.Fatalf("%v, archive shorter than declared: %v", comp, err)
			}
			expectEmpty(t, out)
		}
	})
}

func methodOf(c Compression) Method {
	return map[Compression]Method{CompressNone: MethodNone, CompressZstd: MethodZstd, CompressXZ: MethodXZ}[c]
}

func compressForTest(t *testing.T, c Compression, a []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w, err := compressor(&b, methodOf(c))
	must(t, err)
	_, err = w.Write(a)
	must(t, err)
	must(t, w.Close())
	return b.Bytes()
}

func TestCorruptCompressedBody(t *testing.T) {
	es := []tarEntry{fileHdr("a", strings.Repeat("content ", 1000))}
	a := archive(t, MarshalManifest(manifestFor(es...)), es...)
	for _, comp := range []Compression{CompressZstd, CompressXZ} {
		body := compressForTest(t, comp, a)
		body[len(body)/2] ^= 0xff
		h := Header{Method: methodOf(comp), ArchiveLen: int64(len(a))}
		out := t.TempDir()
		if _, err := unpack(rawWithHeader(h, body), UnpackOptions{OutputDir: out}); err == nil {
			t.Fatalf("%v: corrupt body unpacked", comp)
		}
		expectEmpty(t, out)
		h.ArchiveLen = int64(len(a))
		if _, err := unpack(rawWithHeader(h, []byte("garbage")), UnpackOptions{OutputDir: out}); !isFormat(err) {
			t.Fatalf("%v: garbage body: %v", comp, err)
		}
	}
}

func TestArchiveRejects(t *testing.T) {
	f, g, d := fileHdr("a", "aaa"), fileHdr("b", "bbb"), dirHdr("d")
	link := tarEntry{tar.Header{Typeflag: tar.TypeSymlink, Name: "a", Linkname: "/etc/passwd", ModTime: testTime}, ""}
	hard := tarEntry{tar.Header{Typeflag: tar.TypeLink, Name: "a", Linkname: "b", ModTime: testTime}, ""}
	char := tarEntry{tar.Header{Typeflag: tar.TypeChar, Name: "a", Devmajor: 1, Devminor: 3, ModTime: testTime}, ""}
	block := tarEntry{tar.Header{Typeflag: tar.TypeBlock, Name: "a", Devmajor: 8, ModTime: testTime}, ""}
	fifo := tarEntry{tar.Header{Typeflag: tar.TypeFifo, Name: "a", ModTime: testTime}, ""}
	cases := map[string]struct {
		manifest []Entry
		tar      []tarEntry
	}{
		"symlink":             {manifestFor(f), []tarEntry{link}},
		"hard link":           {manifestFor(f), []tarEntry{hard}},
		"char device":         {manifestFor(f), []tarEntry{char}},
		"block device":        {manifestFor(f), []tarEntry{block}},
		"fifo":                {manifestFor(f), []tarEntry{fifo}},
		"extra entry":         {manifestFor(f), []tarEntry{f, g}},
		"missing entry":       {manifestFor(f, g), []tarEntry{f}},
		"out of order":        {manifestFor(f, g), []tarEntry{g, f}},
		"other name":          {manifestFor(f), []tarEntry{fileHdr("A", "aaa")}},
		"size mismatch":       {manifestFor(f), []tarEntry{fileHdr("a", "aaaa")}},
		"hash mismatch":       {manifestFor(f), []tarEntry{fileHdr("a", "aab")}},
		"dir as file":         {manifestFor(d), []tarEntry{{tar.Header{Typeflag: tar.TypeReg, Name: "d", ModTime: testTime}, ""}}},
		"file as dir":         {manifestFor(f), []tarEntry{dirHdr("a")}},
		"dir slash twice":     {manifestFor(d), []tarEntry{{tar.Header{Typeflag: tar.TypeDir, Name: "d//", ModTime: testTime}, ""}}},
		"global pax header":   {manifestFor(f), []tarEntry{{tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "g", PAXRecords: map[string]string{"comment": "x"}}, ""}, f}},
		"bad manifest path":   {[]Entry{entry("../x", false)}, nil},
		"unsafe tar name":     {manifestFor(f), []tarEntry{fileHdr("../a", "aaa")}},
		"file under file":     {[]Entry{entry("a", false), entry("a/b", false)}, nil},
		"case collision":      {[]Entry{entry("a", false), entry("A", false)}, nil},
		"normalisation":       {[]Entry{entry("é", false)}, nil},
		"windows device name": {[]Entry{entry("aux.c", false)}, nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			a := archive(t, MarshalManifest(c.manifest), c.tar...)
			out := t.TempDir()
			_, err := unpackArchive(context.Background(), bytes.NewReader(a), int64(len(a)), UnpackOptions{OutputDir: out})
			if !isUnsafe(err) {
				t.Fatalf("unpackArchive: %v", err)
			}
			expectEmpty(t, out)
		})
	}
	// Archive-level format errors.
	for name, a := range map[string][]byte{
		"no manifest length": {0, 0},
		"manifest past end":  {0, 0, 1, 0, '{'},
		"bad manifest":       archive(t, []byte(`{"version":1}`)),
		"bad tar":            append(archive(t, MarshalManifest(manifestFor(f)))[:4+len(MarshalManifest(manifestFor(f)))], bytes.Repeat([]byte{1}, 1024)...),
	} {
		t.Run(name, func(t *testing.T) {
			out := t.TempDir()
			_, err := unpackArchive(context.Background(), bytes.NewReader(a), int64(len(a)), UnpackOptions{OutputDir: out})
			if !isFormat(err) && !isUnsafe(err) {
				t.Fatalf("unpackArchive: %v", err)
			}
			expectEmpty(t, out)
		})
	}
}

func TestTarDirNameWithoutSlash(t *testing.T) {
	d := tarEntry{tar.Header{Typeflag: tar.TypeDir, Name: "d", Mode: 0o755, ModTime: testTime}, ""}
	a := archive(t, MarshalManifest(manifestFor(dirHdr("d"))), d)
	out := t.TempDir()
	if _, err := unpackArchive(context.Background(), bytes.NewReader(a), int64(len(a)), UnpackOptions{OutputDir: out}); err != nil {
		t.Fatal(err)
	}
}

func TestExistingFiles(t *testing.T) {
	src := t.TempDir()
	stream, _ := pack(t, PackOptions{Inputs: tree{"d/": "", "d/f": "new", "g": "new g"}.write(t, src)})
	out := t.TempDir()
	must(t, os.Mkdir(filepath.Join(out, "d"), 0o755))
	must(t, os.WriteFile(filepath.Join(out, "d", "f"), []byte("old"), 0o644))
	must(t, os.WriteFile(filepath.Join(out, "keep"), []byte("keep"), 0o644))
	var ee *ExistsError
	if _, err := unpack(stream, UnpackOptions{OutputDir: out}); !errors.As(err, &ee) || ee.Path != "d/f" {
		t.Fatalf("no overwrite: %v", err)
	}
	want := tree{"d/": "", "d/f": "old", "keep": "keep"}
	if got := read(t, out); !maps.Equal(got, want) {
		t.Fatalf("output changed: %v", got)
	}
	if _, err := unpack(stream, UnpackOptions{OutputDir: out, Overwrite: true}); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	want = tree{"d/": "", "d/f": "new", "g": "new g", "keep": "keep"}
	if got := read(t, out); !maps.Equal(got, want) {
		t.Fatalf("after overwrite: %v", got)
	}
	// A directory where a file goes, and a file where a directory goes, are refused even with Overwrite.
	out2 := t.TempDir()
	must(t, os.Mkdir(filepath.Join(out2, "g"), 0o755))
	if _, err := unpack(stream, UnpackOptions{OutputDir: out2, Overwrite: true}); !errors.As(err, &ee) {
		t.Fatalf("dir in the way: %v", err)
	}
	out3 := t.TempDir()
	must(t, os.WriteFile(filepath.Join(out3, "d"), nil, 0o644))
	if _, err := unpack(stream, UnpackOptions{OutputDir: out3, Overwrite: true}); !errors.As(err, &ee) {
		t.Fatalf("file in the way: %v", err)
	}
	// A missing output folder is created.
	out4 := filepath.Join(t.TempDir(), "new", "folder")
	if _, err := unpack(stream, UnpackOptions{OutputDir: out4}); err != nil {
		t.Fatalf("new output folder: %v", err)
	}
	// An output "folder" that is a file fails.
	if _, err := unpack(stream, UnpackOptions{OutputDir: filepath.Join(out3, "d")}); err == nil {
		t.Fatal("output folder is a file")
	}
}

func TestSymlinkInOutput(t *testing.T) {
	stream, _ := pack(t, PackOptions{Inputs: tree{"d/": "", "d/f": "x"}.write(t, t.TempDir())})
	out, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(out, "d")); err != nil {
		t.Skipf("cannot create a symbolic link here: %v", err)
	}
	var ee *ExistsError
	if _, err := unpack(stream, UnpackOptions{OutputDir: out, Overwrite: true}); !errors.As(err, &ee) {
		t.Fatalf("symlink in the way: %v", err)
	}
	expectEmpty(t, outside)
}

func TestCancel(t *testing.T) {
	in := tree{"f": "x"}.write(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var s spool
	if _, err := Pack(ctx, &s, PackOptions{Inputs: in}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Pack: %v", err)
	}
	stream, _ := pack(t, PackOptions{Inputs: in})
	out := t.TempDir()
	if _, err := Unpack(ctx, bytes.NewReader(stream), int64(len(stream)), UnpackOptions{OutputDir: out}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Unpack: %v", err)
	}
	expectEmpty(t, out)
}

func TestInputChanged(t *testing.T) {
	src := t.TempDir()
	in := tree{"f": "original"}.write(t, src)
	got, _, err := collect(in)
	must(t, err)
	must(t, os.WriteFile(filepath.Join(src, "f"), []byte("changed!"), 0o644))
	var s spool
	if _, err := writeStream(context.Background(), &s, got, PackOptions{}); !errors.Is(err, ErrInputChanged) {
		t.Fatalf("same size, new content: %v", err)
	}
	must(t, os.WriteFile(filepath.Join(src, "f"), []byte("short"), 0o644))
	if _, err := writeStream(context.Background(), &(spool{}), got, PackOptions{}); !errors.Is(err, ErrInputChanged) {
		t.Fatalf("shrunk: %v", err)
	}
	must(t, os.Remove(filepath.Join(src, "f")))
	if _, err := writeStream(context.Background(), &(spool{}), got, PackOptions{}); err == nil {
		t.Fatal("removed input packed")
	}
}

func TestAutoCompression(t *testing.T) {
	src := t.TempDir()
	random := make([]byte, 3<<20)
	var x uint64 = 1
	for i := range random {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		random[i] = byte(x)
	}
	must(t, os.WriteFile(filepath.Join(src, "random.bin"), random, 0o644))
	_, res := pack(t, PackOptions{Inputs: []string{filepath.Join(src, "random.bin")}})
	if res.Header.Method != MethodNone {
		t.Fatalf("random data: method %d", res.Header.Method)
	}
	_, res = pack(t, PackOptions{Inputs: []string{filepath.Join(src, "random.bin")}, Compression: CompressZstd})
	if res.Header.Method != MethodZstd {
		t.Fatalf("forced zstd: method %d", res.Header.Method)
	}
	var s spool
	if _, err := Pack(context.Background(), &s, PackOptions{Inputs: []string{filepath.Join(src, "random.bin")}, Compression: 9}); err == nil {
		t.Fatal("unknown compression accepted")
	}
}

func TestPackWriteError(t *testing.T) {
	in := tree{"f": "x"}.write(t, t.TempDir())
	for n := range 4 {
		if _, err := Pack(context.Background(), &failing{after: n}, PackOptions{Inputs: in}); err == nil {
			t.Fatalf("failing destination after %d calls: no error", n)
		}
	}
}

// failing is a spool whose calls start to fail after a number of successful ones.
type failing struct {
	spool
	after int
}

func (f *failing) fail() bool { f.after--; return f.after < 0 }

func (f *failing) Write(p []byte) (int, error) {
	if f.fail() {
		return 0, errors.New("disk full")
	}
	return f.spool.Write(p)
}

func (f *failing) Seek(o int64, w int) (int64, error) {
	if f.fail() {
		return 0, errors.New("seek failed")
	}
	return f.spool.Seek(o, w)
}

func TestStreamHashMatchesFile(t *testing.T) {
	in := tree{"f": "x"}.write(t, t.TempDir())
	spoolFile, err := os.CreateTemp(t.TempDir(), "spool")
	must(t, err)
	defer func() { _ = spoolFile.Close() }()
	res, err := Pack(context.Background(), spoolFile, PackOptions{Inputs: in, SignKey: testKey()})
	must(t, err)
	b, err := os.ReadFile(spoolFile.Name())
	must(t, err)
	if sha256.Sum256(b) != res.StreamHash || int64(len(b)) != res.StreamLen {
		t.Fatal("stream file does not match the result")
	}
}
