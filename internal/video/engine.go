package video

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	_ "embed" // the engine module
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"

	"codeberg.org/gruf/go-ffmpreg/wasm"
	"github.com/tetratelabs/wazero"
)

// The engine is ffmpeg n5.1.10 with x264, compiled to WebAssembly by go-ffmpreg v0.6.22. This is a
// byte-for-byte copy of that module's embed/ffmpreg.wasm.gz: qrvt loads it itself, on first use,
// instead of importing go-ffmpreg/embed, whose init decompresses it at every start (DESIGN.md
// section 12, "Loading"). A test checks the copy against the module.
//
//go:embed ffmpreg.wasm.gz
var engineGz string

// EngineSHA256 is the SHA-256 of the decompressed engine module, checked before it is compiled.
const EngineSHA256 = "357dd97d37f5fa01b45865bc5d6e90a9538e8bff236de7f1c143a3bc863bc766"

// engine is one compiled module in one wazero runtime; any number of instances run from it.
type engine struct {
	rt wazero.Runtime
	cm wazero.CompiledModule
	// guarded engines run with close-on-cancel, in qrvt's own process, when no worker process can be
	// started. Unguarded ones run in a worker process, which the parent kills, or in qrvt's process
	// for trusted jobs (encoding qrvt's own frames). In a process, at most EngineInstances() run at
	// once, whichever kind.
	guarded bool
}

type engineKey struct {
	cacheDir string
	guarded  bool
}

var engines struct {
	sync.Mutex
	m map[engineKey]*engine
}

// loadEngine returns the process's engine for a compilation cache directory ("" for none), compiling
// it on first use. guarded selects close-on-cancel; only a worker process runs unguarded.
func loadEngine(ctx context.Context, cacheDir string, guarded bool) (*engine, error) {
	engines.Lock()
	defer engines.Unlock()
	k := engineKey{cacheDir, guarded}
	if e := engines.m[k]; e != nil {
		return e, nil
	}
	e, err := newEngine(ctx, cacheDir, guarded)
	if err != nil {
		return nil, err
	}
	if engines.m == nil {
		engines.m = map[engineKey]*engine{}
	}
	engines.m[k] = e
	return e, nil
}

// engineWasm decompresses the module and checks its hash.
func engineWasm() ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader([]byte(engineGz)))
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
	}
	if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != EngineSHA256 {
		return nil, fmt.Errorf("video: engine module hash %x, want %s", sum, EngineSHA256)
	}
	return b, nil
}

// newEngine creates a runtime as DESIGN.md section 12 requires: close on context done (so the
// watchdog and cancellation interrupt running code), 512 MiB of linear memory per instance reserved
// up front, and a compilation cache. closeOnDone is false only to measure its cost.
func newEngine(ctx context.Context, cacheDir string, closeOnDone bool) (*engine, error) {
	b, err := engineWasm()
	if err != nil {
		return nil, err
	}
	return newEngineFrom(ctx, cacheDir, closeOnDone, b)
}

// newEngineFrom compiles module b in a new runtime. When the compilation cache cannot be written
// (read-only, or on Windows a file another process is renaming into place at the same moment), it
// compiles again without the cache.
func newEngineFrom(ctx context.Context, cacheDir string, closeOnDone bool, mod []byte) (*engine, error) {
	cfg := engineConfig(closeOnDone)
	if cacheDir != "" {
		if c, err := wazero.NewCompilationCacheWithDir(cacheDir); err == nil {
			cfg = cfg.WithCompilationCache(c)
		}
	}
	rt, err := wasm.NewRuntime(ctx, cfg)
	if err != nil {
		return nil, err
	}
	cm, err := rt.CompileModule(ctx, mod)
	if err != nil {
		_ = rt.Close(ctx)
		if cacheDir != "" {
			return newEngineFrom(ctx, "", closeOnDone, mod)
		}
		return nil, err
	}
	return &engine{rt: rt, cm: cm, guarded: closeOnDone}, nil
}

// engineConfig is the runtime configuration: 512 MiB of linear memory per instance, reserved up
// front, and close-on-cancel when guarded.
func engineConfig(closeOnDone bool) wazero.RuntimeConfig {
	return wazero.NewRuntimeConfig().
		WithCloseOnContextDone(closeOnDone).
		WithMemoryLimitPages(EngineMemoryLimit / 65536).
		WithMemoryCapacityFromMax(true)
}

// defaultCacheDir is where compiled code is kept: qrvt/wazero under the user's cache directory, or
// nowhere when there is none.
func defaultCacheDir() string {
	d, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "qrvt", "wazero")
}

// slots holds one token per engine instance allowed to run: at most GOMAXPROCS - 1 at once. With
// WithCloseOnContextDone(true) every loop back-edge of the compiled code returns to Go for its check,
// where the scheduler can preempt it, so the watchdog stops every instance even without the cap,
// but late: uncapped, stops were up to 8 times late on a CI runner (macos-26-intel, 16.8 s against a
// 2 s limit). The cap is a real defence.
var slots = make(chan struct{}, max(1, runtime.GOMAXPROCS(0)-1))

// EngineInstances is how many engine instances may run at once: one fewer than GOMAXPROCS at
// start-up (at least one). Further instances wait for a turn; the time they wait is not a stall and
// has no limit but the context. A caller that keeps this many decoders open without reading them
// holds every slot, and its next probe or decoder waits until its context ends.
func EngineInstances() int { return cap(slots) }

// exec implements runner. The instance sees the input file read-only through inputFS at /in, the
// output directory at /out, and nothing else; it has no network.
func (e *engine) exec(ctx context.Context, j *job, m *meter) (int, error) {
	m.waiting.Add(1)
	select {
	case slots <- struct{}{}:
		m.waiting.Add(-1)
	case <-ctx.Done():
		m.waiting.Add(-1)
		return 0, ctx.Err()
	}
	defer func() { <-slots }()
	var in string
	reads := &atomic.Int64{}
	if j.progress == progReads {
		reads = &m.reads
	}
	fsc := wazero.NewFSConfig()
	if j.input != "" {
		name := inputName(j.input)
		in = "/in/" + name
		fsc = fsc.WithFSMount(inputFS{path: j.input, name: name, reads: reads}, "/in")
	}
	if j.outDir != "" {
		fsc = fsc.WithDirMount(j.outDir, "/out")
	}
	rc, err := wasm.Run(ctx, e.rt, e.cm, wasm.Args{
		Name: j.prog, Args: j.args(in, "/out"), Stdin: j.stdin, Stdout: j.stdout, Stderr: j.stderr,
		Config: func(c wazero.ModuleConfig) wazero.ModuleConfig {
			return c.WithSysWalltime().WithSysNanotime().WithFSConfig(fsc)
		},
	})
	return int(rc), err
}
