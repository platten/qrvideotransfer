package video

import (
	"context"
)

// Options configure [New] and [NewEmbedded].
type Options struct {
	UnsandboxedHEVC bool

	CacheDir string
}

// New returns the backend that spec names: "embedded" (or ""), "system", or the path of an ffmpeg
// executable.
func New(spec string, o Options) (Backend, error) { panic("not implemented") }

// Embedded is the default backend: ffmpeg compiled to WebAssembly, run by wazero in a sandbox
// (docs/SECURITY.md T1). The module is decompressed and compiled on first use, not at start-up, and
// shared by every instance in the process.
type Embedded struct {
	ff
}

// NewEmbedded returns the embedded backend.
func NewEmbedded(o Options) (*Embedded, error) { panic("not implemented") }

// lazyEngine runs each job in an engine worker process (see ServeWorker). When there is none, or
// one cannot be started, it runs the job in this process with close-on-cancel instead, which is
// slower but keeps every guarantee; WorkerStatus says so.
type lazyEngine struct{ cacheDir string }

func (l lazyEngine) exec(ctx context.Context, j *job, m *meter) (int, error) {
	panic("not implemented")
}
