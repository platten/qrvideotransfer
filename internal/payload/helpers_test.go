package payload

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	scryptLogN = 10 // keep tests fast; the decoder still accepts up to ScryptWorkFactor
	os.Exit(m.Run())
}

var testTime = time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)

// tree is a set of files and directories to create: a path ending in "/" is a directory.
type tree map[string]string

// write creates t under dir with fixed modification times and returns the top-level input paths.
func (t tree) write(tb testing.TB, dir string) []string {
	tb.Helper()
	paths := make([]string, 0, len(t))
	for p := range t {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, p := range paths {
		full := filepath.Join(dir, filepath.FromSlash(strings.TrimSuffix(p, "/")))
		if strings.HasSuffix(p, "/") {
			must(tb, os.MkdirAll(full, 0o750))
			continue
		}
		must(tb, os.MkdirAll(filepath.Dir(full), 0o750))
		must(tb, os.WriteFile(full, []byte(t[p]), 0o600))
	}
	slices.Reverse(paths) // children before parents, so that directory times stick
	tops := map[string]bool{}
	for _, p := range paths {
		full := filepath.Join(dir, filepath.FromSlash(strings.TrimSuffix(p, "/")))
		must(tb, os.Chtimes(full, testTime, testTime))
		tops[strings.SplitN(strings.TrimSuffix(p, "/"), "/", 2)[0]] = true
	}
	var inputs []string
	for top := range tops {
		inputs = append(inputs, filepath.Join(dir, top))
	}
	slices.Sort(inputs)
	return inputs
}

// read returns the tree under dir in the form of tree.
func read(tb testing.TB, dir string) tree {
	tb.Helper()
	t := tree{}
	must(tb, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel := filepath.ToSlash(p[len(dir)+1:])
		if d.IsDir() {
			t[rel+"/"] = ""
			return nil
		}
		b, err := os.ReadFile(p) //nolint:gosec // test helper reading its own temporary folder
		t[rel] = string(b)
		return err
	}))
	return t
}

func must(tb testing.TB, err error) {
	tb.Helper()
	if err != nil {
		tb.Fatal(err)
	}
}

// spool is an in-memory io.ReadWriteSeeker that also serves as an io.ReaderAt.
type spool struct {
	b   []byte
	off int64
}

func (s *spool) Write(p []byte) (int, error) {
	if need := int(s.off) + len(p); need > len(s.b) {
		s.b = append(s.b, make([]byte, need-len(s.b))...)
	}
	copy(s.b[s.off:], p)
	s.off += int64(len(p))
	return len(p), nil
}

func (s *spool) Read(p []byte) (int, error) {
	if s.off >= int64(len(s.b)) {
		return 0, io.EOF
	}
	n := copy(p, s.b[s.off:])
	s.off += int64(n)
	return n, nil
}

func (s *spool) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekCurrent:
		off += s.off
	case io.SeekEnd:
		off += int64(len(s.b))
	}
	if off < 0 {
		return 0, errors.New("negative seek")
	}
	s.off = off
	return off, nil
}

// pack packs inputs into memory and returns the stream.
func pack(tb testing.TB, o PackOptions) ([]byte, *PackResult) {
	tb.Helper()
	var s spool
	res, err := Pack(context.Background(), &s, o)
	if err != nil {
		tb.Fatalf("Pack: %v", err)
	}
	if int64(len(s.b)) != res.StreamLen {
		tb.Fatalf("stream is %d bytes, result says %d", len(s.b), res.StreamLen)
	}
	if sha256.Sum256(s.b) != res.StreamHash {
		tb.Fatal("StreamHash is not the SHA-256 of the stream")
	}
	return s.b, res
}

// unpack unpacks a stream held in memory.
func unpack(stream []byte, o UnpackOptions) (*UnpackResult, error) {
	return Unpack(context.Background(), bytes.NewReader(stream), int64(len(stream)), o)
}

// tarEntry is one entry of a hand-made tar stream.
type tarEntry struct {
	hdr  tar.Header
	data string
}

func fileHdr(name, data string) tarEntry {
	return tarEntry{tar.Header{Typeflag: tar.TypeReg, Name: name, Size: int64(len(data)), Mode: 0o644,
		ModTime: testTime, Format: tar.FormatPAX}, data}
}

func dirHdr(name string) tarEntry {
	return tarEntry{tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: 0o755, ModTime: testTime,
		Format: tar.FormatPAX}, ""}
}

// manifestFor returns the manifest entries that match tar entries.
func manifestFor(es ...tarEntry) []Entry {
	var m []Entry
	for _, e := range es {
		if e.hdr.Typeflag == tar.TypeDir {
			m = append(m, Entry{Path: strings.TrimSuffix(e.hdr.Name, "/"), Dir: true, Mode: 0o755, MTime: testTime})
			continue
		}
		m = append(m, Entry{Path: e.hdr.Name, Size: int64(len(e.data)), Mode: 0o644, MTime: testTime,
			SHA256: sha256.Sum256([]byte(e.data))})
	}
	return m
}

// archive builds archive bytes: the manifest length, the manifest, and a tar stream of es.
func archive(tb testing.TB, manifest []byte, es ...tarEntry) []byte {
	tb.Helper()
	var b bytes.Buffer
	must(tb, binary.Write(&b, binary.BigEndian, uint32(len(manifest))))
	b.Write(manifest)
	tw := tar.NewWriter(&b)
	for _, e := range es {
		h := e.hdr
		must(tb, tw.WriteHeader(&h))
		_, err := tw.Write([]byte(e.data))
		must(tb, err)
	}
	must(tb, tw.Close())
	return b.Bytes()
}

// rawStream wraps archive bytes in an unsigned, uncompressed, unencrypted stream.
func rawStream(a []byte) []byte {
	h := Header{Method: MethodNone, ArchiveLen: int64(len(a)), BodyLen: int64(len(a))}.Marshal()
	return append(h[:], a...)
}

// expectEmpty fails unless dir exists and is empty.
func expectEmpty(tb testing.TB, dir string) {
	tb.Helper()
	es, err := os.ReadDir(dir)
	must(tb, err)
	if len(es) != 0 {
		tb.Fatalf("output folder is not empty: %v", es)
	}
}

func isWindows() bool { return runtime.GOOS == "windows" }
