package payload

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// extractDeep extracts files beneath a folder chain of the given depth into a new folder, and then
// again once for each overwrite given (with every folder existing and every file replaced).
func extractDeep(tb testing.TB, files, depth int, overwrites ...bool) {
	tes := deepManifest(files, depth)
	a := archive(tb, MarshalManifest(manifestFor(tes...)), tes...)
	out := tb.TempDir()
	for _, overwrite := range append([]bool{false}, overwrites...) {
		_, err := unpackArchive(context.Background(), bytes.NewReader(a), int64(len(a)), UnpackOptions{OutputDir: out, Overwrite: overwrite})
		must(tb, err)
	}
}

// TestExtractionWorkIsLinear counts what extraction asks of the operating system: the number of
// calls that take a path, and the total number of path components in them. Each folder must cost a
// fixed number of calls of one component (they go through the parent's handle), and each file a
// fixed number of calls of at most its depth. Work quadratic in the depth, such as resolving every
// folder from the root, fails the bound at depth 1000 by more than a factor of 10. The counts do not
// depend on the speed of the machine.
func TestExtractionWorkIsLinear(t *testing.T) {
	t.Cleanup(func() { testHookOS = nil })
	for _, c := range []struct{ files, depth int }{{5, 1000}, {20, 1000}, {20, 250}, {200, 10}} {
		var calls, components int
		testHookOS = func(p string) { calls, components = calls+1, components+strings.Count(p, "/")+1 }
		extractDeep(t, c.files, c.depth, true)
		testHookOS = nil
		folders, units := c.depth, c.files+c.depth
		t.Logf("%d files at depth %d: %d calls, %d path components", c.files, c.depth, calls, components)
		// Two extractions; per folder at most 10 calls each, per file at most 12.
		if calls > 2*12*units {
			t.Errorf("%d files at depth %d: %d calls for %d folders and files", c.files, c.depth, calls, units)
		}
		if limit := 2 * (10*folders + 12*c.files*(c.depth+2)); components > limit {
			t.Errorf("%d files at depth %d: %d path components, more than %d", c.files, c.depth, components, limit)
		}
	}
}

// BenchmarkExtractDeep measures extraction beneath deep folder chains, for fitting
// time = a * folders + b * files * depth for one extraction into a new folder (SECURITY.md section 5).
func BenchmarkExtractDeep(b *testing.B) {
	for _, c := range []struct{ files, depth int }{{50, 500}, {50, 2000}, {500, 2000}, {14500, 2000}} {
		b.Run(fmt.Sprintf("files=%d/depth=%d", c.files, c.depth), func(b *testing.B) {
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

func TestCancelDuringWrite(t *testing.T) {
	stream, _ := pack(t, PackOptions{Inputs: tree{"d/": "", "d/big": strings.Repeat("payload ", 1<<20)}.write(t, t.TempDir())})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := t.TempDir()
	_, err := Unpack(ctx, bytes.NewReader(stream), int64(len(stream)), UnpackOptions{OutputDir: out,
		Progress: func(done, total int64) {
			if done > 0 {
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
