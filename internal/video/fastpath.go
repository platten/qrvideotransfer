//go:build unsandboxedhevc

package video

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/gen2brain/h265/hevc"
)

// FastPathBuilt reports whether this build contains the HEVC fast path (build tag unsandboxedhevc).
// It stays out of release builds until the decoder can be built without its hand-written assembly
// (DESIGN.md section 12); build it with -tags unsandboxedhevc,noasm.
const FastPathBuilt = true

// The fast path's limits (docs/SECURITY.md section 5). They reduce risk; they do not bound memory.
const (
	fastMaxPixels = 7680 * 4320

	fastMaxDPB = 6

	fastMaxNAL = 64 << 20
)

// FastPathPictureTime is the time a picture may take on the HEVC fast path. The decoder cannot be
// cancelled, so the first picture that exceeds it stops the whole decode, leaving its goroutine
// behind; the caller must not continue decoding in this process.
const FastPathPictureTime = 10 * time.Second

// pictureTime is FastPathPictureTime; a test shortens it.
var pictureTime = FastPathPictureTime

// nalDecoder is the part of hevc.Decoder the fast path uses; tests substitute a decoder that
// misbehaves.
type nalDecoder interface {
	DecodeNAL(hevc.NALUnit) ([]*hevc.Picture, error)
	Flush() []*hevc.Picture
}

// newNALDecoder makes the decoder for one slice. threads must be 1.
var newNALDecoder = func(threads int) (nalDecoder, error) { panic("not implemented") }

// newH265 returns a gen2brain/h265 decoder with exactly one thread: with any other value, 0 (which
// means GOMAXPROCS) included, it starts goroutines of its own whose panics no caller can recover.
func newH265(threads int) (*hevc.Decoder, error) { panic("not implemented") }

// errRefused marks a stream the fast path hands to the engine.
var errRefused = errors.New("video: refused by the HEVC fast path")

// newFastDecoder decodes an HEVC slice with gen2brain/h265 in this process, outside the sandbox. A
// separate engine instance first copies the slice out of its container (input read-only, one
// writable temporary directory) and finishes; then every sequence parameter set is checked; then the
// stream is decoded. On any error, panic or refusal the rest of the slice is decoded by the engine.
func newFastDecoder(ctx context.Context, b *ff, path string, p probeInfo, c DecodeConfig) (Decoder, error) {
	panic("not implemented")
}

type fastDecoder struct {
	ctx      context.Context
	b        *ff
	path     string
	p        probeInfo
	c        DecodeConfig
	pix      string
	tmp      string
	f        *os.File
	nals     *nalReader
	w        *nalWorker
	pts      []int64
	tbN      int64
	tbD      int64
	au       uint64
	queue    []*hevc.Picture
	ended    bool
	pixels   float64
	last     time.Duration
	engine   Decoder
	fellBack error
	fail     error
}

func (d *fastDecoder) Info() ClipInfo { panic("not implemented") }

// start copies the slice out of its container and checks its parameter sets.
func (d *fastDecoder) start() error { panic("not implemented") }

func (d *fastDecoder) openStream() error { panic("not implemented") }

// copyArgs copies the slice, from a preroll before it, as an Annex B stream, and writes each
// packet's timestamps (framemd5) beside it.
func copyArgs(in, out string, p probeInfo, c DecodeConfig) []string { panic("not implemented") }

// readTimestamps reads the packets' presentation timestamps from the framemd5 file.
func (d *fastDecoder) readTimestamps(name string) error { panic("not implemented") }

// checkStream reads every NAL unit and refuses the stream unless every sequence parameter set
// declares the probed size, 4:2:0, 8 or 10 bits as probed, at most fastMaxPixels and a buffer of at
// most fastMaxDPB pictures in every sub-layer.
func checkStream(r *nalReader, p probeInfo) error { panic("not implemented") }

// checkSPS applies the fast path's parameter-set rules to one SPS.
func checkSPS(rbsp []byte, p probeInfo) error { panic("not implemented") }

// parseSPSLimits reads the coded size and the largest sps_max_dec_pic_buffering_minus1 + 1 over all
// sub-layers (the decoder keeps only the last sub-layer's value).
func parseSPSLimits(rbsp []byte) (w, h, dpb int, err error) { panic("not implemented") }

// bitReader reads an RBSP; reading past the end sets bad.
type bitReader struct {
	b   []byte
	pos int
	bad bool
}

func (r *bitReader) u(n int) uint64 { panic("not implemented") }

// ue reads an Exp-Golomb code of at most 32 bits.
func (r *bitReader) ue() int { panic("not implemented") }

// nalReader splits an Annex B stream into NAL units without reading it whole.
type nalReader struct {
	r    io.Reader
	buf  []byte
	s    int
	scan int
	eof  bool
}

var startCode = []byte{0, 0, 1}

// next returns the next NAL unit (a copy, without its start code or trailing zeros), or io.EOF.
func (n *nalReader) next() ([]byte, error) { panic("not implemented") }

// nalWorker runs the decoder on one goroutine, so that a call that takes too long can be abandoned,
// and recovers its panics.
type nalWorker struct {
	dec    nalDecoder
	closed bool
	in     chan hevc.NALUnit
	out    chan nalResult
}

type nalResult struct {
	pics []*hevc.Picture
	err  error
}

func startWorker(dec nalDecoder) *nalWorker { panic("not implemented") }

// call runs one decoder call inside the recover boundary.
func (w *nalWorker) call(nal hevc.NALUnit, flush bool) (r nalResult) { panic("not implemented") }

// do hands one call to the worker and waits until deadline.
func (w *nalWorker) do(nal hevc.NALUnit, flush bool, deadline time.Time) (nalResult, error) {
	panic("not implemented")
}

func pictureTimeError() error { panic("not implemented") }

func (d *fastDecoder) Next(f *Frame) error { panic("not implemented") }

// feed decodes the next NAL unit, or flushes at the end of the stream.
func (d *fastDecoder) feed() error { panic("not implemented") }

// emit converts a picture into f. It returns false for a picture outside the slice.
func (d *fastDecoder) emit(f *Frame, pic *hevc.Picture) (bool, error) { panic("not implemented") }

func cropPlane(dst []uint16, p8 []uint8, p16 []uint16, stride, x0, y0, w, h int) []uint16 {
	panic("not implemented")
}

// fallback hands the rest of the slice to the engine, unless err ends the decode: a limit, a
// cancellation, or a picture over the time limit (whose decoder goroutine is left running).
func (d *fastDecoder) fallback(err error) { panic("not implemented") }

// release stops the worker (when it is idle) and removes the copied stream.
func (d *fastDecoder) release() { panic("not implemented") }

func (d *fastDecoder) cleanup() { panic("not implemented") }

func (d *fastDecoder) Close() error { panic("not implemented") }
