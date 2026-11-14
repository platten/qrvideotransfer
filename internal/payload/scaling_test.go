package payload

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// extractDeep extracts files beneath a chain of the given number of folders into a new folder, and
// then again once for each overwrite given (with every folder existing and every file replaced).
//
// A file has folders+1 path components. Up to 63 folders the archive goes through unpackArchive. A
// deeper chain is refused by validation (FORMAT.md section 12 allows 64 components), so it is handed
// to the extractor directly, below the validation layer: this is only to show that our own code
// stays linear at any depth, and no decoder accepts such a manifest.
func extractDeep(tb testing.TB, files, folders int, overwrites ...bool) {
	tes := deepManifest(files, folders)
	a := archive(tb, MarshalManifest(manifestFor(tes...)), tes...)
	out := tb.TempDir()
	for _, overwrite := range append([]bool{false}, overwrites...) {
		if folders < maxComponents {
			_, err := unpackArchive(context.Background(), bytes.NewReader(a), int64(len(a)), UnpackOptions{OutputDir: out, Overwrite: overwrite})
			must(tb, err)
		} else {
			extractUnvalidated(tb, manifestFor(tes...), out, overwrite)
		}
	}
}

// extractUnvalidated runs the extractor on entries of empty files and folders without applying the
// path rules. Only the scaling tests use it, for chains deeper than any decoder accepts.
func extractUnvalidated(tb testing.TB, entries []Entry, out string, overwrite bool) {
	x, err := newExtractor(context.Background(), out, entries, overwrite, nil)
	must(tb, err)
	defer x.close()
	for i, e := range entries {
		if !e.Dir {
			must(tb, x.write(i, e, strings.NewReader(""), &progressWriter{}))
		}
	}
	must(tb, x.commit())
}

// TestExtractionWorkIsLinear counts what extraction asks of the operating system: the number of
// calls that take a path, and the total number of path components in them. Each folder must cost a
// fixed number of calls of one component (they go through the parent's handle), and each file a
// fixed number of calls of at most its depth. Work quadratic in the depth, such as resolving every
// folder from the root, fails the bound at depth 64 by a factor of about 3 and at depth 1000 by more
// than 10. The counts do not depend on the speed of the machine. The chains of 1000 folders go below
// the validation layer (see extractDeep).
func TestExtractionWorkIsLinear(t *testing.T) {
	t.Cleanup(func() { testHookOS = nil })
	for _, c := range []struct{ files, depth int }{{5, 63}, {20, 63}, {20, 15}, {200, 10}, {5, 1000}, {20, 1000}} {
		var calls, components int
		testHookOS = func(p string) { calls, components = calls+1, components+strings.Count(p, "/")+1 }
		extractDeep(t, c.files, c.depth, true)
		testHookOS = nil
		folders, units := c.depth, c.files+c.depth
		t.Logf("%d files beneath %d folders: %d calls, %d path components", c.files, c.depth, calls, components)
		// Two extractions; per folder at most 10 calls each, per file at most 12.
		if calls > 2*12*units {
			t.Errorf("%d files beneath %d folders: %d calls for %d folders and files", c.files, c.depth, calls, units)
		}
		if limit := 2 * (10*folders + 12*c.files*(c.depth+2)); components > limit {
			t.Errorf("%d files beneath %d folders: %d path components, more than %d", c.files, c.depth, components, limit)
		}
	}
}

// BenchmarkExtractDeep measures one extraction into a new folder of files beneath folder chains
// within the limit of 64 components (SECURITY.md section 5).
func BenchmarkExtractDeep(b *testing.B) {
	for _, c := range []struct{ files, depth int }{{50, 15}, {50, 63}, {500, 15}, {500, 63}} {
		b.Run(fmt.Sprintf("files=%d/components=%d", c.files, c.depth+1), func(b *testing.B) {
			tes := deepManifest(c.files, c.depth)
			a := archive(b, MarshalManifest(manifestFor(tes...)), tes...)
			b.StopTimer() // only the extraction is timed
			for range b.N {
				out := b.TempDir()
				b.StartTimer()
				_, err := unpackArchive(context.Background(), bytes.NewReader(a), int64(len(a)), UnpackOptions{OutputDir: out})
				b.StopTimer()
				must(b, err)
			}
		})
	}
}

// worstCase returns the manifest within the limits that maximises files times depth: a chain of 63
// one-letter folders and, in the deepest, as many empty files of 64 components as fit in
// MaxManifestBytes. With wide set it returns the other extreme, the most empty files at depth 1.
// Names are base-36 counters, skipping those the path rules refuse (con, nul, ...).
func worstCase(wide bool) []Entry {
	var es []Entry
	p := ""
	if !wide {
		for range maxComponents - 1 {
			p += "a"
			es = append(es, Entry{Path: p, Dir: true, Mode: 0o755, MTime: testTime})
			p += "/"
		}
	}
	size := len(MarshalManifest(es))
	empty := len(MarshalManifest(nil))
	for n := int64(0); len(es) < MaxManifestEntries; n++ {
		name := strconv.FormatInt(n, 36)
		if pathRule(name) != "" {
			continue
		}
		e := Entry{Path: p + name, Mode: 0o644, MTime: testTime, SHA256: sha256.Sum256(nil)}
		k := len(MarshalManifest([]Entry{e})) - empty + 1 // with its comma
		if size+k > MaxManifestBytes {
			break
		}
		es, size = append(es, e), size+k
	}
	return es
}

// archiveFile writes the archive of entries (empty files and folders) to a file and returns it.
func archiveFile(tb testing.TB, entries []Entry) (*os.File, int64) {
	f, err := os.Create(filepath.Join(tb.TempDir(), "archive"))
	must(tb, err)
	tb.Cleanup(func() { _ = f.Close() })
	m := MarshalManifest(entries)
	w := bufio.NewWriter(f)
	must(tb, binary.Write(w, binary.BigEndian, uint32(len(m))))
	_, err = w.Write(m)
	must(tb, err)
	tw := tar.NewWriter(w)
	for _, e := range entries {
		h := fileHdr(e.Path, "").hdr
		if e.Dir {
			h = dirHdr(e.Path).hdr
		}
		must(tb, tw.WriteHeader(&h))
	}
	must(tb, errors.Join(tw.Close(), w.Flush()))
	n, err := f.Seek(0, io.SeekCurrent)
	must(tb, err)
	return f, n
}

// BenchmarkExtractWorstCase measures one extraction into a new folder of the worst manifest within
// the limits (worstCase), and of the widest one. SECURITY.md section 5 publishes the figures; the
// nightly workflow's extraction-worst-case job runs it on every CI system. It takes minutes.
func BenchmarkExtractWorstCase(b *testing.B) {
	for _, wide := range []bool{false, true} {
		es := worstCase(wide)
		files, depth := 0, 0
		for _, e := range es {
			if !e.Dir {
				files, depth = files+1, strings.Count(e.Path, "/")+1
			}
		}
		b.Run(fmt.Sprintf("files=%d/components=%d", files, depth), func(b *testing.B) {
			f, n := archiveFile(b, es)
			b.StopTimer()
			for range b.N {
				out := b.TempDir()
				b.StartTimer()
				_, err := unpackArchive(context.Background(), io.NewSectionReader(f, 0, n), n, UnpackOptions{OutputDir: out})
				b.StopTimer()
				must(b, err)
			}
		})
	}
}

func TestCancelDuringWrite(t *testing.T) {
	stream, _ := pack(t, PackOptions{Inputs: tree{"d/": "", "d/big": strings.Repeat("payload ", 1<<20)}.write(t, t.TempDir())})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := t.TempDir()
	_, err := Unpack(ctx, bytes.NewReader(stream), int64(len(stream)), UnpackOptions{OutputDir: out,
		Progress: func(p Progress) {
			if p.Phase == PhaseWrite && p.Done > 0 {
				cancel()
			}
		}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Unpack: %v", err)
	}
	expectEmpty(t, out)
}

func TestCancelDuringCommit(t *testing.T) {
	stream := commitTree(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := t.TempDir()
	withHook(t, func(root *os.Root, p string) error {
		if p == "d/old" { // the second file: d/new is already in place
			cancel()
		}
		return nil
	})
	start := time.Now()
	_, err := Unpack(ctx, bytes.NewReader(stream), int64(len(stream)), UnpackOptions{OutputDir: out})
	if !errors.Is(err, context.Canceled) || time.Since(start) > 5*time.Second {
		t.Fatalf("Unpack: %v after %v", err, time.Since(start))
	}
	expectEmpty(t, out)
}
