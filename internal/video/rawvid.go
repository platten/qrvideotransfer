package video

import (
	"bufio"
	"context"
	"io"
	"os"
	"time"
)

// Raw is the raw-frame file backend, for tests: a 32-byte header followed by the frames' samples,
// uncompressed. Header: "QRVTRAW1", width and height (uint32), bit depth (uint8, 8 or 10), plane
// count (uint8, always 3), two reserved bytes, the frame rate as numerator and denominator (uint32),
// four reserved bytes; all little-endian. Each frame is its Y, Cb and Cr planes (4:2:0), one byte
// per sample at 8 bits and two at 10.
type Raw struct{}

// NewRaw returns the raw-frame backend.
func NewRaw() *Raw { panic("not implemented") }

const rawMagic = "QRVTRAW1"
const rawHeaderLen = 32

type rawHeader struct {
	w, h, bits   int
	fpsN, fpsD   int
	frameBytes   int64
	frames, size int64
}

func (h rawHeader) info() ClipInfo { panic("not implemented") }

func rawFrameBytes(w, h, bits int) int64 { panic("not implemented") }

func encodeRawHeader(w, h, bits, fps int) []byte { panic("not implemented") }

// readRawHeader reads and validates the header of f, whose size is size.
func readRawHeader(f io.Reader, size int64) (rawHeader, error) { panic("not implemented") }

func openRaw(path string) (*os.File, rawHeader, error) { panic("not implemented") }

// Probe implements [Backend].
func (*Raw) Probe(_ context.Context, path string) (ClipInfo, error) { panic("not implemented") }

// NewEncoder implements [Backend]. The configuration's quantiser is ignored.
func (*Raw) NewEncoder(_ context.Context, path string, c EncodeConfig) (Encoder, error) {
	panic("not implemented")
}

type rawEncoder struct {
	f    *os.File
	w    *bufio.Writer
	c    EncodeConfig
	bits int
	buf  []byte
}

func (e *rawEncoder) WriteFrame(fr *Frame) error { panic("not implemented") }

func (e *rawEncoder) Close() error { panic("not implemented") }

// NewDecoder implements [Backend].
func (*Raw) NewDecoder(_ context.Context, path string, c DecodeConfig) (Decoder, error) {
	panic("not implemented")
}

// sliceFrames returns the frame numbers [first, last) whose times fall in the time slice of c.
func sliceFrames(c DecodeConfig, fpsN, fpsD int, frames int64) (first, last int64) {
	panic("not implemented")
}

func frameTime(i int64, fpsN, fpsD int) time.Duration { panic("not implemented") }

type rawDecoder struct {
	f          *bufio.Reader
	c          io.Closer
	h          rawHeader
	next, last int64
	gray       bool
	buf        []byte
}

func (d *rawDecoder) Info() ClipInfo { panic("not implemented") }

func (d *rawDecoder) Next(fr *Frame) error { panic("not implemented") }

func (d *rawDecoder) Close() error { panic("not implemented") }

// Concat joins raw-frame files with identical headers into one file at path.
func (*Raw) Concat(_ context.Context, runs []string, path string, fps int) error {
	panic("not implemented")
}

// checkEncodeConfig validates an encoder configuration.
func checkEncodeConfig(c EncodeConfig) error { panic("not implemented") }

// packFrame appends the planes of f as raw samples (yuv420p, or yuv420p10le at 10 bits).
func packFrame(dst []byte, f *Frame, w, h, bits int) ([]byte, error) { panic("not implemented") }

// unpackFrame fills f from raw samples: Y, then Cb and Cr unless gray (b may then hold only Y).
func unpackFrame(f *Frame, b []byte, w, h, bits int, gray bool) { panic("not implemented") }

func unpackPlane(dst []uint16, b []byte, n, bits int) []uint16 { panic("not implemented") }
