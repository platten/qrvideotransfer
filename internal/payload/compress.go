package payload

import (
	"errors"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// Automatic compression (DESIGN.md section 9): the first autoSample bytes of the archive are
// compressed with zstd; unless that saves at least autoMinSaving percent, the archive is stored as-is.
const (
	autoSample    = 4 << 20
	autoMinSaving = 2
)

// compressor returns a writer that compresses to w with m. Close flushes it but does not close w.
func compressor(w io.Writer, m Method) (io.WriteCloser, error) {
	switch m {
	case MethodZstd:
		return zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.SpeedBestCompression), zstd.WithWindowSize(MaxZstdWindow))
	case MethodXZ:
		return xz.WriterConfig{DictCap: XZDictCap}.NewWriter(w)
	default:
		return nopCloser{w}, nil
	}
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// decompressor returns a reader that decompresses r with m, with the memory limits of this package,
// and a function that releases it.
func decompressor(r io.Reader, m Method) (io.Reader, func(), error) {
	switch m {
	case MethodZstd:
		d, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxWindow(MaxZstdWindow),
			zstd.WithDecoderMaxMemory(MaxZstdWindow))
		if err != nil {
			return nil, nil, fmt.Errorf("%w: zstd: %w", ErrFormat, err)
		}
		return d, d.Close, nil
	case MethodXZ:
		g := &xzGuard{r: r}
		x, err := xz.ReaderConfig{DictCap: XZDictCap, SingleStream: true}.NewReader(g)
		if err != nil {
			return nil, nil, errors.Join(g.err, fmt.Errorf("%w: xz: %w", ErrFormat, err))
		}
		return guarded{x, g}, func() {}, nil
	default:
		return r, func() {}, nil
	}
}

// guarded reports the guard's error rather than the library's rendering of it.
type guarded struct {
	r io.Reader
	g *xzGuard
}

func (d guarded) Read(p []byte) (int, error) {
	n, err := d.r.Read(p)
	if err != nil && d.g.err != nil {
		err = d.g.err
	}
	return n, err
}

// methodFor returns the stream method for a compression choice; for CompressAuto, sample yields the
// beginning of the archive.
func methodFor(c Compression, sample func(w io.Writer) error) (Method, error) {
	switch c {
	case CompressZstd:
		return MethodZstd, nil
	case CompressXZ:
		return MethodXZ, nil
	case CompressNone:
		return MethodNone, nil
	case CompressAuto:
	default:
		return 0, fmt.Errorf("payload: unknown compression %d", c)
	}
	var in, out counter
	zw, err := compressor(&out, MethodZstd)
	if err == nil {
		err = sample(&limitWriter{io.MultiWriter(&in, zw), autoSample})
		if errors.Is(err, errSampleFull) {
			err = nil
		}
		err = errors.Join(err, zw.Close())
	}
	if err != nil {
		return 0, err
	}
	if out.n*100 > in.n*(100-autoMinSaving) {
		return MethodNone, nil
	}
	return MethodZstd, nil
}

// counter counts the bytes written to it.
type counter struct{ n int64 }

func (c *counter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }

var errSampleFull = errors.New("payload: sample complete")

// limitWriter passes on at most n bytes and then fails with errSampleFull.
type limitWriter struct {
	w io.Writer
	n int64
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > l.n {
		k, err := l.w.Write(p[:l.n])
		l.n -= int64(k)
		if err == nil {
			err = errSampleFull
		}
		return k, err
	}
	k, err := l.w.Write(p)
	l.n -= int64(k)
	return k, err
}
