package video

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

// ErrEngine reports that ffmpeg (embedded or native) failed; the message carries the end of its log.
var ErrEngine = errors.New("video: the video engine failed")

// progress names what counts as progress for the watchdog.
type progress int

const (
	progOutput progress = iota
	progReads
	progInput
	progFiles
)

// job is one run of ffmpeg or ffprobe.
type job struct {
	prog string

	args     func(in, out string) []string
	input    string
	outDir   string
	stdin    io.Reader
	stdout   io.Writer
	stderr   io.Writer
	progress progress
	timeout  time.Duration

	maxOutput int64

	module string

	trusted bool
}

// runner runs jobs: the embedded engine or a native ffmpeg.
type runner interface {
	exec(ctx context.Context, j *job, m *meter) (int, error)
}

// run executes j under the watchdog. A non-zero exit code becomes an [ErrEngine] error carrying the
// end of the program's log; a stall becomes the EngineStallTimeout [LimitError].
func run(ctx context.Context, r runner, j job) error { panic("not implemented") }

// dirSize is the total size of the regular files directly in dir.
func dirSize(dir string) int64 { panic("not implemented") }

// logTail keeps the last bytes written to it, for error messages.
type logTail struct {
	mu sync.Mutex
	b  []byte
}

const logTailLen = 600

func (t *logTail) Write(p []byte) (int, error) { panic("not implemented") }

func (t *logTail) String() string { panic("not implemented") }

// inputName is the name under which a decoding instance sees its input: "input" plus the file's
// extension when that is short and alphanumeric (ffmpeg uses it as a hint).
func inputName(path string) string { panic("not implemented") }
