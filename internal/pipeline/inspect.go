package pipeline

import (
	"context"
	"errors"
	"slices"

	"github.com/platten/qrvideotransfer/internal/fec/ldpc"
	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/video"
)

// InspectConfig are the inputs of [Inspect].
type InspectConfig struct {
	Path     string // a code video or a recording of one
	Backend  video.Backend
	Workers  int // 0: GOMAXPROCS, reduced to fit the decode's memory budget as Decode's are
	Progress func(Event)
}

// InspectResult is what [Inspect] read.
type InspectResult struct {
	Probe video.ClipInfo
	// Header is the header of the transfer Inspect locked onto, the first located capture's, with
	// frame index 0; HeaderRead is false, and Header zero, when no capture was located.
	Header     frame.Header
	HeaderRead bool
	// Descriptor is the stream descriptor; nil when it was not decoded.
	Descriptor *frame.Descriptor
	Stats      Stats
	Warnings   []Warning
}

// Inspect reads the recording at c.Path as Decode reads one, summing only codeword 0, the stream
// descriptor, of the transfer it locks onto (the first located capture's), and stops as soon as
// the descriptor decodes. It makes no shard store and writes nothing, so it needs no disk space.
// When the recording ends first it returns what it read with a *ShortError carrying the failure
// diagnostics of DESIGN.md section 13, an agreed refusal included, as Decode's would. A
// descriptor that decodes but is refused is an error, as in Decode.
func Inspect(ctx context.Context, c InspectConfig) (*InspectResult, error) {
	switch {
	case c.Path == "":
		return nil, errors.New("pipeline: no recording")
	case c.Backend == nil:
		return nil, errors.New("pipeline: no video backend")
	}
	in := &inspector{emit: emitter(c.Progress)}
	s := &scanRun{backend: c.Backend, workers: c.Workers, emit: in.emit, process: scanCapture, warn: in.warn}
	in.emit.stage(StageDemodulate, KindStarted, 0, 0, UnitFrames)
	read, info, err := s.recording(ctx, c.Path, func(r *captureResult, read, frames int64) (bool, error) {
		if err := in.add(r); err != nil {
			return false, err
		}
		in.emit.stage(StageDecodeVideo, KindProgress, read, frames, UnitFrames)
		return in.desc != nil, nil
	})
	if err != nil {
		return nil, err
	}
	res := &InspectResult{Probe: *info, Descriptor: in.desc, Warnings: in.warnings}
	if in.key != nil {
		res.Header, res.HeaderRead = in.key.header(), true
	}
	in.emit.stage(StageDecodeVideo, KindFinished, read, info.Frames, UnitFrames)
	res.Stats = in.statsNow()
	st := res.Stats
	in.emit.send(Event{Stage: StageDemodulate, Kind: KindStats, Stats: &st})
	in.emit.stage(StageDemodulate, KindFinished, st.Captures, st.Captures, UnitFrames)
	if in.desc == nil {
		return res, &ShortError{Captures: st.Captures, Located: in.diag.finders, HeadersRead: in.diag.headers,
			Other: in.diag.others(in.key), FramesSeen: in.framesSeen, FramesTotal: st.FramesTotal, Reject: in.diag.agreed(),
			LockedRate: in.lock.locked()}
	}
	return res, nil
}

// inspector is Inspect's state: Decode's, but for codeword 0 only. Only the goroutine running
// Inspect touches it.
type inspector struct {
	emit     emitter
	warnings []Warning
	warned   map[string]bool

	key      *params // the transfer locked onto: the first located capture's
	geom     *geometry
	code     *ldpc.Code
	shard    []byte
	descSoft []int8 // summed soft bits of codeword 0
	cw       []int8 // one capture's codeword 0, in codeword order
	desc     *frame.Descriptor

	prev       []uint32 // the frames the capture prev added
	prevIndex  int
	seen       []uint64 // frames seen, one bit each
	framesSeen int

	stats    Stats
	located  int64
	cleanSum float64
	diag     diagnostics
	lock     lockTrack
}

func (in *inspector) warn(s Stage, code, text string) {
	if in.warned == nil {
		in.warned = map[string]bool{}
	}
	if in.warned[code] {
		return
	}
	in.warned[code] = true
	w := Warning{Code: code, Text: text}
	in.warnings = append(in.warnings, w)
	in.emit.warn(s, w)
}

func (in *inspector) statsNow() Stats {
	st := in.stats
	if in.located > 0 {
		st.CleanFraction = in.cleanSum / float64(in.located)
	}
	st.FramesSeen = in.framesSeen
	return st
}

// add sums codeword 0 of one capture, in capture order, as decoder.add sums every codeword: the
// same lock onto a transfer, the same weights, and the same descriptor checks.
func (in *inspector) add(r *captureResult) error {
	in.stats.Captures++
	in.diag.note(r, in.key)
	if !r.located {
		return nil
	}
	if in.key == nil {
		g, err := newGeometry(r.key)
		if err != nil {
			return nil // a header whose grid holds too few codewords: not a transfer
		}
		in.key, in.geom = &r.key, g
		in.code, _ = ldpc.New(r.key.inner) // known: the header validated it
		in.shard = make([]byte, in.code.ShardSize())
		in.descSoft, in.cw = make([]int8, g.sent), make([]int8, g.sent)
		in.stats.FramesTotal = int(r.key.frames)
		in.seen = make([]uint64, (r.key.frames+63)/64)
		in.lock = newLockTrack(r.key.frames)
		in.prevIndex = -2
	}
	if r.key != *in.key {
		return nil // another transfer, or a false header: counted as a capture, nothing more
	}
	in.located++
	if r.cells > 0 {
		in.cleanSum += float64(r.clean) / float64(r.cells)
		in.lock.see(r.first, r.index, float32(r.clean)/float32(r.cells))
	}
	if r.clean == 0 {
		in.warn(StageDemodulate, WarnExposureTooLong, "No part of a capture shows a single code frame: "+
			"the exposure is too long for the display rate. Lower the display rate or brighten the screen.")
	}
	g := in.geom
	adjacent := in.prevIndex == r.index-1
	var added []uint32
	for _, ph := range r.phases {
		if w, bit := ph.frame/64, uint64(1)<<(ph.frame%64); in.seen[w]&bit == 0 {
			in.seen[w] |= bit
			in.framesSeen++
		}
		w := 2 // in halves: adjacent captures of one frame count half (DESIGN.md section 7, rule 7)
		if adjacent && slices.Contains(in.prev, ph.frame) {
			w = 1
		}
		for t := range in.cw {
			in.cw[t] = ph.soft[g.mapping.Position(ph.frame, 0, t)]
		}
		addSoft(in.descSoft, in.cw, w)
		added = append(added, ph.frame)
	}
	in.prev, in.prevIndex = added, r.index
	if len(r.phases) == 0 {
		return nil
	}
	desc, _, ok, err := decodeDescriptor(in.code, in.shard, in.descSoft, in.key, g)
	if !ok || err != nil {
		return err
	}
	in.desc = &desc
	return nil
}
