//go:build unsandboxedhevc

package video

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gen2brain/h265/hevc"
)

// FastPathBuilt reports whether this build contains the HEVC fast path (build tag unsandboxedhevc).
// It stays out of release builds until the decoder can be built without its hand-written assembly
// (DESIGN.md section 12); build it with -tags unsandboxedhevc,noasm.
const FastPathBuilt = true

// The fast path's limits (docs/SECURITY.md section 5). They reduce risk; they do not bound memory.
const (
	// fastMaxPixels is the largest picture, in luma samples, the fast path decodes (7680 x 4320).
	fastMaxPixels = 7680 * 4320
	// fastMaxDPB is the largest decoded picture buffer, sps_max_dec_pic_buffering_minus1 + 1, of any
	// sub-layer (HEVC Annex A.4.2 maxDpbPicBuf for pictures above three quarters of MaxLumaPs).
	fastMaxDPB = 6
	// fastMaxNAL bounds one NAL unit read from the copied stream.
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
var newNALDecoder = func(threads int) (nalDecoder, error) { return newH265(threads) }

// newH265 returns a gen2brain/h265 decoder with exactly one thread: with any other value, 0 (which
// means GOMAXPROCS) included, it starts goroutines of its own whose panics no caller can recover.
func newH265(threads int) (*hevc.Decoder, error) {
	if threads != 1 {
		return nil, fmt.Errorf("%w: the HEVC fast path runs with exactly one thread, not %d", ErrUnsupported, threads)
	}
	d := &hevc.Decoder{}
	d.Threads(1)
	d.FrameSizeLimit(fastMaxPixels)
	return d, nil
}

// errRefused marks a stream the fast path hands to the engine.
var errRefused = errors.New("video: refused by the HEVC fast path")

// newFastDecoder decodes an HEVC slice with gen2brain/h265 in this process, outside the sandbox. A
// separate engine instance first copies the slice out of its container (input read-only, one
// writable temporary directory) and finishes; then every sequence parameter set is checked; then the
// stream is decoded. On any error, panic or refusal the rest of the slice is decoded by the engine.
func newFastDecoder(ctx context.Context, b *ff, path string, p probeInfo, c DecodeConfig) (Decoder, error) {
	pix, err := outputFormat(p, c)
	if err != nil {
		return nil, err
	}
	d := &fastDecoder{ctx: ctx, b: b, path: path, p: p, c: c, pix: pix, last: -1}
	if err := d.start(); err != nil {
		var le *LimitError
		if ctx.Err() != nil || errors.As(err, &le) {
			d.cleanup()
			return nil, err
		}
		d.fallback(err)
	}
	return d, nil
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
	pts      []int64 // presentation timestamps of the access units, in decode order
	tbN      int64
	tbD      int64
	au       uint64 // access units seen
	queue    []*hevc.Picture
	ended    bool
	pixels   float64
	last     time.Duration // PTS of the last frame returned
	engine   Decoder       // set after a fallback
	fellBack error         // why the engine took over
	fail     error
}

func (d *fastDecoder) Info() ClipInfo { return d.p.ClipInfo }

// start copies the slice out of its container and checks its parameter sets.
func (d *fastDecoder) start() error {
	var err error
	if d.tmp, err = os.MkdirTemp("", "qrvt-hevc-"); err != nil {
		return err
	}
	if err := run(d.ctx, d.b.r, job{prog: "ffmpeg", input: d.path, outDir: d.tmp, progress: progFiles,
		args: func(in, out string) []string { return copyArgs(in, out, d.p, d.c) }}); err != nil {
		return err
	}
	if err := d.readTimestamps(filepath.Join(d.tmp, "s.txt")); err != nil {
		return err
	}
	if err := d.openStream(); err != nil {
		return err
	}
	if err := checkStream(d.nals, d.p); err != nil {
		return err
	}
	_ = d.f.Close()
	if err := d.openStream(); err != nil {
		return err
	}
	dec, err := newNALDecoder(1)
	if err != nil {
		return err
	}
	d.w = startWorker(dec)
	return nil
}

func (d *fastDecoder) openStream() error {
	f, err := os.Open(filepath.Join(d.tmp, "s.hevc"))
	if err != nil {
		return err
	}
	d.f, d.nals = f, &nalReader{r: f}
	return nil
}

// copyArgs copies the slice, from a preroll before it, as an Annex B stream, and writes each
// packet's timestamps (framemd5) beside it.
func copyArgs(in, out string, p probeInfo, c DecodeConfig) []string {
	a := []string{"-nostdin", "-hide_banner", "-nostats", "-loglevel", "error", "-copyts"}
	if seek := c.Start - prerollTime; seek > 0 {
		a = append(a, "-noaccurate_seek", "-ss", strconv.FormatFloat(seek.Seconds(), 'f', 6, 64))
	}
	a = append(a, "-i", in)
	var to []string
	if c.Length > 0 {
		end := p.Start + (c.Start + c.Length + prerollTime).Seconds()
		to = []string{"-to", strconv.FormatFloat(end, 'f', 6, 64)}
	}
	a = append(a, "-map", "0:v:0", "-c", "copy")
	a = append(a, to...)
	a = append(a, "-bsf:v", "hevc_mp4toannexb", "-f", "hevc", "-y", out+"/s.hevc", "-map", "0:v:0", "-c", "copy")
	a = append(a, to...)
	return append(a, "-f", "framemd5", "-y", out+"/s.txt")
}

// readTimestamps reads the packets' presentation timestamps from the framemd5 file.
func (d *fastDecoder) readTimestamps(name string) error {
	f, err := os.Open(name) //nolint:gosec // in our temporary directory
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	s := bufio.NewScanner(f)
	for s.Scan() {
		l := s.Text()
		if v, ok := strings.CutPrefix(l, "#tb 0: "); ok {
			a, b, _ := strings.Cut(strings.TrimSpace(v), "/")
			d.tbN, _ = strconv.ParseInt(a, 10, 64)
			d.tbD, _ = strconv.ParseInt(b, 10, 64)
			continue
		}
		f := strings.Split(l, ",")
		if strings.HasPrefix(l, "#") || len(f) < 3 {
			continue
		}
		pts, err := strconv.ParseInt(strings.TrimSpace(f[2]), 10, 64)
		if err != nil || len(d.pts) >= 1<<24 {
			return fmt.Errorf("%w: packet timestamps", errRefused)
		}
		d.pts = append(d.pts, pts)
	}
	if d.tbN <= 0 || d.tbD <= 0 || len(d.pts) == 0 {
		return fmt.Errorf("%w: no packet timestamps", errRefused)
	}
	return s.Err()
}

// checkStream reads every NAL unit and refuses the stream unless every sequence parameter set
// declares the probed size, 4:2:0, 8 or 10 bits as probed, at most fastMaxPixels and a buffer of at
// most fastMaxDPB pictures in every sub-layer.
func checkStream(r *nalReader, p probeInfo) error {
	n := 0
	for {
		b, err := r.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		nal, ok := hevc.ParseNAL(b)
		if !ok || nal.Type != hevc.NALSPS {
			continue
		}
		n++
		if err := checkSPS(nal.RBSP, p); err != nil {
			return err
		}
	}
	if n == 0 {
		return fmt.Errorf("%w: no sequence parameter set", errRefused)
	}
	return nil
}

// checkSPS applies the fast path's parameter-set rules to one SPS.
func checkSPS(rbsp []byte, p probeInfo) error {
	chroma, depth, depthC, ok := hevc.SPSFormat(rbsp)
	if !ok || chroma != 1 || depth != p.BitDepth || depthC != depth {
		return fmt.Errorf("%w: format %d, %d/%d bits", errRefused, chroma, depth, depthC)
	}
	w, h, dpb, err := parseSPSLimits(rbsp)
	if err != nil {
		return err
	}
	if w*h > fastMaxPixels || dpb > fastMaxDPB {
		return fmt.Errorf("%w: %dx%d with a buffer of %d pictures", errRefused, w, h, dpb)
	}
	return nil
}

// parseSPSLimits reads the coded size and the largest sps_max_dec_pic_buffering_minus1 + 1 over all
// sub-layers (the decoder keeps only the last sub-layer's value).
func parseSPSLimits(rbsp []byte) (w, h, dpb int, err error) {
	r := bitReader{b: rbsp}
	r.u(4)
	maxSub := int(r.u(3)) //nolint:gosec // three bits
	r.u(1)
	// profile_tier_level(1, maxSub)
	r.u(88)
	r.u(8)
	prof, lvl := make([]bool, maxSub), make([]bool, maxSub)
	for i := range maxSub {
		prof[i], lvl[i] = r.u(1) == 1, r.u(1) == 1
	}
	if maxSub > 0 {
		for range 8 - maxSub {
			r.u(2)
		}
	}
	for i := range maxSub {
		if prof[i] {
			r.u(88)
		}
		if lvl[i] {
			r.u(8)
		}
	}
	r.ue() // sps id
	if r.ue() == 3 {
		r.u(1)
	}
	w, h = r.ue(), r.ue()
	if r.u(1) == 1 {
		r.ue()
		r.ue()
		r.ue()
		r.ue()
	}
	r.ue()
	r.ue()
	r.ue()
	first := maxSub
	if r.u(1) == 1 {
		first = 0
	}
	for range maxSub - first + 1 {
		dpb = max(dpb, r.ue()+1)
		r.ue()
		r.ue()
	}
	if r.bad || w <= 0 || h <= 0 || w > MaxCaptureWidth || h > MaxCaptureHeight {
		return 0, 0, 0, fmt.Errorf("%w: unreadable sequence parameter set", errRefused)
	}
	return w, h, dpb, nil
}

// bitReader reads an RBSP; reading past the end sets bad.
type bitReader struct {
	b   []byte
	pos int
	bad bool
}

func (r *bitReader) u(n int) uint64 {
	var v uint64
	for range n {
		if r.pos >= 8*len(r.b) {
			r.bad = true
			return 0
		}
		v = v<<1 | uint64(r.b[r.pos/8]>>(7-r.pos%8)&1)
		r.pos++
	}
	return v
}

// ue reads an Exp-Golomb code of at most 32 bits.
func (r *bitReader) ue() int {
	n := 0
	for r.u(1) == 0 {
		if n++; n > 31 || r.bad {
			r.bad = true
			return 0
		}
	}
	return int(1<<n - 1 + r.u(n)) //nolint:gosec // below 2^32
}

// nalReader splits an Annex B stream into NAL units without reading it whole.
type nalReader struct {
	r    io.Reader
	buf  []byte
	s    int // start of the current NAL unit in buf
	scan int // where to look for the next start code
	eof  bool
}

var startCode = []byte{0, 0, 1}

// next returns the next NAL unit (a copy, without its start code or trailing zeros), or io.EOF.
func (n *nalReader) next() ([]byte, error) {
	for {
		if i := bytes.Index(n.buf[n.scan:], startCode); i >= 0 {
			end := n.scan + i
			b := bytes.TrimRight(n.buf[n.s:end], "\x00")
			n.s, n.scan = end+3, end+3
			if len(b) > 0 {
				return bytes.Clone(b), nil
			}
			continue
		}
		if n.eof {
			b := bytes.TrimRight(n.buf[n.s:], "\x00")
			n.s, n.scan = len(n.buf), len(n.buf)
			if len(b) > 0 {
				return bytes.Clone(b), nil
			}
			return nil, io.EOF
		}
		if len(n.buf)-n.s > fastMaxNAL {
			return nil, fmt.Errorf("%w: a NAL unit over %d bytes", errRefused, fastMaxNAL)
		}
		k := copy(n.buf, n.buf[n.s:])
		n.buf, n.scan, n.s = n.buf[:k], max(0, k-2), 0
		if cap(n.buf)-k < 1<<20 {
			n.buf = append(make([]byte, 0, 2*cap(n.buf)+1<<20), n.buf...)
		}
		m, err := n.r.Read(n.buf[k:cap(n.buf)])
		n.buf = n.buf[:k+m]
		if err == io.EOF {
			n.eof = true
		} else if err != nil {
			return nil, err
		}
	}
}

// nalWorker runs the decoder on one goroutine, so that a call that takes too long can be abandoned,
// and recovers its panics.
type nalWorker struct {
	dec    nalDecoder
	closed bool
	in     chan hevc.NALUnit // a NAL unit to decode; closed to flush
	out    chan nalResult
}

type nalResult struct {
	pics []*hevc.Picture
	err  error
}

func startWorker(dec nalDecoder) *nalWorker {
	w := &nalWorker{dec: dec, in: make(chan hevc.NALUnit), out: make(chan nalResult)}
	go func() {
		for {
			nal, ok := <-w.in
			w.out <- w.call(nal, !ok)
			if !ok {
				return
			}
		}
	}()
	return w
}

// call runs one decoder call inside the recover boundary.
func (w *nalWorker) call(nal hevc.NALUnit, flush bool) (r nalResult) {
	defer func() {
		if p := recover(); p != nil {
			r = nalResult{err: fmt.Errorf("%w: decoder panic: %v", errRefused, p)}
		}
	}()
	if flush {
		return nalResult{pics: w.dec.Flush()}
	}
	pics, err := w.dec.DecodeNAL(nal)
	return nalResult{pics: pics, err: err}
}

// do hands one call to the worker and waits until deadline.
func (w *nalWorker) do(nal hevc.NALUnit, flush bool, deadline time.Time) (nalResult, error) {
	t := time.NewTimer(time.Until(deadline))
	defer t.Stop()
	if flush {
		w.closed = true
		close(w.in)
	} else {
		select {
		case w.in <- nal:
		case <-t.C:
			return nalResult{}, pictureTimeError()
		}
	}
	select {
	case r := <-w.out:
		return r, nil
	case <-t.C:
		return nalResult{}, pictureTimeError()
	}
}

func pictureTimeError() error {
	return &LimitError{Limit: "FastPathPictureTime", Value: pictureTime.Seconds(), Max: pictureTime.Seconds(),
		Hint: "a picture took longer than this on the HEVC fast path; its decoder cannot be stopped, so the whole decode stops"}
}

func (d *fastDecoder) Next(f *Frame) error {
	for d.fail == nil {
		if d.engine != nil {
			return d.engine.Next(f)
		}
		if len(d.queue) > 0 {
			pic := d.queue[0]
			d.queue = d.queue[1:]
			ok, err := d.emit(f, pic)
			pic.Release()
			if err != nil {
				d.fallback(err)
				continue
			}
			if ok {
				return nil
			}
			continue
		}
		if d.ended {
			d.fail = io.EOF
			break
		}
		if err := d.feed(); err != nil {
			d.fallback(err)
		}
	}
	return d.fail
}

// feed decodes the next NAL unit, or flushes at the end of the stream.
func (d *fastDecoder) feed() error {
	b, err := d.nals.next()
	flush := errors.Is(err, io.EOF)
	if err != nil && !flush {
		return err
	}
	var nal hevc.NALUnit
	if !flush {
		var ok bool
		if nal, ok = hevc.ParseNAL(b); !ok {
			return nil
		}
		if nal.Type.IsVCL() && len(nal.RBSP) > 0 && nal.RBSP[0]&0x80 != 0 {
			d.au++
		}
		nal.Tag = d.au - 1
	}
	r, err := d.w.do(nal, flush, time.Now().Add(pictureTime))
	if err != nil {
		return err
	}
	d.queue = append(d.queue, r.pics...)
	d.ended = flush
	return r.err
}

// emit converts a picture into f. It returns false for a picture outside the slice.
func (d *fastDecoder) emit(f *Frame, pic *hevc.Picture) (bool, error) {
	if pic.CropW != d.p.Width || pic.CropH != d.p.Height || pic.BitDepth != d.p.BitDepth || pic.ChromaFormat != 1 || pic.Tag >= uint64(len(d.pts)) {
		return false, fmt.Errorf("%w: picture %dx%d at %d bits, tag %d", errRefused, pic.CropW, pic.CropH, pic.BitDepth, pic.Tag)
	}
	d.pixels += float64(pic.CropW) * float64(pic.CropH)
	if err := checkWork(d.pixels, d.p.Size); err != nil {
		return false, err
	}
	pts := frameTS{pts: d.pts[pic.Tag], tbN: d.tbN, tbD: d.tbD}.at() - time.Duration(d.p.Start*float64(time.Second))
	if pts < d.c.Start || pts <= d.last {
		return false, nil
	}
	if d.c.Length > 0 && pts >= d.c.Start+d.c.Length {
		d.ended, d.queue = true, nil
		return false, nil
	}
	w, h := d.p.Width, d.p.Height
	f.W, f.H, f.Bits = w, h, uint8(d.p.BitDepth) //nolint:gosec // 8 or 10
	f.Y = cropPlane(f.Y, pic.Y, pic.Y16, pic.StrideY, pic.CropX, pic.CropY, w, h)
	if d.c.BrightnessOnly {
		f.Cb, f.Cr = nil, nil
	} else {
		f.Cb = cropPlane(f.Cb, pic.Cb, pic.Cb16, pic.StrideC, pic.CropX/2, pic.CropY/2, w/2, h/2)
		f.Cr = cropPlane(f.Cr, pic.Cr, pic.Cr16, pic.StrideC, pic.CropX/2, pic.CropY/2, w/2, h/2)
	}
	f.PTS, f.Index, d.last = pts, frameIndex(pts, d.p.FPSNum, d.p.FPSDen), pts
	return true, nil
}

func cropPlane(dst []uint16, p8 []uint8, p16 []uint16, stride, x0, y0, w, h int) []uint16 {
	if cap(dst) < w*h {
		dst = make([]uint16, w*h)
	}
	dst = dst[:w*h]
	for y := range h {
		o := (y0+y)*stride + x0
		if p16 != nil {
			copy(dst[y*w:(y+1)*w], p16[o:o+w])
			continue
		}
		for x, v := range p8[o : o+w] {
			dst[y*w+x] = uint16(v)
		}
	}
	return dst
}

// fallback hands the rest of the slice to the engine, unless err ends the decode: a limit, a
// cancellation, or a picture over the time limit (whose decoder goroutine is left running).
func (d *fastDecoder) fallback(err error) {
	var le *LimitError
	if errors.As(err, &le) || d.ctx.Err() != nil {
		d.fail = err
		if d.ctx.Err() != nil {
			d.fail = d.ctx.Err()
		}
		return
	}
	d.fellBack = err
	d.release()
	c := d.c
	if d.last >= 0 { // continue after the last frame returned
		c.Start = d.last + time.Nanosecond
		if d.c.Length > 0 {
			c.Length = d.c.Start + d.c.Length - c.Start
		}
	}
	d.engine = startDecoder(d.ctx, d.b.r, d.path, d.p, c, d.pix)
}

// release stops the worker (when it is idle) and removes the copied stream.
func (d *fastDecoder) release() {
	if d.w != nil && !d.w.closed && d.fail == nil {
		_, _ = d.w.do(hevc.NALUnit{}, true, time.Now().Add(pictureTime))
	}
	d.w = nil
	for _, p := range d.queue {
		p.Release()
	}
	d.queue = nil
	d.cleanup()
}

func (d *fastDecoder) cleanup() {
	if d.f != nil {
		_ = d.f.Close()
	}
	if d.tmp != "" {
		_ = os.RemoveAll(d.tmp)
	}
}

func (d *fastDecoder) Close() error {
	if d.engine != nil {
		_ = d.engine.Close()
	}
	d.release()
	if d.fail == nil {
		d.fail = io.ErrClosedPipe
	}
	return nil
}
