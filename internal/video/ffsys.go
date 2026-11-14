package video

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
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
func NewSystem(ffmpeg string) (*System, error) {
	r := sysRunner{}
	var err error
	if ffmpeg == "" || ffmpeg == "system" {
		if r.ffmpeg, err = exec.LookPath("ffmpeg"); err != nil {
			return nil, fmt.Errorf("%w: no ffmpeg on PATH", ErrUnsupported)
		}
	} else if r.ffmpeg, err = exec.LookPath(ffmpeg); err != nil {
		return nil, fmt.Errorf("%w: %s is not an executable: %w", ErrUnsupported, ffmpeg, err)
	}
	probe := "ffprobe"
	if runtime.GOOS == "windows" {
		probe += ".exe"
	}
	sibling := filepath.Join(filepath.Dir(r.ffmpeg), probe)
	if st, err := os.Stat(sibling); err == nil && st.Mode().IsRegular() {
		r.ffprobe = sibling
	} else if r.ffprobe, err = exec.LookPath("ffprobe"); err != nil {
		return nil, fmt.Errorf("%w: no ffprobe beside %s or on PATH", ErrUnsupported, r.ffmpeg)
	}
	return &System{ff{r: r}}, nil
}

// exec implements runner. Paths are passed with the file: protocol prefix, so that no name is read
// as an option or another protocol.
func (r sysRunner) exec(ctx context.Context, j *job, m *meter) (int, error) {
	bin := r.ffmpeg
	if j.prog == "ffprobe" {
		bin = r.ffprobe
	}
	var in string
	if j.input != "" {
		abs, err := filepath.Abs(j.input)
		if err != nil {
			return 0, err
		}
		in = "file:" + abs
	}
	out := ""
	if j.outDir != "" {
		out = "file:" + j.outDir
	}
	cmd := exec.CommandContext(ctx, bin, j.args(in, out)...) //nolint:gosec // the user chose this ffmpeg
	cmd.Stdin, cmd.Stdout, cmd.Stderr = j.stdin, j.stdout, j.stderr
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	if ee := (*exec.ExitError)(nil); errors.As(err, &ee) && ctx.Err() == nil {
		return ee.ExitCode(), nil
	}
	return 0, err
}
