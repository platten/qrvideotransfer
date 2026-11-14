//go:build unix

package video

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestWorkerClosesInherited: a descriptor that qrvt inherited without close-on-exec, and so passes on
// to its worker (here fd 9, the write end of a pipe), is closed by the worker as it starts; the
// worker then still serves its job.
func TestWorkerClosesInherited(t *testing.T) {
	t.Parallel()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pr.Close() }()
	cmd := exec.Command(os.Args[0], WorkerArg) //nolint:gosec // the test binary
	cmd.Env = os.Environ()
	cmd.ExtraFiles = []*os.File{nil, nil, nil, nil, nil, nil, pw} // fd 9
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close(); _ = cmd.Wait() }()
	_ = pw.Close()
	eof := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, pr); eof <- err }()
	select {
	case err := <-eof: // every write end is closed: the worker's fd 9 too
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the worker kept the inherited descriptor 9 open")
	}
	// The worker is alive and serving: it answers a job.
	b, _ := json.Marshal(workerJob{Prog: "ffmpeg", Module: "broken", Version: protocolVersion})
	if err := writeMsg(stdin, msgJob, b); err != nil {
		t.Fatal(err)
	}
	for {
		typ, _, err := readMsg(stdout, nil, parentLimit)
		if err != nil {
			t.Fatalf("no exit message: %v", err)
		}
		if typ == msgExit {
			break
		}
	}
}

// TestOpenFDsFallback: without a descriptor directory, every number below the limit is a candidate.
func TestOpenFDsFallback(t *testing.T) {
	t.Parallel()
	if fds := openFDs(filepath.Join(t.TempDir(), "missing")); len(fds) < 3 || fds[0] != 0 || len(fds) > 1<<16 {
		t.Errorf("%d descriptors", len(fds))
	}
}

// TestConcatCancel: cancelling a Concat whose input stalls (a FIFO that delivers nothing) stops it
// promptly, because its input is closed on cancellation, as an encoder's is. Concat runs in qrvt's
// own process with close-on-cancel off, so closing its input is its only stop.
func TestConcatCancel(t *testing.T) {
	t.Parallel()
	b := embedded(t)
	if _, err := loadEngine(t.Context(), defaultCacheDir(), false); err != nil { // compile first
		t.Fatal(err)
	}
	dir := t.TempDir()
	fifo := filepath.Join(dir, "run.h264")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	writer := make(chan *os.File, 1)
	go func() {
		f, _ := os.OpenFile(fifo, os.O_WRONLY, 0)
		writer <- f
	}()
	ctx, cancel := context.WithCancel(t.Context())
	ret := make(chan error, 1)
	go func() { ret <- b.Concat(ctx, []string{fifo}, filepath.Join(dir, "out.mp4"), 12) }()
	w := <-writer // Concat has opened the run
	defer func() { _ = w.Close() }()
	time.Sleep(3 * time.Second) // the engine is now waiting for input
	cancel()
	select {
	case err := <-ret:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("got %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Error("Concat still runs 15 s after its cancellation")
		_ = w.Close()
		t.Log(<-ret)
	}
}
