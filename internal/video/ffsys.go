package video

import (
	"context"
)

// System is the backend that runs a native ffmpeg and ffprobe as separate processes (--ffmpeg system
// or a path). It has no sandbox: a crafted file meets that ffmpeg's own robustness alone, so it is
// for recordings the user made themselves (docs/SECURITY.md section 4).
type System struct {
	ff
}

type sysRunner struct{ ffmpeg, ffprobe string }

// NewSystem returns the native backend. ffmpeg is "system" (or "") to search PATH, or the path of an
// ffmpeg executable; ffprobe is taken from the same directory, or else from PATH.
func NewSystem(ffmpeg string) (*System, error) { panic("not implemented") }

// exec implements runner. Paths are passed with the file: protocol prefix, so that no name is read
// as an option or another protocol.
func (r sysRunner) exec(ctx context.Context, j *job, m *meter) (int, error) { panic("not implemented") }
