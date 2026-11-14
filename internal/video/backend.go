package video

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
)

// Options configure [New] and [NewEmbedded].
type Options struct {
	// UnsandboxedHEVC decodes HEVC with the pure-Go gen2brain/h265 in the qrvt process, outside the
	// sandbox (--unsandboxed-hevc). For recordings the user made themselves: it runs outside the
	// sandbox and its memory is not bounded against a crafted stream (docs/SECURITY.md section 4).
	// Only builds with the unsandboxedhevc tag have it; others refuse the option.
	UnsandboxedHEVC bool
	// CacheDir holds the engine's compiled code. Empty: qrvt/wazero under os.UserCacheDir().
	CacheDir string
}

// New returns the backend that spec names: "embedded" (or ""), "system", or the path of an ffmpeg
// executable.
func New(spec string, o Options) (Backend, error) {
	if spec == "" || spec == "embedded" {
		return NewEmbedded(o)
	}
	if o.UnsandboxedHEVC {
		return nil, fmt.Errorf("%w: --unsandboxed-hevc applies to the embedded engine only", ErrUnsupported)
	}
	return NewSystem(spec)
}

// Embedded is the default backend: ffmpeg compiled to WebAssembly, run by wazero in a sandbox
// (docs/SECURITY.md T1). The module is decompressed and compiled on first use, not at start-up, and
// shared by every instance in the process.
type Embedded struct {
	ff
}

// NewEmbedded returns the embedded backend.
func NewEmbedded(o Options) (*Embedded, error) {
	if o.UnsandboxedHEVC && !FastPathBuilt {
		return nil, fmt.Errorf("%w: this build has no HEVC fast path (build tag unsandboxedhevc)", ErrUnsupported)
	}
	dir := o.CacheDir
	if dir == "" {
		dir = defaultCacheDir()
	}
	return &Embedded{ff{r: lazyEngine{cacheDir: dir, fell: &atomic.Pointer[error]{}}, fast: o.UnsandboxedHEVC}}, nil
}

// InProcess is nil while every job of this backend has run in an engine worker process. Otherwise it
// says why one ran in qrvt's process instead (about 4 to 11 times slower), and it keeps saying so for
// the backend's life. A caller that makes one backend per job (the pipeline makes one per decode)
// asks it at the job's end and warns once for that job (WarnEngineInProcess); [WorkerStatus] instead
// describes the process's current state.
func (b *Embedded) InProcess() error {
	l, ok := b.r.(lazyEngine)
	if !ok || l.fell == nil {
		return nil
	}
	if p := l.fell.Load(); p != nil {
		return fmt.Errorf("video engine ran in process, about 4 to 11 times slower: %w", *p)
	}
	return nil
}

// lazyEngine runs each job in an engine worker process (see ServeWorker). When there is none, or
// one cannot be started, it runs the job in this process with close-on-cancel instead, which is
// slower but keeps every guarantee; WorkerStatus and the backend's InProcess say so. While the
// engine is compiled the instance waits on qrvt, so neither the watchdog nor a probe's time bound
// counts that time.
type lazyEngine struct {
	cacheDir string
	fell     *atomic.Pointer[error] // the first reason a job of this backend ran in process
}

// fallBack records why a job runs in process, for the process (WorkerStatus) and the backend.
func (l lazyEngine) fallBack(err error) {
	degraded.Store(&err)
	if l.fell != nil {
		l.fell.CompareAndSwap(nil, &err)
	}
}

func (l lazyEngine) exec(ctx context.Context, j *job, m *meter) (int, error) {
	if j.trusted {
		m.waiting.Add(1) // compiling on first use is not a stall
		e, err := loadEngine(ctx, l.cacheDir, false)
		m.waiting.Add(-1)
		if err != nil {
			return 0, err
		}
		return e.exec(ctx, j, m)
	}
	w, why := currentWorkers()
	if w != nil {
		rc, err := w.exec(ctx, j, m, l.cacheDir)
		if !errors.Is(err, errWorkerStart) {
			return rc, err
		}
		why = err
	}
	l.fallBack(why)
	m.waiting.Add(1) // compiling on first use is not a stall
	e, err := loadEngine(ctx, l.cacheDir, true)
	m.waiting.Add(-1)
	if err != nil {
		return 0, err
	}
	return e.exec(ctx, j, m)
}
