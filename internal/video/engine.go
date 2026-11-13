package video

import (
	"context"
	_ "embed"
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
}

var engines struct {
	sync.Mutex
	m map[string]*engine
}

// loadEngine returns the process's engine for a compilation cache directory ("" for none), compiling
// it on first use.
func loadEngine(ctx context.Context, cacheDir string) (*engine, error) { panic("not implemented") }

// engineWasm decompresses the module and checks its hash.
func engineWasm() ([]byte, error) { panic("not implemented") }

// newEngine creates a runtime as DESIGN.md section 12 requires: close on context done (so the
// watchdog and cancellation interrupt running code), 512 MiB of linear memory per instance reserved
// up front, and a compilation cache. closeOnDone is false only to measure its cost.
func newEngine(ctx context.Context, cacheDir string, closeOnDone bool) (*engine, error) {
	panic("not implemented")
}

// defaultCacheDir is where compiled code is kept: qrvt/wazero under the user's cache directory, or
// nowhere when there is none.
func defaultCacheDir() string { panic("not implemented") }

// exec implements runner. The instance sees the input file read-only through inputFS at /in, the
// output directory at /out, and nothing else; it has no network.
func (e *engine) exec(ctx context.Context, j *job, m *meter) (int, error) { panic("not implemented") }
