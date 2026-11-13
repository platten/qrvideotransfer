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
}

var engines struct {
	sync.Mutex
	m map[string]*engine
}

// loadEngine returns the process's engine for a compilation cache directory ("" for none), compiling
// it on first use.
func loadEngine(ctx context.Context, cacheDir string) (*engine, error) {
	engines.Lock()
	defer engines.Unlock()
	if e := engines.m[cacheDir]; e != nil {
		return e, nil
	}
	e, err := newEngine(ctx, cacheDir, true)
	if err != nil {
		return nil, err
	}
	if engines.m == nil {
		engines.m = map[string]*engine{}
	}
	engines.m[cacheDir] = e
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
	cfg := wazero.NewRuntimeConfig().
		WithCloseOnContextDone(closeOnDone).
		WithMemoryLimitPages(EngineMemoryLimit / 65536).
		WithMemoryCapacityFromMax(true)
	if cacheDir != "" {
		if c, err := wazero.NewCompilationCacheWithDir(cacheDir); err == nil {
			cfg = cfg.WithCompilationCache(c)
		}
	}
	rt, err := wasm.NewRuntime(ctx, cfg)
	if err != nil {
		return nil, err
	}
	cm, err := rt.CompileModule(ctx, b)
	if err != nil {
		_ = rt.Close(ctx)
		return nil, err
	}
	return &engine{rt: rt, cm: cm}, nil
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

// EngineInstances is how many engine instances may run at once.
func EngineInstances() int { return 0 }

// exec implements runner. The instance sees the input file read-only through inputFS at /in, the
// output directory at /out, and nothing else; it has no network.
func (e *engine) exec(ctx context.Context, j *job, m *meter) (int, error) {
	var in string
	reads := &atomic.Int64{}
	if j.progress == progReads {
		reads = &m.n
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
