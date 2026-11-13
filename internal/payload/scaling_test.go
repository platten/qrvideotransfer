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

// reportTimingsOnCI makes TestExtractionScaling fail on GitHub Actions with its measurements, so
// that the figures of each runner appear in the job log (passing tests print nothing there).
const reportTimingsOnCI = true

// TestExtractionScaling extracts the same files beneath folder chains of depth 500 and 2000, each
// twice into the same folder (the second time every folder exists and every file is replaced). Work
// that grows linearly with the depth takes about 4 times as long at 2000; quadratic work takes 16
// times as long. More than 8 fails.
func TestExtractionScaling(t *testing.T) {
	if testing.Short() {
		t.Skip("extracts deep trees; skipped with -short")
	}
	if raceEnabled {
		t.Skip("timing ratios do not hold under the race detector; it runs in the ordinary test task")
	}
	const files = 50
	measure := func(depth int) time.Duration {
		tes := deepManifest(files, depth)
		a := archive(t, MarshalManifest(manifestFor(tes...)), tes...)
		out := t.TempDir()
		start := time.Now()
		for _, overwrite := range []bool{false, true} {
			_, err := unpackArchive(context.Background(), bytes.NewReader(a), int64(len(a)), UnpackOptions{OutputDir: out, Overwrite: overwrite})
			must(t, err)
		}
		return time.Since(start)
	}
	short := min(measure(500), measure(500))
	long := measure(2000)
	ratio := float64(long) / float64(short)
	// Each file's path is resolved a few times; the folders cost the same at any depth. The worst
	// manifest within the limits has about 14,500 files at depth 2000 (TestDeepManifestResources).
	perComponent := long / time.Duration(2*files*2000)
	worst := perComponent * 14500 * 2000
	msg := fmt.Sprintf("depth 500: %v, depth 2000: %v, ratio %.1f; %v per file per path component; "+
		"estimated full extraction of the worst manifest within the limits: %v",
		short, long, ratio, perComponent, worst.Round(time.Second))
	t.Log(msg)
	if ratio > 8 {
		t.Fatalf("extraction grows faster than linearly with depth: %s", msg)
	}
	if reportTimingsOnCI && os.Getenv("GITHUB_ACTIONS") == "true" {
		t.Errorf("timings (reported on purpose): %s", msg)
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
