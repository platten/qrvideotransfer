package main

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"codeberg.org/gruf/go-ffmpreg/embed"
	"codeberg.org/gruf/go-ffmpreg/wasm"
	"github.com/tetratelabs/wazero"
)

// engine is the S1 wrapper: one compiled ffmpreg module in one wazero
// runtime, from which any number of instances run concurrently.
type engine struct {
	rt wazero.Runtime
	cm wazero.CompiledModule
}

// memPages is each instance's linear-memory limit, reserved up front
// (spike S1: growth by reallocation doubles resident memory).
const memPages = 8192 // 512 MiB

func newEngine(ctx context.Context) (*engine, error) {
	cfg := wazero.NewRuntimeConfigCompiler().WithMemoryLimitPages(memPages).WithMemoryCapacityFromMax(true)
	if d, err := os.UserCacheDir(); err == nil {
		if c, err := wazero.NewCompilationCacheWithDir(filepath.Join(d, "qrvt-spike-s1", "wazero")); err == nil {
			cfg = cfg.WithCompilationCache(c)
		}
	}
	rt, err := wasm.NewRuntime(ctx, cfg)
	if err != nil {
		return nil, err
	}
	cm, err := rt.CompileModule(ctx, embed.B())
	if err != nil {
		return nil, err
	}
	embed.Free()
	return &engine{rt: rt, cm: cm}, nil
}

// run executes ffmpeg (or ffprobe) with dir mounted at /d. It returns the
// exit code.
func (e *engine) run(ctx context.Context, name, dir string, args []string, stdin io.Reader, stdout, stderr io.Writer) (uint32, error) {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	return wasm.Run(ctx, e.rt, e.cm, wasm.Args{
		Name: name, Args: args, Stdin: stdin, Stdout: stdout, Stderr: stderr,
		Config: func(c wazero.ModuleConfig) wazero.ModuleConfig {
			c = c.WithSysWalltime().WithSysNanotime()
			if dir == "" {
				return c
			}
			return c.WithFSConfig(wazero.NewFSConfig().WithDirMount(dir, "/d"))
		},
	})
}
