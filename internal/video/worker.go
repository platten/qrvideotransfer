package video

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// WorkerArg is the first argument with which qrvt re-executes itself as an engine worker: a child
// process that runs one engine instance, with close-on-cancel off, and that the parent's watchdog
// kills. [ServeWorker] recognises it, so that help, completion and the man page never see it.
const WorkerArg = "__qrvt-engine-worker"

// Message types of the worker protocol. A message is a type byte, a big-endian uint32 length and
// that many bytes. Both sides check the length against the type's bound before allocating.
const (
	msgJob      = 'J' // parent to worker: the job, as JSON
	msgData     = 'D' // parent to worker: the program's standard input
	msgEnd      = 'E' // parent to worker: end of the program's standard input
	msgOut      = 'O' // worker to parent: the program's standard output
	msgLog      = 'L' // worker to parent: the program's standard error
	msgProgress = 'P' // worker to parent: bytes read from the input file so far (uint64)
	msgExit     = 'X' // worker to parent: exit code (int32), then error text
	msgReady    = 'R' // worker to parent: the engine is loaded and the program starts
)

const (
	maxJobMsg  = 64 << 10
	maxChunk   = 1 << 20
	maxLogMsg  = 64 << 10
	maxExitMsg = 4 + 4<<10
	maxArgs    = 256
	maxArgLen  = 4096
)

// parentLimit bounds the messages a worker may send.
func parentLimit(t byte) int {
	switch t {
	case msgOut:
		return maxChunk
	case msgLog:
		return maxLogMsg
	case msgProgress:
		return 8
	case msgExit:
		return maxExitMsg
	case msgReady:
		return 0
	}
	return -1
}

// workerLimit bounds the messages the parent may send.
func workerLimit(t byte) int {
	switch t {
	case msgJob:
		return maxJobMsg
	case msgData:
		return maxChunk
	case msgEnd:
		return 0
	}
	return -1
}

// workerJob is what the parent sends: everything the program needs, and no recording content but
// the input file's path.
type workerJob struct {
	Prog     string   `json:"prog"`
	Args     []string `json:"args"`
	Input    string   `json:"input,omitempty"`
	OutDir   string   `json:"out_dir,omitempty"`
	CacheDir string   `json:"cache_dir,omitempty"`
	Reads    bool     `json:"reads,omitempty"` // report input reads (probing)
	// MaxOutput bounds the program's standard output in bytes (0: none). For a decode it is the
	// output MaxDecodedPixelsPerInputByte allows, so the worker enforces that limit too.
	MaxOutput int64 `json:"max_output,omitempty"`
	// Module names a test module to run instead of the engine; only test binaries register any.
	Module string `json:"module,omitempty"`
}

// testModules holds the Wasm modules a test binary lets a worker run instead of the engine.
var testModules = map[string][]byte{}

// validate checks a job as untrusted input.
func (j *workerJob) validate() error {
	switch {
	case j.Prog != "ffmpeg" && j.Prog != "ffprobe":
		return errors.New("unknown program")
	case len(j.Args) > maxArgs:
		return errors.New("too many arguments")
	case j.MaxOutput < 0:
		return errors.New("negative output bound")
	case j.CacheDir != "" && !filepath.IsAbs(j.CacheDir):
		return errors.New("cache directory is not absolute")
	}
	for _, a := range j.Args {
		if len(a) > maxArgLen {
			return errors.New("argument too long")
		}
	}
	if _, ok := testModules[j.Module]; j.Module != "" && !ok {
		return errors.New("unknown module")
	}
	if j.Input != "" {
		if st, err := os.Stat(j.Input); !filepath.IsAbs(j.Input) || err != nil || !st.Mode().IsRegular() {
			return errors.New("input is not an absolute path to a regular file")
		}
	}
	if j.OutDir != "" {
		if st, err := os.Stat(j.OutDir); !filepath.IsAbs(j.OutDir) || err != nil || !st.IsDir() {
			return errors.New("output is not an absolute path to a directory")
		}
	}
	return nil
}

func writeMsg(w io.Writer, typ byte, b []byte) error {
	var h [5]byte
	h[0] = typ
	binary.BigEndian.PutUint32(h[1:], uint32(len(b))) //nolint:gosec // every caller bounds len(b)
	if _, err := w.Write(h[:]); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

// readMsg reads one message into buf (grown as needed). A type limit refuses returns an error
// before anything is allocated.
func readMsg(r io.Reader, buf []byte, limit func(byte) int) (byte, []byte, error) {
	var h [5]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, buf, err
	}
	n, max := binary.BigEndian.Uint32(h[1:]), limit(h[0])
	if max < 0 || int64(n) > int64(max) {
		return 0, buf, fmt.Errorf("%w: worker protocol: message %q of %d bytes", ErrEngine, sanitise(string(h[:1]), 1), n)
	}
	if cap(buf) < int(n) {
		buf = make([]byte, n)
	}
	buf = buf[:n]
	_, err := io.ReadFull(r, buf)
	if errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	return h[0], buf, err
}

// msgWriter sends messages from several goroutines, one at a time.
type msgWriter struct {
	mu sync.Mutex
	w  *bufio.Writer
}

func (m *msgWriter) send(typ byte, b []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := writeMsg(m.w, typ, b); err != nil {
		return err
	}
	return m.w.Flush()
}

// chunked is a writer that sends what it is given as messages of one type, split to the type's
// bound. When limit is positive, writing more than limit bytes in all fails.
type chunked struct {
	w     *msgWriter
	typ   byte
	size  int
	limit int64
	n     int64
}

var errOutputLimit = errors.New("output limit")

func (c *chunked) Write(b []byte) (int, error) {
	if c.limit > 0 && c.n+int64(len(b)) > c.limit {
		return 0, errOutputLimit
	}
	for k := 0; k < len(b); k += c.size {
		if err := c.w.send(c.typ, b[k:min(k+c.size, len(b))]); err != nil {
			return k, err
		}
	}
	c.n += int64(len(b))
	return len(b), nil
}

// ServeWorker is the entry point of the hook a host calls first thing in main (the root package's
// EngineWorker). Given the process's arguments without the program name, it runs the job and
// reports served when the process was started as an engine worker, after which the caller exits
// with code. Otherwise it enables engine workers for this process, re-executing its own executable,
// and returns at once.
func ServeWorker(args []string, stdin io.Reader, stdout io.Writer) (served bool, code int) {
	if len(args) == 0 || args[0] != WorkerArg {
		exe, err := os.Executable()
		if err != nil {
			setWorkers(nil, fmt.Errorf("cannot find this program to start engine workers: %w", err))
		} else {
			setWorkers(&procRunner{exe: exe, args: []string{WorkerArg}, env: workerEnv()}, nil)
		}
		return false, 0
	}
	return true, serveWorker(stdin, stdout, os.Exit)
}

// serveWorker runs one job read from in and writes its results to out. When in closes or breaks
// before the job is over, the parent is gone and it calls exit at once, whatever the engine is doing.
func serveWorker(in io.Reader, out io.Writer, exit func(int)) int {
	// The engine occupies one P for as long as it runs; the goroutine that watches the parent needs
	// another.
	runtime.GOMAXPROCS(max(2, runtime.GOMAXPROCS(0)))
	r := bufio.NewReaderSize(in, 64<<10)
	typ, b, err := readMsg(r, nil, workerLimit)
	if err != nil || typ != msgJob {
		return 2
	}
	w := &msgWriter{w: bufio.NewWriterSize(out, 64<<10)}
	var j workerJob
	if err := json.Unmarshal(b, &j); err != nil {
		_ = w.send(msgExit, exitMsg(-1, "invalid job"))
		return 2
	}
	if err := j.validate(); err != nil {
		_ = w.send(msgExit, exitMsg(-1, "invalid job: "+err.Error()))
		return 2
	}
	stdin, feed := io.Pipe()
	go func() {
		var buf []byte
		ended := false
		for {
			var typ byte
			var err error
			typ, buf, err = readMsg(r, buf, workerLimit)
			if err != nil || typ == msgJob || (ended && typ != 0) {
				exit(3) // the parent is gone, or broke the protocol
				return
			}
			if typ == msgEnd {
				ended = true
				_ = feed.Close()
				continue
			}
			_, _ = feed.Write(buf)
		}
	}()
	rc, err := runJob(&j, stdin, w)
	text := ""
	if err != nil {
		text = err.Error()
	}
	if w.send(msgExit, exitMsg(rc, text)) != nil {
		return 3
	}
	return 0
}

func exitMsg(rc int, text string) []byte {
	b := binary.BigEndian.AppendUint32(nil, uint32(int32(rc))) //nolint:gosec // exit codes fit
	if len(text) > maxExitMsg-4 {
		text = text[:maxExitMsg-4]
	}
	return append(b, text...)
}

// progressTick is how often a worker reports input reads while probing.
var progressTick = 200 * time.Millisecond

// runJob runs the job's program in this process's unguarded engine (the parent kills the process
// to stop it), reporting progress while it runs.
func runJob(j *workerJob, stdin io.Reader, w *msgWriter) (int, error) {
	ctx := context.Background()
	var e *engine
	var err error
	if j.Module != "" {
		e, err = newEngineFrom(ctx, "", false, testModules[j.Module])
	} else {
		e, err = loadEngine(ctx, j.CacheDir, false)
	}
	if err != nil {
		return 0, err
	}
	if err := w.send(msgReady, nil); err != nil {
		return 0, err
	}
	m := &meter{}
	done := make(chan struct{})
	defer close(done)
	if j.Reads {
		go func() {
			last := int64(-1)
			t := time.NewTicker(progressTick)
			defer t.Stop()
			for {
				select {
				case <-done:
					return
				case <-t.C:
					if v := m.n.Load(); v != last {
						last = v
						_ = w.send(msgProgress, binary.BigEndian.AppendUint64(nil, uint64(v))) //nolint:gosec // non-negative
					}
				}
			}
		}()
	}
	args := j.Args
	wj := &job{prog: j.Prog, args: func(string, string) []string { return args }, input: j.Input, outDir: j.OutDir,
		stdin: stdin, stdout: &chunked{w: w, typ: msgOut, size: maxChunk, limit: j.MaxOutput},
		stderr: &chunked{w: w, typ: msgLog, size: maxLogMsg}}
	if j.Reads {
		wj.progress = progReads
	}
	return e.exec(ctx, wj, m)
}

// Engine workers for this process: the runner, or why there is none.
var workers struct {
	sync.Mutex
	r      *procRunner
	reason error
}

func setWorkers(r *procRunner, reason error) {
	workers.Lock()
	workers.r, workers.reason = r, reason
	workers.Unlock()
}

func currentWorkers() (*procRunner, error) {
	workers.Lock()
	defer workers.Unlock()
	if workers.r == nil && workers.reason == nil {
		return nil, errors.New("the host did not call the engine-worker hook first thing in main")
	}
	return workers.r, workers.reason
}

// degraded records the last reason the engine ran in process.
var degraded atomic.Pointer[error]

// WorkerStatus is nil while the embedded engine runs in worker processes. Otherwise it says why it
// runs in this process instead, with close-on-cancel on, which decodes about 4 to 11 times slower
// (docs/spikes/S1.md section 10); a caller shows it as a warning.
func WorkerStatus() error {
	if r, err := currentWorkers(); r == nil {
		return fmt.Errorf("video engine runs in process, about 4 to 11 times slower: %w", err)
	}
	if p := degraded.Load(); p != nil {
		return fmt.Errorf("video engine runs in process, about 4 to 11 times slower: %w", *p)
	}
	return nil
}
