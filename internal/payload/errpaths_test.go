package payload

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestErrorStrings(t *testing.T) {
	for _, err := range []error{
		&UnsafeArchiveError{"p", "r"}, &InputError{"p", "r"}, &ExistsError{"p", "r"},
		&LimitError{"l", 2, 1}, &UnsupportedVersionError{"v", 3},
	} {
		if s := err.Error(); !strings.HasPrefix(s, "payload: ") || len(s) < 15 {
			t.Errorf("%T: %q", err, s)
		}
	}
}

// failAll is a spool whose Write, Seek and Read calls fail after a number of successful ones.
type failAll struct {
	spool
	after int
}

func (f *failAll) fail() bool { f.after--; return f.after < 0 }

func (f *failAll) Write(p []byte) (int, error) {
	if f.fail() {
		return 0, errors.New("write failed")
	}
	return f.spool.Write(p)
}

func (f *failAll) Seek(o int64, w int) (int64, error) {
	if f.fail() {
		return 0, errors.New("seek failed")
	}
	return f.spool.Seek(o, w)
}

func (f *failAll) Read(p []byte) (int, error) {
	if f.fail() {
		return 0, errors.New("read failed")
	}
	return f.spool.Read(p)
}

func TestPackFailures(t *testing.T) {
	in := tree{"d/": "", "d/f": "content", "g": strings.Repeat("x", 100)}.write(t, t.TempDir())
	for _, o := range []PackOptions{
		{Compression: CompressNone, SignKey: testKey()},
		{Compression: CompressXZ, Passphrase: passphrase("pw", nil)},
	} {
		o.Inputs = in
		for n := 0; ; n++ {
			_, err := Pack(context.Background(), &failAll{after: n}, o)
			if err == nil {
				break
			}
			if n > 1000 {
				t.Fatal("Pack keeps failing")
			}
		}
	}
}

// cutReaderAt fails every read that reaches offset cut.
type cutReaderAt struct {
	b   []byte
	cut int64
}

func (c cutReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > c.cut {
		return 0, errors.New("read failed")
	}
	return bytes.NewReader(c.b).ReadAt(p, off)
}

func TestVerifyReadErrors(t *testing.T) {
	s := signedStream(t, false)
	for _, cut := range []int64{0, HeaderSize + 1, int64(len(s)) - 10} {
		if _, err := Verify(context.Background(), cutReaderAt{s, cut}, int64(len(s)), nil); err == nil || isFormat(err) {
			t.Fatalf("cut at %d: %v", cut, err)
		}
	}
}

func TestEmptyPassphrase(t *testing.T) {
	stream, _ := pack(t, PackOptions{Inputs: tree{"f": "x"}.write(t, t.TempDir()), Passphrase: passphrase("pw", nil)})
	if _, err := unpack(stream, UnpackOptions{OutputDir: t.TempDir(), Passphrase: passphrase("", nil)}); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("empty passphrase: %v", err)
	}
}

// errAfter yields b and then fails with err.
type errAfter struct {
	r   io.Reader
	err error
}

func (e *errAfter) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if errors.Is(err, io.EOF) {
		return n, e.err
	}
	return n, err
}

func TestArchiveReadErrors(t *testing.T) {
	es := []tarEntry{fileHdr("a", "content")}
	m := MarshalManifest(manifestFor(es...))
	a := archive(t, m, es...)
	boom := errors.New("device error")
	cases := map[string]struct {
		r    io.Reader
		n    int64
		want func(error) bool
	}{
		"manifest cut short":     {bytes.NewReader(a[:10]), int64(len(a)), isFormat},
		"error in the manifest":  {&errAfter{bytes.NewReader(a[:10]), boom}, int64(len(a)), isFormat},
		"error in a file":        {&errAfter{bytes.NewReader(a[:4+len(m)+600]), boom}, int64(len(a)), isFormat},
		"error after the end":    {&errAfter{bytes.NewReader(a), boom}, int64(len(a)), isFormat},
		"error in the tar":       {&errAfter{bytes.NewReader(a[:4+len(m)+100]), boom}, int64(len(a)), isFormat},
		"ends inside the tar":    {bytes.NewReader(a[:4+len(m)+100]), int64(len(a)), isFormat},
		"file cut short in tar":  {bytes.NewReader(a[:4+len(m)+515]), int64(4 + len(m) + 515), isFormat},
		"declared beyond length": {bytes.NewReader(append(bytes.Clone(a), 1)), int64(len(a)), isLimit("archive length")},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			out := t.TempDir()
			_, err := unpackArchive(context.Background(), c.r, c.n, UnpackOptions{OutputDir: out})
			if !c.want(err) {
				t.Fatalf("unpackArchive: %v", err)
			}
			expectEmpty(t, out)
		})
	}
}

func TestParentIsFile(t *testing.T) {
	es := []tarEntry{dirHdr("a"), fileHdr("a/b", "x")}
	a := archive(t, MarshalManifest(manifestFor(es...)), es...)
	out := t.TempDir()
	must(t, os.WriteFile(filepath.Join(out, "a"), nil, 0o600))
	var ee *ExistsError
	if _, err := unpackArchive(context.Background(), bytes.NewReader(a), int64(len(a)), UnpackOptions{OutputDir: out}); !errors.As(err, &ee) {
		t.Fatalf("parent is a file: %v", err)
	}
	// Without its folder as an entry, the manifest itself is refused.
	a = archive(t, MarshalManifest(manifestFor(es[1])), es[1])
	if _, err := unpackArchive(context.Background(), bytes.NewReader(a), int64(len(a)), UnpackOptions{OutputDir: t.TempDir()}); !isUnsafe(err) {
		t.Fatalf("folder not an entry: %v", err)
	}
}

func TestPermissionErrors(t *testing.T) {
	if isWindows() {
		t.Skip("Windows has no Unix permission bits to remove")
	}
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not stop root")
	}
	src := t.TempDir()
	in := tree{"f": "x", "d/": "", "d/g": "y"}.write(t, src)
	must(t, os.Chmod(filepath.Join(src, "f"), 0))
	if _, _, err := collect(in[1:]); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("unreadable file: %v", err)
	}
	must(t, os.Chmod(filepath.Join(src, "d"), 0))
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(src, "d"), 0o700) }) //nolint:gosec // permission bits under test
	if _, _, err := collect(in[:1]); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("unreadable folder: %v", err)
	}

	stream, _ := pack(t, PackOptions{Inputs: tree{"h": "z"}.write(t, t.TempDir())})
	out := t.TempDir()
	must(t, os.Chmod(out, 0o500))                  //nolint:gosec // a read-only folder, on purpose
	t.Cleanup(func() { _ = os.Chmod(out, 0o700) }) //nolint:gosec // permission bits under test
	if _, err := unpack(stream, UnpackOptions{OutputDir: out}); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("read-only output folder: %v", err)
	}
	expectEmpty(t, out)
}

func TestHeaderStreamLenLimit(t *testing.T) {
	b := Header{BodyLen: MaxStreamLen}.Marshal()
	if _, err := ParseHeader(b[:]); !isLimit("stream length")(err) {
		t.Fatalf("body of MaxStreamLen: %v", err)
	}
	b = Header{BodyLen: MaxStreamLen - HeaderSize}.Marshal()
	if _, err := ParseHeader(b[:]); err != nil {
		t.Fatalf("stream of MaxStreamLen: %v", err)
	}
}

func TestManifestTruncated(t *testing.T) {
	for _, m := range []string{
		`{"version":1,"entries":[]`, `{"version":`, `{"version":1,"entries":[{"path":`,
		`{"version":1,"entries":[{"path":"a"`, `{"version":1,"entries":[{"path":"a","mode":`,
	} {
		if _, err := ParseManifest([]byte(m)); !isFormat(err) {
			t.Errorf("ParseManifest(%s) = %v", m, err)
		}
	}
	es := []Entry{entry("a", false), entry("b", false)}
	es[0].Size, es[1].Size = math.MaxInt64, 1
	var l *LimitError
	if err := ValidateManifest(es, math.MaxInt64); !errors.As(err, &l) || l.Value != math.MaxInt64 {
		t.Fatalf("overflowing sizes: %v", err)
	}
}

func TestPackManifestLimits(t *testing.T) {
	if testing.Short() {
		t.Skip("builds manifests at the limits; skipped with -short")
	}
	long := strings.Repeat("p/", 2047) + "pp"
	srcs := make([]source, MaxManifestBytes/len(long)+1)
	for i := range srcs {
		srcs[i] = source{Entry: Entry{Path: long, Dir: true}}
	}
	if _, err := writeStream(context.Background(), &spool{}, srcs, PackOptions{}); !isLimit("manifest size")(err) {
		t.Fatalf("manifest size: %v", err)
	}
	srcs = make([]source, MaxManifestEntries+1)
	if _, err := writeStream(context.Background(), &spool{}, srcs, PackOptions{}); !isLimit("manifest entries")(err) {
		t.Fatalf("manifest entries: %v", err)
	}
}
