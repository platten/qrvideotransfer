package cli

import (
	"errors"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	qrvt "github.com/platten/qrvideotransfer"
	"github.com/platten/qrvideotransfer/internal/frame"
)

// preset is a named video size (FORMAT.md section 3).
type preset struct {
	name string
	w, h int
}

// presets are the --resolution presets; "2160p" is another name for "4k".
var presets = []preset{{"480p", 848, 480}, {"720p", 1280, 720}, {"1080p", 1920, 1080}, {"1440p", 2560, 1440}, {"4k", 3840, 2160}}

var (
	sizeRE = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*([a-zA-Z]*)$`)
	whRE   = regexp.MustCompile(`^(\d{1,5})[xX](\d{1,5})$`)

	errBadSize    = errors.New("use a positive size such as 500MB or 16GiB")
	errTooLarge   = errors.New("too large")
	errResolution = errors.New("use a preset (480p, 720p, 1080p, 1440p, 4k) or WxH such as 1920x1080")

	sizeUnits = map[string]int64{
		"": 1, "b": 1,
		"k": 1 << 10, "kib": 1 << 10, "kb": 1e3,
		"m": 1 << 20, "mib": 1 << 20, "mb": 1e6,
		"g": 1 << 30, "gib": 1 << 30, "gb": 1e9,
		"t": 1 << 40, "tib": 1 << 40, "tb": 1e12,
	}
	modes        = map[string]qrvt.Modulation{"gray4": qrvt.Gray4, "bw": qrvt.Gray2, "color": qrvt.RGB8}
	protections  = map[string]qrvt.Protection{"fast": qrvt.Fast, "normal": qrvt.Normal, "robust": qrvt.Robust}
	compressions = map[string]qrvt.Compression{"auto": qrvt.CompressAuto, "zstd": qrvt.CompressZstd, "xz": qrvt.CompressXZ, "none": qrvt.CompressNone}
)

// The format's limits on the manifest (SECURITY.md section 5): entries and size in MiB.
const (
	maxEntries     = 1_000_000
	maxManifestMiB = 64
)

// maxDimension bounds a requested width or height before any other check.
const maxDimension = 16384

// parseSize parses a byte count with an optional unit, in any case: B; K, KiB, M, MiB, G, GiB, T,
// TiB (binary); kB, MB, GB, TB (decimal). Fractions are allowed when the result is whole bytes.
func parseSize(s string) (int64, error) {
	m := sizeRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, errBadSize
	}
	mult, ok := sizeUnits[strings.ToLower(m[2])]
	if !ok {
		return 0, errBadSize
	}
	r, _ := new(big.Rat).SetString(m[1]) // the expression admits only valid numbers
	r.Mul(r, new(big.Rat).SetInt64(mult))
	switch {
	case !r.IsInt() || r.Sign() <= 0:
		return 0, errBadSize
	case !r.Num().IsInt64():
		return 0, errTooLarge
	}
	return r.Num().Int64(), nil
}

// parseResolution parses a preset name or WxH.
func parseResolution(s string) (w, h int, err error) {
	name := strings.ToLower(s)
	if name == "2160p" {
		name = "4k"
	}
	for _, p := range presets {
		if p.name == name {
			return p.w, p.h, nil
		}
	}
	m := whRE.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, errResolution
	}
	w, _ = strconv.Atoi(m[1])
	h, _ = strconv.Atoi(m[2])
	if w < 1 || h < 1 || w > maxDimension || h > maxDimension {
		return 0, 0, errResolution
	}
	return w, h, nil
}

// fitCell reduces a requested size to multiples of the cell and checks the grid against the limits
// of FORMAT.md section 3. larger is the advice when the grid is too large.
func fitCell(w, h, cell int, larger string) (int, int, error) {
	fw, fh := w-w%cell, h-h%cell
	gx, gy := fw/cell, fh/cell
	switch {
	case fh < 480:
		return 0, 0, usagef("--resolution %dx%d: the height must be at least 480", w, h)
	case gx > frame.MaxGrid:
		return 0, 0, usagef("--resolution %dx%d: %d cells across; the most is %d, %s", w, h, gx, frame.MaxGrid, larger)
	case gy > frame.MaxGrid:
		return 0, 0, usagef("--resolution %dx%d: %d cells down; the most is %d, %s", w, h, gy, frame.MaxGrid, larger)
	case frame.CheckGrid(gx, gy) != nil:
		return 0, 0, usagef("--resolution %dx%d: the height must be less than 15 times the width", w, h)
	}
	return fw, fh, nil
}

// pathError returns the reason of a file-system error without the operation and path.
func pathError(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// statErr is os.Stat with the reason of a failure only.
func statErr(path string) (fs.FileInfo, error) {
	st, err := os.Stat(path)
	return st, pathError(err)
}

// checkBackend validates --ffmpeg.
func checkBackend(s string) error {
	switch {
	case s == "embedded" || s == "system":
		return nil
	case strings.TrimSpace(s) == "":
		return usagef("--ffmpeg: use embedded, system or the path of an ffmpeg program")
	}
	st, err := os.Stat(s)
	switch {
	case err != nil:
		return usagef("--ffmpeg %s: %v", s, pathError(err))
	case st.IsDir():
		return usagef("--ffmpeg %s: is a folder", s)
	}
	return nil
}

// checkJobs validates --jobs.
func checkJobs(n int) error {
	if n < 0 || n > 1024 {
		return usagef("--jobs %d: use 0 (the CPU count) to 1024", n)
	}
	return nil
}

// checkFile checks that what names an existing file that is not a folder.
func checkFile(what, path string) error {
	st, err := os.Stat(path)
	switch {
	case err != nil:
		return usagef("%s %s: %v", what, path, pathError(err))
	case st.IsDir():
		return usagef("%s %s: is a folder", what, path)
	}
	return nil
}

// checkFolder checks that path, if it exists, is a folder.
func checkFolder(what, path string) error {
	if st, err := os.Stat(path); err == nil && !st.IsDir() {
		return usagef("%s %s: not a folder", what, path)
	}
	return nil
}

// checkNewFile checks that path can be created: its folder exists and it is not a folder itself.
func checkNewFile(what, path string) error {
	if st, err := os.Stat(path); err == nil && st.IsDir() {
		return usagef("%s %s: is a folder", what, path)
	}
	dir := filepath.Dir(path)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return usagef("%s %s: the folder %s does not exist", what, path, dir)
	}
	return nil
}

// choose looks up a flag value in a table of names.
func choose[T any](flag, v string, table map[string]T, names string) (T, error) {
	if x, ok := table[strings.ToLower(v)]; ok {
		return x, nil
	}
	var zero T
	return zero, usagef("--%s %q: use %s", flag, v, names)
}

// modeName and protectionName name the enum values as the flags do.
func modeName(m qrvt.Modulation) string {
	switch m {
	case qrvt.Gray2:
		return "bw"
	case qrvt.Gray4:
		return "gray4"
	case qrvt.RGB8:
		return "color"
	case qrvt.Gray8:
		return "gray8 (hdr)"
	}
	return "unknown"
}

func protectionName(p qrvt.Protection) string {
	for n, v := range protections {
		if v == p {
			return n
		}
	}
	return "unknown"
}
