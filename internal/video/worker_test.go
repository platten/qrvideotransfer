package video

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
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

// hostileWorker misbehaves in the way named.
func hostileWorker(mode string) int {
	_, _, _ = readMsg(bufio.NewReader(os.Stdin), nil, workerLimit)
	switch mode {
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
	case "early":
	}
	return 0
}

// runFakeParent starts a spinning worker with no watchdog, prints its process id, and waits to be
// killed.
func runFakeParent() int {
	w, err := currentWorkers()
	if w == nil {
		fmt.Println("no workers:", err)
		return 1
	}
	p := *w
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

// TestWorkerParentKilled: a worker whose parent dies mid-job exits by itself within 5 s.
func TestWorkerParentKilled(t *testing.T) {
	t.Parallel()
	cmd := exec.Command(os.Args[0], fakeParent) //nolint:gosec // the test binary
	cmd.Env = os.Environ()
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
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
// allocations, and with what is printed sanitised. Not parallel, so that the allocation count is
// this test's.
func TestWorkerHostile(t *testing.T) {
	for _, mode := range []string{"oversize", "garbage", "badtype", "progress", "shortexit", "early", "lies"} {
		p, ps := testWorkers(t, mode)
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		var out bytes.Buffer
		err := run(t.Context(), workerRunner{p}, job{prog: "ffmpeg", args: func(string, string) []string { return nil }, stdout: &out})
		runtime.ReadMemStats(&after)
		if !errors.Is(err, ErrEngine) {
			t.Errorf("%s: %v", mode, err)
			continue
		}
		if strings.ContainsAny(err.Error(), "\x1b\x07\x00") {
			t.Errorf("%s: unsanitised %q", mode, err)
		}
		if grown := after.TotalAlloc - before.TotalAlloc; grown > 64<<20 {
			t.Errorf("%s: allocated %d MiB", mode, grown>>20)
		}
		gone(t, ps.all(), 5*time.Second)
	}
	// Wrong plane sizes: a third of a frame is a truncated frame.
	p, _ := testWorkers(t, "planes")
	pi := probeInfo{ClipInfo: ClipInfo{Width: 16, Height: 16, BitDepth: 8}, Size: 1000, PixFmt: "yuv420p"}
	d := startDecoder(t.Context(), workerRunner{p}, clip("hevc8.mp4"), pi, DecodeConfig{}, "yuv420p")
	var f Frame
	if err := d.Next(&f); !errors.Is(err, ErrInvalidVideo) {
		t.Errorf("partial frame: %v", err)
	}
	_ = d.Close()
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
	_ = writeMsg(in, msgJob, []byte(`{"prog":"sh"}`))
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
