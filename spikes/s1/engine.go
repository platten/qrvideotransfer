package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"codeberg.org/gruf/go-ffmpreg/embed"
	"codeberg.org/gruf/go-ffmpreg/wasm"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/experimental"
)

// engine is one compiled ffmpreg module inside one wazero runtime. Any
// number of instances may run concurrently from it.
type engine struct {
	rt      wazero.Runtime
	cm      wazero.CompiledModule
	native  bool // run the system ffmpeg instead, for comparison
	Compile time.Duration
}

// newEngine compiles the embedded module, using a compilation cache in dir
// (empty: no cache). interp selects wazero's interpreter.
func newEngine(ctx context.Context, cacheDir string, interp bool, limitPages uint32) (*engine, error) {
	start := time.Now()
	cfg := wazero.NewRuntimeConfigCompiler()
	if interp {
		cfg = wazero.NewRuntimeConfigInterpreter()
	}
	if cacheDir != "" {
		c, err := wazero.NewCompilationCacheWithDir(cacheDir)
		if err != nil {
			return nil, err
		}
		cfg = cfg.WithCompilationCache(c)
	}
	if limitPages > 0 {
		// Reserve the whole limit up front: the backing slice is never
		// reallocated, so growth leaves no garbage for the Go GC, and
		// untouched pages are not resident.
		cfg = cfg.WithMemoryLimitPages(limitPages).WithMemoryCapacityFromMax(true)
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
	return &engine{rt: rt, cm: cm, Compile: time.Since(start)}, nil
}

// run describes one invocation of ffmpeg or ffprobe.
type run struct {
	Name   string // ffmpeg or ffprobe
	Args   []string
	Dir    string // host directory mounted at /d (and used as cwd for native)
	RO     bool   // mount read-only
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// result reports one invocation.
type result struct {
	RC      uint32
	Wall    time.Duration
	PeakMem uint64 // peak wasm linear memory, bytes
}

// Run executes r. Paths inside Args must refer to /d/... for the engine;
// for the native backend /d/ is rewritten to the host directory.
func (e *engine) Run(ctx context.Context, r run) (result, error) {
	start := time.Now()
	if r.Stderr == nil {
		r.Stderr = io.Discard
	}
	if r.Stdout == nil {
		r.Stdout = io.Discard
	}
	if e.native {
		args := make([]string, len(r.Args))
		for i, a := range r.Args {
			args[i] = strings.ReplaceAll(a, "/d/", r.Dir+"/")
		}
		cmd := exec.CommandContext(ctx, r.Name, args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = r.Stdin, r.Stdout, r.Stderr
		err := cmd.Run()
		rc := uint32(0)
		if ee, ok := err.(*exec.ExitError); ok {
			rc, err = uint32(ee.ExitCode()), nil
		}
		return result{RC: rc, Wall: time.Since(start)}, err
	}
	var peak atomic.Uint64
	ctx = experimental.WithMemoryAllocator(ctx, experimental.MemoryAllocatorFunc(
		func(c, max uint64) experimental.LinearMemory {
			return &linearMem{buf: make([]byte, 0, c), peak: &peak}
		}))
	rc, err := wasm.Run(ctx, e.rt, e.cm, wasm.Args{
		Name: r.Name, Args: r.Args,
		Stdin: r.Stdin, Stdout: r.Stdout, Stderr: r.Stderr,
		Config: func(c wazero.ModuleConfig) wazero.ModuleConfig {
			c = c.WithSysWalltime().WithSysNanotime()
			if r.Dir == "" {
				return c
			}
			fs := wazero.NewFSConfig()
			if r.RO {
				fs = fs.WithReadOnlyDirMount(r.Dir, "/d")
			} else {
				fs = fs.WithDirMount(r.Dir, "/d")
			}
			return c.WithFSConfig(fs)
		},
	})
	return result{RC: rc, Wall: time.Since(start), PeakMem: peak.Load()}, err
}

// linearMem backs a wasm memory with a Go slice and records its peak size.
type linearMem struct {
	buf  []byte
	peak *atomic.Uint64
}

func (m *linearMem) Reallocate(size uint64) []byte {
	if size > uint64(cap(m.buf)) {
		nb := make([]byte, size, size+size/4)
		copy(nb, m.buf)
		m.buf = nb
	} else {
		m.buf = m.buf[:size]
	}
	for {
		p := m.peak.Load()
		if size <= p || m.peak.CompareAndSwap(p, size) {
			break
		}
	}
	return m.buf
}

func (m *linearMem) Free() { m.buf = nil }

// probeInfo is the subset of ffprobe output the spike needs.
type probeInfo struct {
	Codec, PixFmt, Transfer, Primaries, Space, Range string
	W, H                                             int
	Frames                                           int
}

// probe runs ffprobe (embedded or native) on dir/name.
func (e *engine) probe(ctx context.Context, dir, name string) (probeInfo, error) {
	var out, errb bytes.Buffer
	res, err := e.Run(ctx, run{Name: "ffprobe", Dir: dir, RO: true, Stdout: &out, Stderr: &errb, Args: []string{
		"-v", "error", "-select_streams", "v:0", "-count_packets",
		"-show_entries", "stream=codec_name,width,height,pix_fmt,color_transfer,color_primaries,color_space,color_range,nb_read_packets",
		"-of", "json", "/d/" + name}})
	if err != nil || res.RC != 0 {
		return probeInfo{}, fmt.Errorf("ffprobe rc=%d err=%v: %s", res.RC, err, errb.String())
	}
	var j struct {
		Streams []struct {
			CodecName      string `json:"codec_name"`
			Width          int    `json:"width"`
			Height         int    `json:"height"`
			PixFmt         string `json:"pix_fmt"`
			ColorTransfer  string `json:"color_transfer"`
			ColorPrimaries string `json:"color_primaries"`
			ColorSpace     string `json:"color_space"`
			ColorRange     string `json:"color_range"`
			NbReadPackets  string `json:"nb_read_packets"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out.Bytes(), &j); err != nil || len(j.Streams) == 0 {
		return probeInfo{}, fmt.Errorf("ffprobe output: %v %q", err, out.String())
	}
	s := j.Streams[0]
	n, _ := strconv.Atoi(s.NbReadPackets)
	return probeInfo{s.CodecName, s.PixFmt, s.ColorTransfer, s.ColorPrimaries, s.ColorSpace, s.ColorRange, s.Width, s.Height, n}, nil
}

// splitPath returns the absolute directory and base name of p.
func splitPath(p string) (string, string) {
	a, err := filepath.Abs(p)
	if err != nil {
		a = p
	}
	return filepath.Dir(a), filepath.Base(a)
}

// rss returns the process's current and peak resident set size in bytes.
func rss() (cur, peak uint64) {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(f[1], 10, 64)
		switch f[0] {
		case "VmRSS:":
			cur = v * 1024
		case "VmHWM:":
			peak = v * 1024
		}
	}
	return
}

func mb(b uint64) float64 { return float64(b) / (1 << 20) }
