package video

import (
	"context"
	_ "embed"
	"runtime"
	"sync"

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
	panic("not implemented")
}

// engineWasm decompresses the module and checks its hash.
func engineWasm() ([]byte, error) { panic("not implemented") }

// newEngine creates a runtime as DESIGN.md section 12 requires: close on context done (so the
// watchdog and cancellation interrupt running code), 512 MiB of linear memory per instance reserved
// up front, and a compilation cache. closeOnDone is false only to measure its cost.
func newEngine(ctx context.Context, cacheDir string, closeOnDone bool) (*engine, error) {
	panic("not implemented")
}

// newEngineFrom compiles module b in a new runtime. When the compilation cache cannot be written
// (read-only, or on Windows a file another process is renaming into place at the same moment), it
// compiles again without the cache.
func newEngineFrom(ctx context.Context, cacheDir string, closeOnDone bool, mod []byte) (*engine, error) {
	panic("not implemented")
}

// engineConfig is the runtime configuration: 512 MiB of linear memory per instance, reserved up
// front, and close-on-cancel when guarded.
func engineConfig(closeOnDone bool) wazero.RuntimeConfig { panic("not implemented") }

// defaultCacheDir is where compiled code is kept: qrvt/wazero under the user's cache directory, or
// nowhere when there is none.
func defaultCacheDir() string { panic("not implemented") }

// slots holds one token per engine instance allowed to run. Compiled Wasm cannot be preempted by
// the Go scheduler, so instances that spin without calling the host would starve every goroutine,
// the watchdog's included, if they occupied every P; at most GOMAXPROCS - 1 run at once.
var slots = make(chan struct{}, max(1, runtime.GOMAXPROCS(0)-1))

// EngineInstances is how many engine instances may run at once: one fewer than GOMAXPROCS at
// start-up (at least one), so that a P stays free for the watchdog and for qrvt itself. Further
// instances wait for a turn; the time they wait is not a stall. A caller that keeps more decoders open
// than this waits on them.
func EngineInstances() int { panic("not implemented") }

// exec implements runner. The instance sees the input file read-only through inputFS at /in, the
// output directory at /out, and nothing else; it has no network.
func (e *engine) exec(ctx context.Context, j *job, m *meter) (int, error) { panic("not implemented") }
