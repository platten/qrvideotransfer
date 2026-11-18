package pipeline

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"

	"github.com/platten/qrvideotransfer/internal/fec"
	"github.com/platten/qrvideotransfer/internal/fec/ldpc"
	"github.com/platten/qrvideotransfer/internal/fec/rs"
	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/payload"
	"github.com/platten/qrvideotransfer/internal/video"
)

// MaxSoftBytes caps the summed soft bits kept for codewords not yet decoded (SECURITY.md section 5).
// Beyond it the least confident code frames are evicted.
const MaxSoftBytes = 256 << 20

// statsEvery is how many captures pass between two KindStats events.
const statsEvery = 16

// Decode recovers the stream carried by the recordings and unpacks it (DESIGN.md section 10).
// Recordings are read in order, each by one video decoder whose captures go to a pool of vision
// workers; the workers' results come back to this goroutine, which alone owns the soft-bit
// accumulators, the shard store and the events. Reading stops as soon as every block can be
// reconstructed. Temporary files go in a folder of their own under TempDir, removed on return.
func Decode(ctx context.Context, c DecodeConfig) (*DecodeResult, error) {
	return decode(ctx, c, nil)
}

// processCapture runs the vision chain on one capture; tests replace it to stall a worker.
var processCapture = (*captureWorker).process

// decode is Decode; when seen is not nil it receives the decoder, for tests to inspect.
func decode(ctx context.Context, c DecodeConfig, seen **decoder) (*DecodeResult, error) {
	switch {
	case len(c.Recordings) == 0:
		return nil, errors.New("pipeline: no recordings")
	case c.Backend == nil:
		return nil, errors.New("pipeline: no video backend")
	case c.Unpack.OutputDir == "":
		return nil, errors.New("pipeline: no output folder")
	}
	tmp, err := os.MkdirTemp(c.TempDir, "qrvt-decode-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	d := &decoder{c: &c, emit: emitter(c.Progress), tmp: tmp, frames: map[uint32]*frameAcc{}, softCap: MaxSoftBytes}
	defer d.close()
	if seen != nil {
		*seen = d
	}
	workers := c.Workers
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	d.emit.stage(StageDemodulate, KindStarted, 0, 0, UnitFrames)
	for _, rec := range c.Recordings {
		if d.complete() {
			break
		}
		if err := d.recording(ctx, rec, workers); err != nil {
			return nil, err
		}
	}
	d.sendStats()
	d.emit.stage(StageDemodulate, KindFinished, d.stats.Captures, d.stats.Captures, UnitFrames)
	if eb, ok := c.Backend.(*video.Embedded); ok {
		if err := eb.InProcess(); err != nil {
			d.warn(StageDecodeVideo, WarnEngineInProcess, err.Error())
		}
	}
	if !d.complete() {
		return nil, d.shortfall()
	}
	stream, err := d.repair(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = stream.Close() }()
	up := c.Unpack
	up.Progress = func(p payload.Progress) { d.emit.stage(StageUnpack, KindProgress, p.Done, p.Total, UnitBytes) }
	d.emit.stage(StageUnpack, KindStarted, 0, 0, UnitBytes)
	res, err := payload.Unpack(ctx, stream, d.desc.StreamLen, up)
	if err != nil {
		return nil, err
	}
	d.emit.stage(StageUnpack, KindFinished, res.Bytes, res.Bytes, UnitBytes)
	return &DecodeResult{Session: d.desc.Session, Stats: d.stats, Unpacked: res, Warnings: d.warnings}, nil
}

// frameAcc holds the summed soft bits of one code frame's codewords.
type frameAcc struct {
	soft  [][]int8 // by codeword; nil when none yet, or decoded, or evicted
	done  []bool   // by codeword: decoded and stored
	left  int      // codewords 1..C-1 not yet decoded
	last  int      // index of the last capture that added to it
	dirty bool     // soft bits added since the last attempt
}

// decoder is the owner of a decode's state. Only the goroutine running Decode touches it.
type decoder struct {
	c      *DecodeConfig
	emit   emitter
	tmp    string
	key    *params // the transfer locked onto: the first located capture's
	geom   *geometry
	code   *ldpc.Code
	shard  []byte
	cwBits []byte

	descSoft []int8 // summed soft bits of codeword 0, until the descriptor decodes
	desc     *frame.Descriptor
	plan     fec.Plan
	store    *rs.Store
	ready    int // blocks with at least K shards

	frames     map[uint32]*frameAcc
	pending    []uint32 // frames with soft bits added since their last attempt
	softBytes  int64    // summed soft bits held
	bookBytes  int64    // frame records held, beside their soft bits
	softCap    int64    // MaxSoftBytes, for both; lower in tests
	seen       []uint64 // frames seen, one bit each (F bits, at most 512 KiB)
	framesSeen int
	maxWaiting int // most results held for in-order application at once
	evictSteps int // frames taken off the eviction order, for tests
	captures   int // captures processed, across recordings, with a gap between recordings

	stats            Stats
	located          int64
	cleanSum         float64
	bitErrs, bitsAll int64
	warnings         []Warning
	warned           map[string]bool
}

func (d *decoder) close() {
	if d.store != nil {
		_ = d.store.Close()
	}
}

func (d *decoder) complete() bool { return d.store != nil && d.ready == d.plan.Blocks }

func (d *decoder) warn(s Stage, code, text string) {
	if d.warned == nil {
		d.warned = map[string]bool{}
	}
	if d.warned[code] {
		return
	}
	d.warned[code] = true
	w := Warning{Code: code, Text: text}
	d.warnings = append(d.warnings, w)
	d.emit.warn(s, w)
}

func (d *decoder) sendStats() {
	if d.located > 0 {
		d.stats.CleanFraction = d.cleanSum / float64(d.located)
	}
	if d.bitsAll > 0 {
		d.stats.RawBitErrors = float64(d.bitErrs) / float64(d.bitsAll)
	}
	d.stats.FramesSeen = d.framesSeen
	st := d.stats
	d.emit.send(Event{Stage: StageDemodulate, Kind: KindStats, Stats: &st})
}

// recording reads one recording through the vision workers, adding every capture's soft bits.
func (d *decoder) recording(ctx context.Context, path string, workers int) error {
	info, err := d.c.Backend.Probe(ctx, path)
	if err != nil {
		return err
	}
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	dec, err := d.c.Backend.NewDecoder(rctx, path, video.DecodeConfig{BitDepth: info.BitDepth})
	if err != nil {
		return err
	}
	defer func() { _ = dec.Close() }()
	d.emit.stage(StageDecodeVideo, KindStarted, 0, info.Frames, UnitFrames)

	type job struct {
		f     *video.Frame
		index int
	}
	free := make(chan *video.Frame, workers+2)
	for range workers + 2 {
		free <- &video.Frame{}
	}
	// A capture is read only with a token, returned once its result has been applied in order, so
	// a stalled worker holds back the reading and at most workers+2 results wait to be applied.
	tokens := make(chan struct{}, workers+2)
	jobs := make(chan job)
	results := make(chan captureResult, workers)
	var readErr error
	go func() {
		defer close(jobs)
		for i := 0; ; i++ {
			select {
			case tokens <- struct{}{}:
			case <-rctx.Done():
				return
			}
			var f *video.Frame
			select {
			case f = <-free:
			case <-rctx.Done():
				return
			}
			if err := dec.Next(f); err != nil {
				if !errors.Is(err, io.EOF) {
					readErr = err
				}
				return
			}
			select {
			case jobs <- job{f, i}:
			case <-rctx.Done():
				return
			}
		}
	}()
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var w captureWorker
			for j := range jobs {
				r := processCapture(&w, j.f, j.index)
				free <- j.f
				results <- r
			}
		}()
	}
	go func() { wg.Wait(); close(results) }()

	var applyErr error
	base := d.captures
	waiting := map[int]captureResult{}
	next, read := 0, int64(0)
	for r := range results {
		waiting[r.index] = r
		d.maxWaiting = max(d.maxWaiting, len(waiting))
		for {
			r, ok := waiting[next]
			if !ok {
				break
			}
			delete(waiting, next)
			<-tokens
			next++
			read++
			if d.complete() || rctx.Err() != nil {
				continue // drain
			}
			r.index += base
			if err := d.apply(r); err != nil {
				cancel()
				applyErr = err
				continue
			}
			d.emit.stage(StageDecodeVideo, KindProgress, read, info.Frames, UnitFrames)
			if d.stats.Captures%statsEvery == 0 {
				d.sendStats()
			}
			if d.complete() {
				cancel()
			}
		}
	}
	d.captures = base + next + 2 // captures of different recordings are never adjacent
	if err := ctx.Err(); err != nil {
		return err
	}
	if applyErr != nil {
		return applyErr
	}
	if readErr != nil && !d.complete() {
		return readErr
	}
	if err := d.flush(); err != nil {
		return err
	}
	d.emit.stage(StageDecodeVideo, KindFinished, read, info.Frames, UnitFrames)
	d.sendStats()
	return nil
}

// apply adds one capture's soft bits, in capture order.
func (d *decoder) apply(r captureResult) error {
	if err := d.add(r); err != nil {
		return err
	}
	return d.attemptDue(r.index)
}

// add sums one capture's soft bits into the accumulators.
func (d *decoder) add(r captureResult) error {
	d.stats.Captures++
	if !r.located {
		return nil
	}
	if d.key == nil {
		g, err := newGeometry(r.key)
		if err != nil {
			return nil // a header whose grid holds too few codewords: not a transfer
		}
		d.key, d.geom = &r.key, g
		d.code, _ = ldpc.New(r.key.inner) // known: the header validated it
		d.shard = make([]byte, d.code.ShardSize())
		d.cwBits = make([]byte, (d.code.N()+7)/8)
		d.descSoft = make([]int8, g.sent)
		d.stats.FramesTotal = int(r.key.frames)
		d.seen = make([]uint64, (r.key.frames+63)/64)
	}
	if r.key != *d.key {
		return nil // another transfer, or a false header: counted as a capture, nothing more
	}
	d.located++
	if r.cells > 0 {
		d.cleanSum += float64(r.clean) / float64(r.cells)
	}
	if r.clean == 0 {
		d.warn(StageDemodulate, WarnExposureTooLong, "No part of a capture shows a single code frame: "+
			"the exposure is too long for the display rate. Lower the display rate or brighten the screen.")
	}
	g := d.geom
	for _, ph := range r.phases {
		fa := d.frames[ph.frame]
		if fa == nil {
			fa = &frameAcc{soft: make([][]int8, g.cpf), done: make([]bool, g.cpf), left: g.cpf - 1, last: -2}
			d.frames[ph.frame] = fa
			d.bookBytes += frameRecordBytes(g.cpf)
			if w, bit := ph.frame/64, uint64(1)<<(ph.frame%64); d.seen[w]&bit == 0 {
				d.seen[w] |= bit
				d.framesSeen++
			}
		}
		w := 2 // in halves: adjacent captures of one frame count half (DESIGN.md section 7, rule 7)
		if fa.last == r.index-1 {
			w = 1
		}
		if d.desc == nil {
			addSoft(d.descSoft, ph.soft[:g.sent], w)
		}
		for c := 1; c < g.cpf; c++ {
			if fa.done[c] {
				continue
			}
			if fa.soft[c] == nil {
				fa.soft[c] = make([]int8, g.sent)
				d.softBytes += int64(g.sent)
			}
			addSoft(fa.soft[c], ph.soft[c*g.sent:(c+1)*g.sent], w)
		}
		fa.last = r.index
		if !fa.dirty && fa.left > 0 {
			fa.dirty = true
			d.pending = append(d.pending, ph.frame)
		}
	}
	if d.desc == nil && len(r.phases) > 0 {
		if err := d.tryDescriptor(); err != nil {
			return err
		}
	}
	d.evict()
	return nil
}

// attemptDue attempts, after capture index, every pending frame that two captures have passed
// without, whether or not those captures showed the code (rule 11).
func (d *decoder) attemptDue(index int) error {
	keep := d.pending[:0]
	for _, f := range d.pending {
		fa := d.frames[f]
		if fa == nil {
			continue // should not happen: evict takes a frame whose record it drops off the list
		}
		if d.store == nil || fa.last > index-2 {
			keep = append(keep, f)
			continue
		}
		if err := d.attempt(f, fa); err != nil {
			return err
		}
	}
	d.pending = keep
	return nil
}

// addSoft adds w/2 times src to dst, saturating at -127 and 127.
func addSoft(dst, src []int8, w int) {
	for i, s := range src {
		v := int(dst[i]) + int(s)*w/2
		dst[i] = int8(max(-127, min(127, v)))
	}
}

// tryDescriptor decodes codeword 0's summed soft bits; once it decodes and validates, the plan and
// the shard store are made and every frame seen so far is queued for an attempt.
func (d *decoder) tryDescriptor() error {
	if _, ok := d.code.Decode(d.shard, d.descSoft); !ok {
		return nil
	}
	desc, err := frame.ParseDescriptor(d.shard, d.key.header(), d.geom.cpf)
	if err != nil {
		return err
	}
	plan, err := fec.NewPlan(desc.StreamLen, desc.ShardSize, desc.M, desc.OrderSeed)
	if err != nil {
		return err
	}
	size, err := rs.StoreSize(plan)
	if err != nil {
		return err
	}
	// The store and, later, the stream file.
	if err := checkDisk(d.tmp, "shard store and stream", size+desc.StreamLen); err != nil {
		return err
	}
	store, err := rs.Create(filepath.Join(d.tmp, "shards"), plan)
	if err != nil {
		return err
	}
	d.desc, d.plan, d.store, d.descSoft = &desc, plan, store, nil
	d.stats.BlocksTotal = plan.Blocks
	d.pending = d.pending[:0]
	for f, fa := range d.frames {
		if fa.left > 0 {
			fa.dirty = true
			d.pending = append(d.pending, f)
		}
	}
	return nil
}

// attempt decodes every codeword of frame f that has soft bits and stores the shards that pass.
func (d *decoder) attempt(f uint32, fa *frameAcc) error {
	fa.dirty = false
	g, p := d.geom, d.plan
	for c := 1; c < g.cpf; c++ {
		if fa.done[c] || fa.soft[c] == nil {
			continue
		}
		if _, ok := d.code.Decode(d.shard, fa.soft[c]); !ok {
			continue
		}
		d.countBitErrors(fa.soft[c])
		b, s := p.Locate(p.Packet(int(f), c, g.cpf))
		before := d.store.Have(b)
		if err := d.store.Put(b, s, d.shard); err != nil {
			return err
		}
		after := d.store.Have(b)
		if before < p.K && after >= p.K {
			d.ready++
			d.stats.BlocksReady = d.ready
		}
		if after > before { // a frame decoded again after its record was dropped adds nothing new
			d.stats.CodewordsOK++
		}
		fa.done[c], fa.soft[c] = true, nil
		fa.left--
		d.softBytes -= int64(g.sent)
	}
	return nil
}

// countBitErrors counts the hard decisions of soft that differ from the codeword just decoded.
func (d *decoder) countBitErrors(soft []int8) {
	d.code.Encode(d.cwBits, d.shard)
	for t, s := range soft {
		bit := d.cwBits[t/8] >> (7 - t%8) & 1
		if s != 0 {
			d.bitsAll++
			if (s < 0) != (bit == 1) {
				d.bitErrs++
			}
		}
	}
}

// flush attempts every frame with soft bits added since its last attempt, at a recording's end.
func (d *decoder) flush() error {
	if d.store == nil {
		return nil
	}
	for _, f := range d.pending {
		if err := d.attempt(f, d.frames[f]); err != nil {
			return err
		}
	}
	d.pending = d.pending[:0]
	return nil
}

// evict keeps the soft bits and frame records held under the cap: it drops the soft bits of the
// least confident frame first, and once no frame holds any, the record of the frame seen least
// recently. A frame whose record was dropped and that is seen again is decoded again; its shards
// are already stored, so nothing is counted twice (attempt).
func (d *decoder) evict() {
	for d.usedBytes() > d.softCap {
		var worst, oldest *frameAcc
		var oldestFrame uint32
		best := -1.0
		for f, fa := range d.frames {
			if oldest == nil || fa.last < oldest.last {
				oldest, oldestFrame = fa, f
			}
			n, sum := 0, 0
			for _, s := range fa.soft {
				for _, v := range s {
					sum += int(max(v, -v))
				}
				n += len(s)
			}
			if n == 0 {
				continue
			}
			if m := float64(sum) / float64(n); best < 0 || m < best {
				worst, best = fa, m
			}
		}
		switch {
		case worst != nil:
			for c, s := range worst.soft {
				d.softBytes -= int64(len(s))
				worst.soft[c] = nil
			}
		case oldest != nil:
			delete(d.frames, oldestFrame)
			d.bookBytes -= frameRecordBytes(len(oldest.done))
			d.pending = slices.DeleteFunc(d.pending, func(f uint32) bool { return f == oldestFrame })
		default:
			return
		}
	}
}

// shortfall describes what is missing.
func (d *decoder) shortfall() error {
	if d.store == nil {
		return &ShortError{}
	}
	var e ShortError
	for b := range d.plan.Blocks {
		if h := d.store.Have(b); h < d.plan.K {
			e.Blocks = append(e.Blocks, Shortfall{Block: b, Have: h, Need: d.plan.K})
		}
	}
	return &e
}

// repair reconstructs every block's data shards into the stream file and checks the stream's hash
// against the descriptor's.
func (d *decoder) repair(ctx context.Context) (*os.File, error) {
	p := d.plan
	d.emit.stage(StageRepair, KindStarted, 0, int64(p.Blocks), UnitBlocks)
	// The sparse store may have grown into the space counted for the stream since it was made.
	if err := checkDisk(d.tmp, "stream", d.desc.StreamLen); err != nil {
		return nil, err
	}
	f, err := os.Create(filepath.Join(d.tmp, "stream"))
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
		}
	}()
	codec, err := rs.NewCodec(p.K, p.M)
	if err != nil {
		return nil, err
	}
	shards := make([][]byte, p.K+p.M)
	bufs := make([][]byte, p.K+p.M)
	for i := range bufs {
		bufs[i] = make([]byte, p.ShardSize)
	}
	for b := range p.Blocks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for i := range shards {
			shards[i] = bufs[i][:p.ShardSize]
			has, err := d.store.Get(b, i, shards[i])
			if err != nil {
				return nil, err
			}
			if !has {
				shards[i] = shards[i][:0]
			}
		}
		if err := codec.Reconstruct(shards); err != nil {
			return nil, err
		}
		for i := range p.K {
			off := p.DataOffset(b, i)
			if n := min(int64(p.ShardSize), d.desc.StreamLen-off); n > 0 {
				if _, err := f.WriteAt(shards[i][:n], off); err != nil {
					return nil, err
				}
			}
		}
		d.emit.stage(StageRepair, KindProgress, int64(b+1), int64(p.Blocks), UnitBlocks)
	}
	d.emit.stage(StageRepair, KindFinished, int64(p.Blocks), int64(p.Blocks), UnitBlocks)
	d.emit.stage(StageVerify, KindStarted, 0, d.desc.StreamLen, UnitBytes)
	if err := checkHash(ctx, f, d.desc.StreamLen, d.desc.StreamHash); err != nil {
		return nil, err
	}
	d.emit.stage(StageVerify, KindFinished, d.desc.StreamLen, d.desc.StreamLen, UnitBytes)
	ok = true
	return f, nil
}

// checkHash compares the first 16 bytes of the SHA-256 of the stream's n bytes with want
// (FORMAT.md section 12, order of checks: this comes first).
func checkHash(ctx context.Context, r io.ReaderAt, n int64, want [16]byte) error {
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for off := int64(0); off < n; {
		if err := ctx.Err(); err != nil {
			return err
		}
		k, err := r.ReadAt(buf[:min(int64(len(buf)), n-off)], off)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if k == 0 {
			return fmt.Errorf("%w: the stream ends at %d of %d bytes", ErrStreamHash, off, n)
		}
		h.Write(buf[:k])
		off += int64(k)
	}
	if [16]byte(h.Sum(nil)[:16]) != want {
		return ErrStreamHash
	}
	return nil
}

// frameRecordBytes is the bookkeeping a frame record holds beside its soft bits: a done flag and a
// slice header per codeword, the record itself and its map entry.
func frameRecordBytes(cpf int) int64 { return 96 + 25*int64(cpf) }

// usedBytes returns the soft bits and frame records held.
func (d *decoder) usedBytes() int64 { return d.softBytes + d.bookBytes }
