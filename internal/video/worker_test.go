package video

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// spinWasm is a module whose _start loops forever without calling the host.
var spinWasm = []byte{
	0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00, // magic, version
	0x01, 0x04, 0x01, 0x60, 0x00, 0x00, // type: func () -> ()
	0x03, 0x02, 0x01, 0x00, // function 0 has type 0
	0x07, 0x0a, 0x01, 0x06, '_', 's', 't', 'a', 'r', 't', 0x00, 0x00, // export "_start"
	0x0a, 0x09, 0x01, 0x07, 0x00, 0x03, 0x40, 0x0c, 0x00, 0x0b, 0x0b, // loop { br 0 }
}

// fakeParent is the argument with which the test binary plays a parent that starts a spinning worker
// and prints its process id.
const fakeParent = "__test-fake-parent"

// TestMain makes the test binary act as qrvt does: it serves engine-worker jobs when started as a
// worker (or as a hostile one, or as a fake parent), and otherwise enables workers, so that every
// test of the embedded backend runs its engine in worker processes. Workers write their coverage
// to the test's coverage directory.
func TestMain(m *testing.M) {
	testModules["spin"] = spinWasm
	testModules["broken"] = []byte("not wasm")
	if len(os.Args) > 2 && os.Args[1] == WorkerArg {
		os.Exit(hostileWorker(os.Args[2]))
	}
	if served, code := ServeWorker(os.Args[1:], os.Stdin, os.Stdout); served {
		os.Exit(code)
	}
	if len(os.Args) > 1 && os.Args[1] == fakeParent {
		os.Exit(runFakeParent())
	}
	flag.Parse()
	if f := flag.Lookup("test.gocoverdir"); f != nil && f.Value.String() != "" {
		_ = os.Setenv("GOCOVERDIR", f.Value.String())
		w, _ := currentWorkers()
		w.env = workerEnv()
	}
	os.Exit(m.Run())
}

// hostileWorker misbehaves in the way named. It first says the engine is loaded, as a worker does
// before any guest code runs, except in the modes that test the phase before that ("pre-" modes)
// and those that send the message themselves or never.
func hostileWorker(mode string) int {
	_, _, _ = readMsg(bufio.NewReader(os.Stdin), nil, workerLimit)
	if !strings.HasPrefix(mode, "pre-") && mode != "noready" && mode != "slowload" && mode != "readyhang" {
		_ = writeMsg(os.Stdout, msgReady, nil)
	}
	switch mode {
	case "pre-garbage":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("usage: qrvt\n"), 100))
	case "pre-out": // output before the ready message
		_ = writeMsg(os.Stdout, msgOut, []byte{1})
		_ = writeMsg(os.Stdout, msgExit, exitMsg(0, ""))
	case "pre-exit": // a failure before the ready message
		_ = writeMsg(os.Stdout, msgExit, exitMsg(1, "cannot load the engine"))
	case "oversize": // a message claiming 4 GiB
		_, _ = os.Stdout.Write([]byte{msgOut, 0xff, 0xff, 0xff, 0xff})
	case "garbage":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("garbage\x00\x01"), 1000))
	case "badtype":
		_ = writeMsg(os.Stdout, 'Z', []byte("x"))
	case "progress":
		_ = writeMsg(os.Stdout, msgProgress, []byte{1, 2, 3})
	case "shortexit":
		_ = writeMsg(os.Stdout, msgExit, []byte{1})
	case "planes": // a third of a frame, then success
		_ = writeMsg(os.Stdout, msgOut, make([]byte, 100))
		_ = writeMsg(os.Stdout, msgExit, exitMsg(0, ""))
	case "lies": // a huge read count, an error text with escapes, a failure
		_ = writeMsg(os.Stdout, msgProgress, binary.BigEndian.AppendUint64(nil, 1<<62))
		_ = writeMsg(os.Stdout, msgLog, []byte("\x1b[31mred\x1b[0m"))
		_ = writeMsg(os.Stdout, msgExit, exitMsg(7, "\x1b]0;title\x07bad"))
	case "slowload": // compiling the engine on a slow machine: no progress, then the ready message
		time.Sleep(3 * time.Second)
		_ = writeMsg(os.Stdout, msgReady, nil)
		_ = writeMsg(os.Stdout, msgExit, exitMsg(0, ""))
	case "c1": // a refusal whose text, cut to its bound, starts inside a two-byte character (U+06DB)
		_ = writeMsg(os.Stdout, msgExit, exitMsg(-1, "ۛ"+strings.Repeat("a", 199)))
	case "c1tail": // the same for the log's tail
		_ = writeMsg(os.Stdout, msgExit, exitMsg(7, "ۛ"+strings.Repeat("a", logTailLen-1)))
	case "probelie": // a probe result naming a picture and a frame count far beyond every limit
		_ = writeMsg(os.Stdout, msgOut, []byte(`{"streams":[{"codec_name":"hevc","width":2000000000,"height":2000000000,`+
			`"pix_fmt":"yuv420p","r_frame_rate":"30/1","avg_frame_rate":"30/1","nb_frames":"4000000000","duration":"1e12"}],`+
			`"format":{"format_name":"mov,mp4,m4a,3gp,3g2,mj2","duration":"1e12"}}`))
		_ = writeMsg(os.Stdout, msgExit, exitMsg(0, ""))
	case "sizelie": // a frame line claiming a huge picture, then a frame of the probed size
		_ = writeMsg(os.Stdout, msgLog, []byte("[Parsed_showinfo_0 @ 0x1] config in time_base: 1/10, frame_rate: 10/1\n"+
			"[Parsed_showinfo_0 @ 0x1] n: 0 pts: 0 pts_time:0 pos: 0 fmt:yuv420p s:65535x65535 i:P\n"))
		_ = writeMsg(os.Stdout, msgOut, make([]byte, 16*16*3/2))
		_ = writeMsg(os.Stdout, msgExit, exitMsg(0, ""))
	case "noready": // never says the engine is loaded
		time.Sleep(time.Hour)
	case "readyhang": // says it is ready, then hangs
		_ = writeMsg(os.Stdout, msgReady, nil)
		time.Sleep(time.Hour)
	case "pre-version": // speaks another protocol version
		_ = writeMsg(os.Stdout, msgExit, exitMsg(exitVersion, "protocol version 99"))
	case "lateversion": // claims a version mismatch after it has sent output
		_ = writeMsg(os.Stdout, msgOut, []byte{1})
		_ = writeMsg(os.Stdout, msgExit, exitMsg(exitVersion, "protocol version 99"))
	case "early":
	}
	return 0
}

// runFakeParent starts a spinning worker with no watchdog, prints its process id, and waits to be
// killed. With the argument "nojob" it does not put the worker in a Job Object (Windows), so that
// the worker's exit on its pipe closing is tested alone.
func runFakeParent() int {
	w, err := currentWorkers()
	if w == nil {
		fmt.Println("no workers:", err)
		return 1
	}
	p := *w
	p.noJob = len(os.Args) > 2 && os.Args[2] == "nojob"
	p.started = func(pid int) { fmt.Println(pid) }
	_, _ = p.exec(context.Background(), &job{prog: "ffmpeg", module: "spin", args: func(string, string) []string { return nil },
		stdout: io.Discard, stderr: io.Discard}, &meter{}, "")
	time.Sleep(time.Hour)
	return 0
}

// workerRunner adapts a procRunner to the runner interface.
type workerRunner struct{ p *procRunner }

func (w workerRunner) exec(ctx context.Context, j *job, m *meter) (int, error) {
	return w.p.exec(ctx, j, m, defaultCacheDir())
}

// pids collects the workers a runner starts.
type pids struct {
	mu sync.Mutex
	l  []int
}

func (p *pids) add(pid int) { p.mu.Lock(); p.l = append(p.l, pid); p.mu.Unlock() }

func (p *pids) all() []int { p.mu.Lock(); defer p.mu.Unlock(); return append([]int(nil), p.l...) }

func testWorkers(t *testing.T, args ...string) (*procRunner, *pids) {
	t.Helper()
	w, err := currentWorkers()
	if w == nil {
		t.Fatal(err)
	}
	ps := &pids{}
	p := *w
	p.args = append([]string{WorkerArg}, args...)
	p.started = ps.add
	return &p, ps
}

// gone waits up to d for every process to exit.
func gone(t *testing.T, ps []int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for _, pid := range ps {
		for processAlive(pid) {
			if time.Now().After(deadline) {
				t.Fatalf("worker %d still running after %v", pid, d)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}

// TestWorkerSpin: a module that loops forever without calling the host, in a worker, is killed
// within EngineStallTimeout plus 5 s; the parent gets the typed error and no process is left.
func TestWorkerSpin(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("takes 30 s")
	}
	p, ps := testWorkers(t)
	start := time.Now()
	err := run(t.Context(), workerRunner{p}, job{prog: "ffmpeg", module: "spin", args: func(string, string) []string { return nil }})
	var le *LimitError
	if !errors.As(err, &le) || le.Limit != "EngineStallTimeout" {
		t.Fatalf("got %v", err)
	}
	if el := time.Since(start); el < EngineStallTimeout || el > EngineStallTimeout+5*time.Second {
		t.Errorf("stopped after %v", el)
	}
	gone(t, ps.all(), 5*time.Second)
}

// TestWorkerParentKilled: a worker whose parent dies mid-job exits by itself within 5 s, with its Job
// Object (Windows) and without it, where only its pipe closing ends it.
func TestWorkerParentKilled(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"job", "nojob"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			parentKilled(t, mode)
		})
	}
}

func parentKilled(t *testing.T, mode string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], fakeParent, mode) //nolint:gosec // the test binary
	cmd.Env = os.Environ()
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	var line string
	within(t, time.Minute, "the fake parent's first line", func() { line, err = bufio.NewReader(out).ReadString('\n') })
	pid, perr := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || perr != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("fake parent said %q: %v", line, err)
	}
	time.Sleep(time.Second) // the worker is spinning now
	if !processAlive(pid) {
		t.Fatal("the worker is not running")
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	gone(t, []int{pid}, 5*time.Second)
}

// TestWorkerHostile: replies a worker may forge end the job cleanly, with an error, without large
// allocations, and with what is printed sanitised: valid UTF-8 without control characters, even
// where a bound cuts a character. None is taken for a worker that failed to start. Not parallel, so
// that the allocation count is this test's.
func TestWorkerHostile(t *testing.T) {
	for _, mode := range []string{"oversize", "garbage", "badtype", "progress", "shortexit", "early", "lies",
		"c1", "c1tail", "lateversion"} {
		p, ps := testWorkers(t, mode)
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		var out bytes.Buffer
		err := run(testCtx(t, time.Minute), workerRunner{p}, job{prog: "ffmpeg", args: func(string, string) []string { return nil }, stdout: &out})
		runtime.ReadMemStats(&after)
		if !errors.Is(err, ErrEngine) {
			t.Errorf("%s: %v", mode, err)
			continue
		}
		if msg := err.Error(); !printable(msg) {
			t.Errorf("%s: unsanitised %q", mode, msg)
		}
		if errors.Is(err, errWorkerStart) {
			t.Errorf("%s: taken for a failed start: %v", mode, err)
		}
		if grown := after.TotalAlloc - before.TotalAlloc; grown > 64<<20 {
			t.Errorf("%s: allocated %d MiB", mode, grown>>20)
		}
		gone(t, ps.all(), 5*time.Second)
	}
	// Wrong plane sizes: a third of a frame is a truncated frame.
	p, _ := testWorkers(t, "planes")
	pi := probeInfo{ClipInfo: ClipInfo{Width: 16, Height: 16, BitDepth: 8}, Size: 1000, PixFmt: "yuv420p"}
	d := startDecoder(testCtx(t, time.Minute), workerRunner{p}, clip("hevc8.mp4"), pi, DecodeConfig{}, "yuv420p")
	var f Frame
	if err := d.Next(&f); !errors.Is(err, ErrInvalidVideo) {
		t.Errorf("partial frame: %v", err)
	}
	_ = d.Close()
}

// printable reports whether s is valid UTF-8 without C0 or C1 control characters or DEL.
func printable(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return false
		}
	}
	return true
}

// TestWorkerForgedResults: a worker's lying probe result (a picture of 2e9 x 2e9 pixels and 4e9
// frames) and a lying frame size (65535 x 65535) are refused without large allocations. Not
// parallel, so that the allocation count is this test's.
func TestWorkerForgedResults(t *testing.T) {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	ctx := testCtx(t, time.Minute)
	p, _ := testWorkers(t, "probelie")
	if _, err := probe(ctx, workerRunner{p}, clip("hevc8.mp4")); err == nil {
		t.Error("a huge probed size was accepted")
	}
	p, _ = testWorkers(t, "sizelie")
	pi := probeInfo{ClipInfo: ClipInfo{Width: 16, Height: 16, BitDepth: 8}, Size: 1000, PixFmt: "yuv420p"}
	d := startDecoder(ctx, workerRunner{p}, clip("hevc8.mp4"), pi, DecodeConfig{}, "yuv420p")
	var f Frame
	if err := d.Next(&f); !errors.Is(err, ErrInvalidVideo) {
		t.Errorf("forged frame size: %v", err)
	}
	_ = d.Close()
	runtime.ReadMemStats(&after)
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 64<<20 {
		t.Errorf("allocated %d MiB", grown>>20)
	}
}

// TestWorkerLoadLimit: a worker that never says it has loaded the engine is killed when
// EngineLoadLimit (shortened here) runs out, with the typed error and no process left; the stall
// watchdog, shorter still, does not fire meanwhile. Not parallel: it shortens the limit.
func TestWorkerLoadLimit(t *testing.T) {
	loadLimit = 2 * time.Second
	t.Cleanup(func() { loadLimit = EngineLoadLimit })
	p, ps := testWorkers(t, "noready")
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	start := time.Now()
	err := run(ctx, workerRunner{p}, job{prog: "ffmpeg", args: func(string, string) []string { return nil },
		timeout: time.Second})
	var le *LimitError
	if !errors.As(err, &le) || le.Limit != "EngineLoadLimit" {
		t.Fatalf("got %v", err)
	}
	if el := time.Since(start); el < 2*time.Second || el > 12*time.Second {
		t.Errorf("stopped after %v", el)
	}
	gone(t, ps.all(), 5*time.Second)
}

// TestWorkerVersion: a worker refuses a job of another protocol version, and the parent takes that,
// coming before the ready message, for a failure to start, so the job runs in process and
// WorkerStatus says why. Not parallel: it changes the version the parent sends and the process's
// status.
func TestWorkerVersion(t *testing.T) {
	ctx := testCtx(t, 3*time.Minute)
	var out bytes.Buffer
	in := &bytes.Buffer{}
	b, _ := json.Marshal(workerJob{Prog: "ffmpeg", Version: protocolVersion + 1})
	_ = writeMsg(in, msgJob, b)
	if code := serveWorker(in, &out, func(int) {}); code != 2 {
		t.Errorf("worker exit %d", code)
	}
	typ, msg, err := readMsg(&out, nil, parentLimit)
	if typ != msgExit || err != nil || len(msg) < 4 || int32(binary.BigEndian.Uint32(msg)) != exitVersion {
		t.Errorf("refusal %q %q %v", typ, msg, err)
	}

	p, _ := testWorkers(t, "pre-version")
	if err := run(ctx, workerRunner{p}, job{prog: "ffmpeg", args: func(string, string) []string { return nil }}); !errors.Is(err, errWorkerStart) {
		t.Errorf("parent: %v", err)
	}

	v := protocolVersion
	defer func() { protocolVersion = v; degraded.Store(nil) }()
	protocolVersion++
	d, err := embedded(t).NewDecoder(ctx, clip("hevc8.mp4"), DecodeConfig{BrightnessOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(decodeAll(t, d)); n != 24 {
		t.Errorf("%d frames", n)
	}
	if err := WorkerStatus(); err == nil || !strings.Contains(err.Error(), "protocol version") {
		t.Errorf("status: %v", err)
	}
}

// TestWorkerStartFailures: whatever ends a worker before its ready message (an exit, an exit with a
// failure, garbage, output) is a failure to start, since no guest code has run: nothing reaches the
// job's output, and through the embedded backend the job runs in process and WorkerStatus says why.
// Not parallel: it replaces the process's workers.
func TestWorkerStartFailures(t *testing.T) {
	ctx := testCtx(t, 3*time.Minute)
	for _, mode := range []string{"pre-early", "pre-exit", "pre-garbage", "pre-out"} {
		p, ps := testWorkers(t, mode)
		var out bytes.Buffer
		err := run(ctx, workerRunner{p}, job{prog: "ffmpeg", args: func(string, string) []string { return nil }, stdout: &out})
		if !errors.Is(err, errWorkerStart) || !printable(err.Error()) {
			t.Errorf("%s: %v", mode, err)
		}
		if out.Len() != 0 {
			t.Errorf("%s: %d bytes of output passed on", mode, out.Len())
		}
		gone(t, ps.all(), 5*time.Second)
	}

	w, _ := currentWorkers()
	defer func() { setWorkers(w, nil); degraded.Store(nil) }()
	p, _ := testWorkers(t, "pre-garbage")
	setWorkers(p, nil)
	d, err := embedded(t).NewDecoder(ctx, clip("hevc8.mp4"), DecodeConfig{BrightnessOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(decodeAll(t, d)); n != 24 {
		t.Errorf("%d frames", n)
	}
	if err := WorkerStatus(); err == nil || !strings.Contains(err.Error(), "before it was ready") {
		t.Errorf("status: %v", err)
	}
}

// testCtx is the test's context with a deadline, so that a hang fails the test by name instead of
// running into go test's timeout.
func testCtx(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), d)
	t.Cleanup(cancel)
	return ctx
}

// within runs f and fails the test if it has not returned after d. f must not call t's methods.
func within(t *testing.T, d time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s: still waiting after %v", what, d)
	}
}

// TestWorkerCancel: cancelling a decode and an encode running in workers is prompt and leaves no
// process and no temporary file.
func TestWorkerCancel(t *testing.T) {
	t.Parallel()
	p, ps := testWorkers(t)
	b := &Embedded{ff{r: workerRunner{p}}}
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	d, err := b.NewDecoder(ctx, clip("hevc10hlg.mp4"), DecodeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var f Frame
	if err := d.Next(&f); err != nil {
		t.Fatal(err)
	}
	e, err := b.NewEncoder(ctx, filepath.Join(dir, "a.mp4"), EncodeConfig{Width: 640, Height: 480, FPS: 12})
	if err != nil {
		t.Fatal(err)
	}
	fr, _ := codeFrame(640, 480, 4, 1, 0)
	if err := e.WriteFrame(fr); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	cancel()
	for err == nil {
		err = d.Next(&f)
	}
	werr := error(nil)
	for werr == nil {
		werr = e.WriteFrame(fr)
	}
	_ = d.Close()
	_ = e.Close()
	if el := time.Since(start); el > 5*time.Second {
		t.Errorf("stopped %v after the cancel", el)
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("decoder: %v", err)
	}
	gone(t, ps.all(), 5*time.Second)
	if es, _ := os.ReadDir(dir); len(es) != 0 {
		t.Errorf("left behind: %v", es)
	}
}

// TestWorkerFallback: when no worker can be started the engine runs in process, the decode still
// succeeds, and WorkerStatus says why. Not parallel: it replaces the process's workers.
func TestWorkerFallback(t *testing.T) {
	w, _ := currentWorkers()
	defer func() { setWorkers(w, nil); degraded.Store(nil) }()
	if WorkerStatus() != nil {
		t.Fatalf("status with workers: %v", WorkerStatus())
	}
	setWorkers(&procRunner{exe: filepath.Join(t.TempDir(), "missing")}, nil)
	b := embedded(t)
	d, err := b.NewDecoder(t.Context(), clip("hevc8.mp4"), DecodeConfig{BrightnessOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(decodeAll(t, d)); n != 24 {
		t.Errorf("%d frames", n)
	}
	if err := WorkerStatus(); err == nil || !strings.Contains(err.Error(), "slower") {
		t.Errorf("status: %v", err)
	}
	setWorkers(nil, nil)
	if err := WorkerStatus(); err == nil || !strings.Contains(err.Error(), "hook") {
		t.Errorf("status without the hook: %v", err)
	}
	setWorkers(nil, errors.New("no executable"))
	if err := WorkerStatus(); err == nil || !strings.Contains(err.Error(), "no executable") {
		t.Errorf("status without an executable: %v", err)
	}
}

// TestWorkerJob covers the worker's validation of its job and the protocol's bounds.
func TestWorkerJob(t *testing.T) {
	t.Parallel()
	abs, _ := filepath.Abs(clip("hevc8.mp4"))
	dir := t.TempDir()
	good := workerJob{Prog: "ffmpeg", Input: abs, OutDir: dir, CacheDir: dir}
	if err := good.validate(); err != nil {
		t.Fatal(err)
	}
	for name, j := range map[string]workerJob{
		"program":  {Prog: "sh"},
		"args":     {Prog: "ffmpeg", Args: make([]string, maxArgs+1)},
		"arglen":   {Prog: "ffmpeg", Args: []string{strings.Repeat("a", maxArgLen+1)}},
		"output":   {Prog: "ffmpeg", MaxOutput: -1},
		"cache":    {Prog: "ffmpeg", CacheDir: "relative"},
		"module":   {Prog: "ffmpeg", Module: "nope"},
		"input":    {Prog: "ffmpeg", Input: "relative.mp4"},
		"inputdir": {Prog: "ffmpeg", Input: dir},
		"outdir":   {Prog: "ffmpeg", OutDir: abs},
	} {
		if err := j.validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// The worker refuses a bad job with a message, and a malformed one without.
	var out bytes.Buffer
	in := &bytes.Buffer{}
	_ = writeMsg(in, msgJob, []byte(`{"prog":"sh","version":1}`))
	if code := serveWorker(in, &out, func(int) {}); code != 2 {
		t.Errorf("bad job: exit %d", code)
	}
	typ, b, err := readMsg(&out, nil, parentLimit)
	if typ != msgExit || err != nil || !strings.Contains(string(b), "unknown program") {
		t.Errorf("refusal %q %q %v", typ, b, err)
	}
	for _, in := range [][]byte{nil, {msgData, 0, 0, 0, 0}, append([]byte{msgJob, 0, 0, 0, 1}, '{')} {
		if code := serveWorker(bytes.NewReader(in), io.Discard, func(int) {}); code != 2 {
			t.Errorf("%q: exit %d", in, code)
		}
	}
	if env := workerEnv(); len(env) > 4 {
		t.Errorf("worker environment %v", env)
	}
	c := &chunked{w: &msgWriter{w: bufio.NewWriter(io.Discard)}, typ: msgOut, size: 4, limit: 10}
	if n, err := c.Write([]byte("0123456789")); n != 10 || err != nil {
		t.Errorf("chunked %d %v", n, err)
	}
	if _, err := c.Write([]byte("x")); !errors.Is(err, errOutputLimit) {
		t.Errorf("over the limit: %v", err)
	}
}

// TestWorkerSlowLoad: the time a worker spends loading and compiling the engine, before it says it
// is ready, is not a stall (a first run on a slow machine compiles for longer than the watchdog's
// limit).
func TestWorkerSlowLoad(t *testing.T) {
	t.Parallel()
	p, _ := testWorkers(t, "slowload")
	err := run(t.Context(), workerRunner{p}, job{prog: "ffmpeg", args: func(string, string) []string { return nil },
		timeout: time.Second})
	if err != nil {
		t.Fatalf("a slow engine load was taken for a stall: %v", err)
	}
}
