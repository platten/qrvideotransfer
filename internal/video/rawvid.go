package video

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Raw is the raw-frame file backend, for tests: a 32-byte header followed by the frames' samples,
// uncompressed. Header: "QRVTRAW1", width and height (uint32), bit depth (uint8, 8 or 10), plane
// count (uint8, always 3), two reserved bytes, the frame rate as numerator and denominator (uint32),
// four reserved bytes; all little-endian. Each frame is its Y, Cb and Cr planes (4:2:0), one byte
// per sample at 8 bits and two at 10.
type Raw struct{}

// NewRaw returns the raw-frame backend.
func NewRaw() *Raw { return &Raw{} }

const rawMagic = "QRVTRAW1"
const rawHeaderLen = 32

type rawHeader struct {
	w, h, bits   int
	fpsN, fpsD   int
	frameBytes   int64
	frames, size int64
}

func (h rawHeader) info() ClipInfo {
	return ClipInfo{
		Codec: "rawvideo", Width: h.w, Height: h.h, BitDepth: h.bits, FPSNum: h.fpsN, FPSDen: h.fpsD,
		Frames: h.frames, Duration: frameTime(h.frames, h.fpsN, h.fpsD), Transfer: "bt709",
	}
}

func rawFrameBytes(w, h, bits int) int64 {
	n := int64(w)*int64(h) + 2*int64(w/2)*int64(h/2)
	if bits > 8 {
		n *= 2
	}
	return n
}

func encodeRawHeader(w, h, bits, fps int) []byte {
	b := make([]byte, rawHeaderLen)
	copy(b, rawMagic)
	binary.LittleEndian.PutUint32(b[8:], uint32(w))    //nolint:gosec // validated by the encoder
	binary.LittleEndian.PutUint32(b[12:], uint32(h))   //nolint:gosec // validated by the encoder
	b[16], b[17] = byte(bits), 3                       //nolint:gosec // 8 or 10
	binary.LittleEndian.PutUint32(b[20:], uint32(fps)) //nolint:gosec // validated by the encoder
	binary.LittleEndian.PutUint32(b[24:], 1)
	return b
}

// readRawHeader reads and validates the header of f, whose size is size.
func readRawHeader(f io.Reader, size int64) (rawHeader, error) {
	b := make([]byte, rawHeaderLen)
	if _, err := io.ReadFull(f, b); err != nil || string(b[:8]) != rawMagic {
		return rawHeader{}, fmt.Errorf("%w: not a raw-frame file", ErrInvalidVideo)
	}
	h := rawHeader{
		w: int(binary.LittleEndian.Uint32(b[8:])), h: int(binary.LittleEndian.Uint32(b[12:])),
		bits: int(b[16]), fpsN: int(binary.LittleEndian.Uint32(b[20:])), fpsD: int(binary.LittleEndian.Uint32(b[24:])),
		size: size,
	}
	if err := checkSize(h.w, h.h); err != nil {
		return rawHeader{}, err
	}
	if (h.bits != 8 && h.bits != 10) || b[17] != 3 || h.w%2 != 0 || h.h%2 != 0 || h.fpsN <= 0 || h.fpsD <= 0 || h.fpsN > 1000*h.fpsD {
		return rawHeader{}, fmt.Errorf("%w: bad raw-frame header", ErrInvalidVideo)
	}
	h.frameBytes = rawFrameBytes(h.w, h.h, h.bits)
	if (size-rawHeaderLen)%h.frameBytes != 0 {
		return rawHeader{}, fmt.Errorf("%w: raw-frame file ends inside a frame", ErrInvalidVideo)
	}
	h.frames = (size - rawHeaderLen) / h.frameBytes
	return h, nil
}

func openRaw(path string) (*os.File, rawHeader, error) {
	f, err := os.Open(path) //nolint:gosec // the caller names the file
	if err != nil {
		return nil, rawHeader{}, err
	}
	st, err := f.Stat()
	if err == nil {
		var h rawHeader
		if h, err = readRawHeader(f, st.Size()); err == nil {
			return f, h, nil
		}
	}
	_ = f.Close()
	return nil, rawHeader{}, err
}

// Probe implements [Backend].
func (*Raw) Probe(_ context.Context, path string) (ClipInfo, error) {
	f, h, err := openRaw(path)
	if err != nil {
		return ClipInfo{}, err
	}
	_ = f.Close()
	return h.info(), nil
}

// NewEncoder implements [Backend]. The configuration's quantiser is ignored.
func (*Raw) NewEncoder(_ context.Context, path string, c EncodeConfig) (Encoder, error) {
	if err := checkEncodeConfig(c); err != nil {
		return nil, err
	}
	f, err := os.Create(path) //nolint:gosec // the caller names the file
	if err != nil {
		return nil, err
	}
	bits := max(c.BitDepth, 8)
	w := bufio.NewWriter(f)
	_, _ = w.Write(encodeRawHeader(c.Width, c.Height, bits, c.FPS))
	return &rawEncoder{f: f, w: w, c: c, bits: bits}, nil
}

type rawEncoder struct {
	f    *os.File
	w    *bufio.Writer
	c    EncodeConfig
	bits int
	buf  []byte
}

func (e *rawEncoder) WriteFrame(fr *Frame) error {
	var err error
	if e.buf, err = packFrame(e.buf[:0], fr, e.c.Width, e.c.Height, e.bits); err != nil {
		return err
	}
	_, err = e.w.Write(e.buf)
	return err
}

func (e *rawEncoder) Close() error {
	err := e.w.Flush()
	if cerr := e.f.Close(); err == nil {
		err = cerr
	}
	return err
}

// NewDecoder implements [Backend].
func (*Raw) NewDecoder(_ context.Context, path string, c DecodeConfig) (Decoder, error) {
	f, h, err := openRaw(path)
	if err != nil {
		return nil, err
	}
	if c.BitDepth != 0 && c.BitDepth != h.bits {
		_ = f.Close()
		return nil, fmt.Errorf("%w: %d-bit output from a %d-bit source", ErrUnsupported, c.BitDepth, h.bits)
	}
	first, last := sliceFrames(c, h.fpsN, h.fpsD, h.frames)
	if _, err := f.Seek(rawHeaderLen+first*h.frameBytes, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &rawDecoder{f: bufio.NewReader(f), c: f, h: h, next: first, last: last, gray: c.BrightnessOnly,
		buf: make([]byte, h.frameBytes)}, nil
}

// sliceFrames returns the frame numbers [first, last) whose times fall in the time slice of c.
func sliceFrames(c DecodeConfig, fpsN, fpsD int, frames int64) (first, last int64) {
	at := func(t time.Duration) int64 { // first frame at or after t
		return (int64(t)*int64(fpsN) + int64(time.Second)*int64(fpsD) - 1) / (int64(time.Second) * int64(fpsD))
	}
	first, last = min(at(max(c.Start, 0)), frames), frames
	if c.Length > 0 {
		last = min(at(c.Start+c.Length), frames)
	}
	return first, max(first, last)
}

func frameTime(i int64, fpsN, fpsD int) time.Duration {
	if fpsN <= 0 {
		return 0
	}
	return time.Duration(i * int64(time.Second) * int64(fpsD) / int64(fpsN))
}

type rawDecoder struct {
	f          *bufio.Reader
	c          io.Closer
	h          rawHeader
	next, last int64
	gray       bool
	buf        []byte
}

func (d *rawDecoder) Info() ClipInfo { return d.h.info() }

func (d *rawDecoder) Next(fr *Frame) error {
	if d.next >= d.last {
		return io.EOF
	}
	if _, err := io.ReadFull(d.f, d.buf); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidVideo, err)
	}
	unpackFrame(fr, d.buf, d.h.w, d.h.h, d.h.bits, d.gray)
	fr.Index, fr.PTS = int(d.next), frameTime(d.next, d.h.fpsN, d.h.fpsD)
	d.next++
	return nil
}

func (d *rawDecoder) Close() error { return d.c.Close() }

// Concat joins raw-frame files with identical headers into one file at path.
func (*Raw) Concat(_ context.Context, runs []string, path string, fps int) error {
	var hdr []byte
	out, err := os.Create(path) //nolint:gosec // the caller names the file
	if err != nil {
		return err
	}
	for _, r := range runs {
		f, h, err := openRaw(r)
		if err != nil {
			_ = out.Close()
			return err
		}
		want := encodeRawHeader(h.w, h.h, h.bits, fps)
		if hdr == nil {
			hdr = want
			_, err = out.Write(hdr)
		} else if !bytes.Equal(hdr, want) {
			err = fmt.Errorf("%w: %s has a different format", ErrFrame, filepath.Base(r))
		}
		if err == nil {
			_, err = io.Copy(out, f)
		}
		_ = f.Close()
		if err != nil {
			_ = out.Close()
			return err
		}
	}
	return out.Close()
}

// checkEncodeConfig validates an encoder configuration.
func checkEncodeConfig(c EncodeConfig) error {
	if err := checkSize(c.Width, c.Height); err != nil {
		return err
	}
	if c.Width%2 != 0 || c.Height%2 != 0 || c.FPS <= 0 || c.FPS > 1000 || c.Quantiser < 0 || c.Quantiser > 51 ||
		(c.BitDepth != 0 && c.BitDepth != 8 && c.BitDepth != 10) {
		return fmt.Errorf("%w: encoder configuration %+v", ErrUnsupported, c)
	}
	return nil
}

// packFrame appends the planes of f as raw samples (yuv420p, or yuv420p10le at 10 bits).
func packFrame(dst []byte, f *Frame, w, h, bits int) ([]byte, error) {
	cw, ch := w/2, h/2
	if f == nil || f.W != w || f.H != h || len(f.Y) != w*h || len(f.Cb) != cw*ch || len(f.Cr) != cw*ch ||
		(f.Bits != 0 && int(f.Bits) != bits) {
		return dst, fmt.Errorf("%w: want %dx%d, %d bits, three planes", ErrFrame, w, h, bits)
	}
	limit := uint16(1)<<bits - 1
	for _, p := range [][]uint16{f.Y, f.Cb, f.Cr} {
		for _, v := range p {
			if v > limit {
				return dst, fmt.Errorf("%w: sample %d above %d bits", ErrFrame, v, bits)
			}
			if bits > 8 {
				dst = append(dst, byte(v), byte(v>>8)) //nolint:gosec // little-endian split
			} else {
				dst = append(dst, byte(v)) //nolint:gosec // checked against the limit above
			}
		}
	}
	return dst, nil
}

// unpackFrame fills f from raw samples: Y, then Cb and Cr unless gray (b may then hold only Y).
func unpackFrame(f *Frame, b []byte, w, h, bits int, gray bool) {
	f.W, f.H, f.Bits = w, h, uint8(bits) //nolint:gosec // 8 or 10
	n := w * h
	c := (w / 2) * (h / 2)
	f.Y = unpackPlane(f.Y, b, n, bits)
	if gray {
		f.Cb, f.Cr = nil, nil
		return
	}
	s := 1
	if bits > 8 {
		s = 2
	}
	f.Cb = unpackPlane(f.Cb, b[n*s:], c, bits)
	f.Cr = unpackPlane(f.Cr, b[(n+c)*s:], c, bits)
}

func unpackPlane(dst []uint16, b []byte, n, bits int) []uint16 {
	if cap(dst) < n {
		dst = make([]uint16, n)
	}
	dst = dst[:n]
	if bits > 8 {
		for i := range dst {
			dst[i] = (uint16(b[2*i]) | uint16(b[2*i+1])<<8) & 0x3ff
		}
		return dst
	}
	for i := range dst {
		dst[i] = uint16(b[i])
	}
	return dst
}
