package pipeline

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/platten/qrvideotransfer/internal/fec"
	"github.com/platten/qrvideotransfer/internal/fec/ldpc"
	"github.com/platten/qrvideotransfer/internal/fec/rs"
	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/payload"
	"github.com/platten/qrvideotransfer/internal/render"
	"github.com/platten/qrvideotransfer/internal/video"
)

// displayBits is the simulated display bits per cell of each modulation (DESIGN.md section 5),
// which a dry run uses to estimate the video's size.
var displayBits = map[uint8]float64{1: 7.3, 2: 8.4, 3: 28}

// concatenator joins runs of frames into the final video; every backend of internal/video has it.
type concatenator interface {
	Concat(ctx context.Context, runs []string, path string, fps int) error
}

// check validates the configuration before anything is read or written.
func (c *EncodeConfig) check() error {
	switch {
	case len(c.Inputs) == 0:
		return errors.New("pipeline: no inputs")
	case c.Output == "":
		return errors.New("pipeline: no output")
	case c.Backend == nil:
		return errors.New("pipeline: no video backend")
	case c.Cell != 4 && c.Cell != 8 && c.Cell != 12 && c.Cell != 16:
		return fmt.Errorf("pipeline: cell %d is not 4, 8, 12 or 16", c.Cell)
	case c.Width <= 0 || c.Height <= 0 || c.Width%c.Cell != 0 || c.Height%c.Cell != 0:
		return fmt.Errorf("pipeline: %d x %d is not a multiple of the %d-pixel cell", c.Width, c.Height, c.Cell)
	case c.Height < render.MinHeight:
		return fmt.Errorf("pipeline: height %d below %d", c.Height, render.MinHeight)
	case c.FPS < 1 || c.FPS > 240:
		return fmt.Errorf("pipeline: display rate %d is not 1 to 240", c.FPS)
	case displayBits[c.Modulation] == 0:
		return fmt.Errorf("pipeline: modulation %d is not supported", c.Modulation)
	case c.Parity < 5 || c.Parity > 100:
		return fmt.Errorf("pipeline: parity %d%% is not 5 to 100", c.Parity)
	}
	if _, err := frame.LookupInner(c.Inner); err != nil {
		return err
	}
	if err := frame.CheckGrid(c.Width/c.Cell, c.Height/c.Cell); err != nil {
		return err
	}
	if st, err := os.Stat(filepath.Dir(c.Output)); err != nil || !st.IsDir() {
		return fmt.Errorf("pipeline: the output's folder %s is not a folder", filepath.Dir(c.Output))
	}
	return nil
}

// Encode packs the inputs, protects the stream and writes the code video (DESIGN.md sections 8, 9
// and 12). Temporary files go in a folder of their own under TempDir, removed on return; the video
// is written beside Output under a temporary name and renamed into place only when complete.
func Encode(ctx context.Context, c EncodeConfig) (*EncodeResult, error) {
	if err := c.check(); err != nil {
		return nil, err
	}
	emit := emitter(c.Progress)
	tmp, err := os.MkdirTemp(c.TempDir, "qrvt-encode-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	emit.stage(StagePack, KindStarted, 0, 0, UnitBytes)
	spool, err := os.Create(filepath.Join(tmp, "stream")) //nolint:gosec // our own temporary folder
	if err != nil {
		return nil, err
	}
	defer func() { _ = spool.Close() }()
	packed, err := payload.Pack(ctx, spool, payload.PackOptions{Inputs: c.Inputs, Compression: c.Compression,
		Passphrase: c.Passphrase, SignKey: c.SignKey,
		Progress: func(done, total int64) { emit.stage(StagePack, KindProgress, done, total, UnitBytes) }})
	if err != nil {
		return nil, err
	}
	emit.stage(StagePack, KindFinished, packed.StreamLen, packed.StreamLen, UnitBytes)

	e, err := newEncoding(&c, packed)
	if err != nil {
		return nil, err
	}
	res := &EncodeResult{Session: e.desc.Session, Files: packed.Files, ArchiveLen: packed.Header.ArchiveLen, StreamLen: packed.StreamLen,
		Blocks: e.plan.Blocks, K: e.plan.K, M: e.plan.M, Codewords: e.cpf, Frames: e.desc.Frames}
	for _, w := range packed.Warnings {
		res.Warnings = append(res.Warnings, Warning{Code: string(w.Code), Text: w.Text})
	}
	if c.DryRun {
		cells := len(e.render.Layout().DataCells())
		res.VideoBytes = int64(float64(e.desc.Frames) * float64(cells) * displayBits[c.Modulation] / 8)
		return res, nil
	}
	parity, err := os.Create(filepath.Join(tmp, "parity")) //nolint:gosec // our own temporary folder
	if err != nil {
		return nil, err
	}
	defer func() { _ = parity.Close() }()
	if err := e.protect(ctx, emit, spool, parity); err != nil {
		return nil, err
	}
	if res.VideoBytes, err = e.writeVideo(ctx, emit, spool, parity, tmp); err != nil {
		return nil, err
	}
	return res, nil
}

// encoding is everything the encode's workers share, read-only once built.
type encoding struct {
	c       *EncodeConfig
	header  frame.Header // with FrameIndex 0
	plan    fec.Plan
	desc    frame.Descriptor
	descCW  []byte // the descriptor's codeword, codeword 0 of every frame
	cpf     int    // C
	mapping *frame.Mapping
	render  *render.Renderer
}

// newEncoding draws the session and the outer order seed, and derives the plan and descriptor.
func newEncoding(c *EncodeConfig, packed *payload.PackResult) (*encoding, error) {
	var rnd [12]byte
	src := c.Random
	if src == nil {
		src = rand.Reader
	}
	if _, err := io.ReadFull(src, rnd[:]); err != nil {
		return nil, err
	}
	session, seed := binary.BigEndian.Uint64(rnd[:8]), binary.BigEndian.Uint32(rnd[8:])
	gx, gy := c.Width/c.Cell, c.Height/c.Cell
	h := frame.Header{Version: frame.Version, Session: uint16(session), TotalFrames: 4, //nolint:gosec // low 16 bits on purpose
		GridX: uint16(gx), GridY: uint16(gy), Modulation: c.Modulation, InnerCode: c.Inner} //nolint:gosec // grid checked
	m, err := frame.NewMapping(h)
	if err != nil {
		return nil, err
	}
	r, err := render.New(gx, gy, c.Cell, c.Modulation)
	if err != nil {
		return nil, err
	}
	inner, _ := frame.LookupInner(c.Inner) // checked
	_, k, err := fec.Shape(packed.StreamLen, inner.ShardSize)
	if err != nil {
		return nil, err
	}
	plan, err := fec.NewPlan(packed.StreamLen, inner.ShardSize, fec.DefaultM(k, c.Parity), seed)
	if err != nil {
		return nil, err
	}
	frames, err := plan.CheckFrames(m.CodewordsPerFrame())
	if err != nil {
		return nil, err
	}
	h.TotalFrames = uint32(frames) //nolint:gosec // at most fec.MaxFrames
	d := frame.Descriptor{Session: session, StreamLen: packed.StreamLen, ShardSize: inner.ShardSize, K: plan.K, M: plan.M,
		Blocks: plan.Blocks, Frames: frames, CodewordsPerFrame: m.CodewordsPerFrame(), OrderSeed: seed,
		DisplayFPS: uint8(min(c.FPS, 255))} //nolint:gosec // clamped
	copy(d.StreamHash[:], packed.StreamHash[:16])
	code, _ := ldpc.New(c.Inner)
	shard := make([]byte, inner.ShardSize)
	b := d.Marshal()
	copy(shard, b[:])
	cw := make([]byte, (code.N()+7)/8)
	code.Encode(cw, shard)
	return &encoding{c: c, header: h, plan: plan, desc: d, descCW: cw, cpf: m.CodewordsPerFrame(), mapping: m, render: r}, nil
}

// protect computes every block's parity shards (FORMAT.md section 11) into the parity file:
// parity shard j of block b at offset (b M + j) S. Data shards are read from the spool.
func (e *encoding) protect(ctx context.Context, emit emitter, spool, parity *os.File) error {
	p := e.plan
	emit.stage(StageProtect, KindStarted, 0, int64(p.Blocks), UnitBlocks)
	codec, err := rs.NewCodec(p.K, p.M)
	if err != nil {
		return err
	}
	shards := make([][]byte, p.K+p.M)
	for i := range shards {
		shards[i] = make([]byte, p.ShardSize)
	}
	for b := range p.Blocks {
		if err := ctx.Err(); err != nil {
			return err
		}
		for i := range p.K {
			if err := readShard(spool, shards[i], p.DataOffset(b, i)); err != nil {
				return err
			}
		}
		if err := codec.Encode(shards); err != nil {
			return err
		}
		for j := range p.M {
			if _, err := parity.WriteAt(shards[p.K+j], (int64(b)*int64(p.M)+int64(j))*int64(p.ShardSize)); err != nil {
				return err
			}
		}
		emit.stage(StageProtect, KindProgress, int64(b+1), int64(p.Blocks), UnitBlocks)
	}
	emit.stage(StageProtect, KindFinished, int64(p.Blocks), int64(p.Blocks), UnitBlocks)
	return nil
}

// readShard reads the shard at off from r, zero beyond the end.
func readShard(r io.ReaderAt, dst []byte, off int64) error {
	n, err := r.ReadAt(dst, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	clear(dst[n:])
	return nil
}

// writeVideo renders and encodes every frame and returns the video's size. Workers each encode a
// run of an even number of frames to a file of their own (DESIGN.md section 12); the runs are then
// joined into the video, written beside the output and renamed into place.
func (e *encoding) writeVideo(ctx context.Context, emit emitter, spool, parity *os.File, tmp string) (int64, error) {
	c := e.c
	join, ok := c.Backend.(concatenator)
	if !ok {
		return 0, errors.New("pipeline: the video backend cannot join runs")
	}
	frames := e.desc.Frames
	workers := c.Workers
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	workers = max(1, min(workers, frames/2))
	runLen := 2 * ((frames + 2*workers - 1) / (2 * workers))
	ext := ".h264"
	if _, raw := c.Backend.(*video.Raw); raw {
		ext = ".raw"
	}
	emit.stage(StageRender, KindStarted, 0, int64(frames), UnitFrames)
	emit.stage(StageEncodeVideo, KindStarted, 0, int64(frames), UnitFrames)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error)
	var runs []string
	var wg sync.WaitGroup
	for first := 0; first < frames; first += runLen {
		path := filepath.Join(tmp, fmt.Sprintf("run-%06d%s", len(runs), ext))
		runs = append(runs, path)
		wg.Add(1)
		go func(first, last int) {
			defer wg.Done()
			if err := e.encodeRun(ctx, path, first, last, spool, parity, done); err != nil {
				done <- err
			}
		}(first, min(first+runLen, frames))
	}
	go func() { wg.Wait(); close(done) }()
	var firstErr error
	n := 0
	for err := range done {
		if err != nil {
			if firstErr == nil {
				firstErr = err
				cancel()
			}
			continue
		}
		n++
		emit.stage(StageRender, KindProgress, int64(n), int64(frames), UnitFrames)
		emit.stage(StageEncodeVideo, KindProgress, int64(n), int64(frames), UnitFrames)
	}
	if firstErr == nil {
		firstErr = ctx.Err()
	}
	if firstErr != nil {
		return 0, firstErr
	}
	emit.stage(StageRender, KindFinished, int64(frames), int64(frames), UnitFrames)
	part := filepath.Join(filepath.Dir(c.Output), "."+filepath.Base(tmp)+".part")
	defer func() { _ = os.Remove(part) }()
	if err := join.Concat(ctx, runs, part, c.FPS); err != nil {
		return 0, err
	}
	if err := os.Rename(part, c.Output); err != nil {
		return 0, err
	}
	st, err := os.Stat(c.Output)
	if err != nil {
		return 0, err
	}
	emit.stage(StageEncodeVideo, KindFinished, int64(frames), int64(frames), UnitFrames)
	return st.Size(), nil
}

// encodeRun renders frames first..last-1 and encodes them to path, sending nil on done for each
// frame written. It owns its encoder, inner code, mapper and frame.
func (e *encoding) encodeRun(ctx context.Context, path string, first, last int, spool, parity *os.File, done chan<- error) (err error) {
	c := e.c
	enc, err := c.Backend.NewEncoder(ctx, path, video.EncodeConfig{Width: c.Width, Height: c.Height, FPS: c.FPS,
		Quantiser: video.DefaultQuantiser, BitDepth: 8})
	if err != nil {
		return err
	}
	defer func() {
		if cerr := enc.Close(); err == nil {
			err = cerr
		}
	}()
	code, _ := ldpc.New(c.Inner) // checked
	mapper := render.NewMapper(e.mapping)
	p := e.plan
	cws := make([][]byte, e.cpf)
	cws[0] = e.descCW
	for i := 1; i < e.cpf; i++ {
		cws[i] = make([]byte, len(e.descCW))
	}
	shard := make([]byte, p.ShardSize)
	symbols := make([]uint8, len(e.render.Layout().DataCells()))
	w, h := e.render.Size()
	f := &video.Frame{Y: make([]uint16, w*h), Cb: make([]uint16, w*h/4), Cr: make([]uint16, w*h/4)}
	hdr := e.header
	for fi := first; fi < last; fi++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		for cw := 1; cw < e.cpf; cw++ {
			b, s := p.Locate(p.Packet(fi, cw, e.cpf))
			src, off := spool, p.DataOffset(b, s)
			if s >= p.K {
				src, off = parity, (int64(b)*int64(p.M)+int64(s-p.K))*int64(p.ShardSize)
			}
			if err := readShard(src, shard, off); err != nil {
				return err
			}
			code.Encode(cws[cw], shard)
		}
		hdr.FrameIndex = uint32(fi) //nolint:gosec // below F
		// The symbols, codewords and frame are sized for the mapping and renderer, so neither fails.
		_ = mapper.Symbols(symbols, hdr.FrameIndex, cws)
		_ = e.render.Render(f, hdr, symbols)
		f.Index = fi
		if err := enc.WriteFrame(f); err != nil {
			return err
		}
		done <- nil
	}
	return nil
}
