package video

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

// TestWorkerInProcess runs the worker's side in this process: a probe that reports its reads, a job
// whose module does not compile, and a parent that stops listening. Not parallel: it shortens the
// progress tick.
func TestWorkerInProcess(t *testing.T) {
	progressTick = time.Millisecond
	defer func() { progressTick = 200 * time.Millisecond }()
	abs, _ := filepath.Abs(clip("hevc10hlg.mp4"))
	job := func(j workerJob) io.Reader {
		j.Version = protocolVersion
		b, _ := json.Marshal(j)
		var in bytes.Buffer
		_ = writeMsg(&in, msgJob, b)
		_ = writeMsg(&in, msgEnd, nil)
		return io.MultiReader(&in, blockReader{}) // the parent stays
	}
	var out bytes.Buffer
	probeJob := workerJob{Prog: "ffprobe", Input: abs, Reads: true, CacheDir: defaultCacheDir(),
		Args: append([]string{"-count_frames"}, probeArgs("/in/"+inputName(abs))...)}
	if code := serveWorker(job(probeJob), &out, func(int) {}); code != 0 {
		t.Fatalf("probe exit %d", code)
	}
	seen := map[byte]bool{}
	for {
		typ, b, err := readMsg(&out, nil, parentLimit)
		if err != nil {
			break
		}
		seen[typ] = true
		if typ == msgExit && !bytes.Equal(b, exitMsg(0, "")) {
			t.Errorf("exit message %q", b)
		}
	}
	if !seen[msgOut] || !seen[msgProgress] || !seen[msgExit] {
		t.Errorf("messages seen: %v", seen)
	}

	out.Reset()
	if code := serveWorker(job(workerJob{Prog: "ffmpeg", Module: "broken"}), &out, func(int) {}); code != 0 {
		t.Fatalf("broken module exit %d", code)
	}
	typ, b, _ := readMsg(&out, nil, parentLimit)
	if typ != msgExit || len(b) < 5 {
		t.Errorf("broken module: %q %q", typ, b)
	}
	// A parent that has gone: the exit message cannot be sent.
	if code := serveWorker(job(workerJob{Prog: "ffmpeg", Module: "broken"}), failWriter{}, func(int) {}); code != 3 {
		t.Errorf("parent gone: exit %d", code)
	}
	// A parent that breaks the protocol after the job: the worker exits.
	var in bytes.Buffer
	b, _ = json.Marshal(workerJob{Prog: "ffmpeg", Module: "broken", Version: protocolVersion})
	_ = writeMsg(&in, msgJob, b)
	_ = writeMsg(&in, msgJob, b)
	exited := make(chan int, 1)
	go serveWorker(io.MultiReader(&in, blockReader{}), io.Discard, func(c int) { exited <- c })
	if c := <-exited; c != 3 {
		t.Errorf("protocol break: exit %d", c)
	}
}

// blockReader never returns.
type blockReader struct{}

func (blockReader) Read([]byte) (int, error) { select {} }

// TestWorkerProtocol covers the message helpers' edges.
func TestWorkerProtocol(t *testing.T) {
	t.Parallel()
	if err := writeMsg(failWriter{}, msgOut, nil); err == nil {
		t.Error("write to a failing writer")
	}
	if _, _, err := readMsg(bytes.NewReader([]byte{msgOut, 0, 0, 0, 9, 1}), nil, parentLimit); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("short body: %v", err)
	}
	if _, _, err := readMsg(bytes.NewReader([]byte{'Q', 0, 0, 0, 0}), nil, workerLimit); !errors.Is(err, ErrEngine) {
		t.Errorf("unknown type: %v", err)
	}
	if _, _, err := readMsg(bytes.NewReader([]byte{0x9b, 0, 0, 0, 0}), nil, workerLimit); !printable(err.Error()) {
		t.Errorf("unknown type 0x9b: %q", err)
	}
	// sanitise cuts on a character boundary, so no stray byte of a character is left.
	for _, s := range []string{"aۛaa", "ۛ" + strings.Repeat("a", 9), "\x9b\x9b\x9b\x9b"} {
		if got := sanitise(s, 3); !printable(got) || len(got) > 3 {
			t.Errorf("sanitise(%q, 3) = %q", s, got)
		}
	}
	w := &msgWriter{w: bufio.NewWriterSize(failWriter{}, 16)}
	if err := w.send(msgOut, make([]byte, 64)); err == nil {
		t.Error("send to a failing writer")
	}
	c := &chunked{w: w, typ: msgOut, size: 8}
	if _, err := c.Write(make([]byte, 32)); err == nil {
		t.Error("chunked write to a failing writer")
	}
	if m := exitMsg(1, strings.Repeat("x", 2*maxExitMsg)); len(m) != maxExitMsg {
		t.Errorf("exit message of %d bytes", len(m))
	}
	// A job too large to send is refused before any process starts.
	p, ps := testWorkers(t)
	err := run(t.Context(), workerRunner{p}, job{prog: "ffmpeg", args: func(string, string) []string {
		return make([]string, maxJobMsg)
	}})
	if !errors.Is(err, ErrEngine) || len(ps.all()) != 0 {
		t.Errorf("huge job: %v, %d workers", err, len(ps.all()))
	}
	// A consumer that stops reading ends the job.
	p, _ = testWorkers(t, "planes")
	if err := run(t.Context(), workerRunner{p}, job{prog: "ffmpeg", args: func(string, string) []string { return nil },
		stdout: failWriter{}}); err == nil {
		t.Error("failing consumer")
	}
	// A worker that exits before taking its input.
	p, _ = testWorkers(t, "early")
	if err := run(t.Context(), workerRunner{p}, job{prog: "ffmpeg", args: func(string, string) []string { return nil },
		stdin: bytes.NewReader(make([]byte, 4*maxChunk)), progress: progInput}); !errors.Is(err, ErrEngine) {
		t.Errorf("early exit with input: %v", err)
	}
}
