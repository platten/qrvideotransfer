package pipeline

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/platten/qrvideotransfer/internal/fec/ldpc"
	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/payload"
	"github.com/platten/qrvideotransfer/internal/video"
)

var errInjected = errors.New("injected")

// TestUnusualCaptures: a capture whose header names a grid too small for two codewords does not
// lock the decode; once locked, a capture of another transfer adds nothing.
func TestUnusualCaptures(t *testing.T) {
	key, truth := fixture(t)
	d := &decoder{c: &DecodeConfig{}, tmp: t.TempDir(), frames: map[uint32]*frameAcc{}, softCap: MaxSoftBytes}
	defer d.close()
	tiny := params{session: 1, gridX: 53, gridY: 30, mod: 1, inner: 4, frames: 4}
	if _, err := newGeometry(tiny); err == nil {
		t.Fatal("a 53 x 30 grid with `robust` holds two codewords")
	}
	var w captureWorker
	if _, err := w.geometry(tiny); err == nil || len(w.geoms) != 0 {
		t.Errorf("a worker cached a geometry for the tiny grid: %v", err)
	}
	k2 := key
	k2.mod = 1
	for _, k := range []params{key, k2, key} {
		if g, err := w.geometry(k); err != nil || g.key != k {
			t.Fatalf("geometry %+v: %v", k, err)
		}
	}
	if len(w.geoms) != 2 || w.geoms[0].key != key {
		t.Errorf("worker cache %d entries, first %+v", len(w.geoms), w.geoms[0].key)
	}
	other := key
	other.session++
	for i, k := range []params{tiny, key, other} {
		if err := d.apply(captureResult{index: i, located: true, key: k, cells: 1, clean: 1,
			phases: []phaseBits{{frame: 0, soft: truth[0]}}}); err != nil {
			t.Fatal(err)
		}
	}
	if d.key == nil || *d.key != key || d.located != 1 || d.stats.Captures != 3 {
		t.Errorf("locked onto %+v after %d located of %d captures", d.key, d.located, d.stats.Captures)
	}
}

// TestFileFaults: a missing temporary folder, an output that is a folder, and a skipped input.
func TestFileFaults(t *testing.T) {
	in, _ := writeInputs(t, 18, 3000)
	missing := filepath.Join(t.TempDir(), "none")
	c := profile480(in)
	c.Output, c.Backend, c.TempDir = filepath.Join(t.TempDir(), "x.raw"), video.NewRaw(), missing
	if _, err := Encode(context.Background(), c); err == nil {
		t.Error("encode with a missing temporary folder")
	}
	path, _ := encodeRaw(t, profile480(in))
	if _, err := Decode(context.Background(), DecodeConfig{Recordings: []string{path}, Backend: video.NewRaw(), TempDir: missing,
		Unpack: payload.UnpackOptions{OutputDir: t.TempDir()}}); err == nil {
		t.Error("decode with a missing temporary folder")
	}
	c.TempDir, c.Output = t.TempDir(), filepath.Join(t.TempDir(), "code.raw")
	if err := os.Mkdir(c.Output, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := Encode(context.Background(), c); err == nil {
		t.Error("encode over a folder")
	}
	if fs := filesUnder(t, filepath.Dir(c.Output)); len(fs) != 0 {
		t.Errorf("files left beside the output: %v", fs)
	}
	if err := os.Symlink(filepath.Join(in, "first.bin"), filepath.Join(in, "link.bin")); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	c.Output = filepath.Join(t.TempDir(), "x.raw")
	res, err := Encode(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 1 || res.Warnings[0].Code != WarnSkippedFile {
		t.Errorf("warnings %+v", res.Warnings)
	}
}

// TestCompleteSkipsRest: once every block is complete, later recordings are not read.
func TestCompleteSkipsRest(t *testing.T) {
	in, want := writeInputs(t, 19, 3000)
	path, res := encodeRaw(t, profile480(in))
	out, dr, err := decodeTo(t, path, path)
	if err != nil {
		t.Fatal(err)
	}
	checkOutput(t, out, want)
	if dr.Stats.Captures > int64(res.Frames) {
		t.Errorf("%d captures read for %d frames", dr.Stats.Captures, res.Frames)
	}
}

// faulty is the raw-frame backend with failures injected; it cannot join runs.
type faulty struct {
	raw                        *video.Raw
	probe, newEnc, write, next error
	close, newDec, concat      error
}

func (b *faulty) Probe(ctx context.Context, p string) (video.ClipInfo, error) {
	if b.probe != nil {
		return video.ClipInfo{}, b.probe
	}
	return b.raw.Probe(ctx, p)
}

func (b *faulty) NewEncoder(ctx context.Context, p string, c video.EncodeConfig) (video.Encoder, error) {
	if b.newEnc != nil {
		return nil, b.newEnc
	}
	e, err := b.raw.NewEncoder(ctx, p, c)
	return &faultyEncoder{e, b}, err
}

func (b *faulty) NewDecoder(ctx context.Context, p string, c video.DecodeConfig) (video.Decoder, error) {
	if b.newDec != nil {
		return nil, b.newDec
	}
	d, err := b.raw.NewDecoder(ctx, p, c)
	return &faultyDecoder{d, b.next}, err
}

type faultyEncoder struct {
	video.Encoder
	b *faulty
}

func (e *faultyEncoder) WriteFrame(f *video.Frame) error {
	if e.b.write != nil {
		return e.b.write
	}
	return e.Encoder.WriteFrame(f)
}

func (e *faultyEncoder) Close() error {
	err := e.Encoder.Close()
	if e.b.close != nil {
		return e.b.close
	}
	return err
}

type faultyDecoder struct {
	video.Decoder
	next error
}

func (d *faultyDecoder) Next(f *video.Frame) error {
	if d.next != nil {
		return d.next
	}
	return d.Decoder.Next(f)
}

// joining is faulty with the raw backend's Concat.
type joining struct{ *faulty }

func (j joining) Concat(ctx context.Context, runs []string, path string, fps int) error {
	if j.concat != nil {
		return j.concat
	}
	return j.raw.Concat(ctx, runs, path, fps)
}

// TestBackendFaults: a failure of the video backend stops an encode or decode with that error and
// leaves no temporary file.
func TestBackendFaults(t *testing.T) {
	in, _ := writeInputs(t, 13, 4000)
	for name, b := range map[string]video.Backend{
		"no concat":   &faulty{raw: video.NewRaw()},
		"new encoder": joining{&faulty{raw: video.NewRaw(), newEnc: errInjected}},
		"write":       joining{&faulty{raw: video.NewRaw(), write: errInjected}},
		"close":       joining{&faulty{raw: video.NewRaw(), close: errInjected}},
		"concat":      joining{&faulty{raw: video.NewRaw(), concat: errInjected}},
	} {
		t.Run("encode "+name, func(t *testing.T) {
			c := profile480(in)
			c.Output, c.Backend, c.TempDir = filepath.Join(t.TempDir(), "x.raw"), b, t.TempDir()
			if _, err := Encode(context.Background(), c); err == nil || name != "no concat" && !errors.Is(err, errInjected) {
				t.Fatalf("got %v", err)
			}
			if fs := filesUnder(t, c.TempDir); len(fs) != 0 {
				t.Errorf("temporary files left: %v", fs)
			}
		})
	}
	path, _ := encodeRaw(t, profile480(in))
	for name, b := range map[string]video.Backend{
		"probe": &faulty{raw: video.NewRaw(), probe: errInjected},
		"next":  &faulty{raw: video.NewRaw(), next: errInjected},
		"open":  &faulty{raw: video.NewRaw(), newDec: errInjected},
	} {
		t.Run("decode "+name, func(t *testing.T) {
			tmp := t.TempDir()
			_, err := Decode(context.Background(), DecodeConfig{Recordings: []string{path}, Backend: b, TempDir: tmp,
				Unpack: payload.UnpackOptions{OutputDir: filepath.Join(t.TempDir(), "out")}})
			if !errors.Is(err, errInjected) {
				t.Fatalf("got %v", err)
			}
			if fs := filesUnder(t, tmp); len(fs) != 0 {
				t.Errorf("temporary files left: %v", fs)
			}
		})
	}
}

// TestTinyGrid: a grid that holds fewer than two codewords per frame is refused.
func TestTinyGrid(t *testing.T) {
	in, _ := writeInputs(t, 14, 100)
	c := profile480(in)
	c.Cell, c.Inner = 16, 4
	c.Output, c.Backend, c.TempDir = filepath.Join(t.TempDir(), "x.raw"), video.NewRaw(), t.TempDir()
	if _, err := Encode(context.Background(), c); err == nil || !strings.Contains(err.Error(), "codewords") {
		t.Fatalf("got %v", err)
	}
}

// TestExposureWarning: a located capture with no cell credited to a single frame raises the
// ExposureTooLong warning, once.
func TestExposureWarning(t *testing.T) {
	key, _ := fixture(t)
	var got []Event
	d := &decoder{c: &DecodeConfig{}, emit: func(e Event) { got = append(got, e) }, tmp: t.TempDir(), frames: map[uint32]*frameAcc{}, softCap: MaxSoftBytes}
	defer d.close()
	for i := range 3 {
		if err := d.apply(captureResult{index: i, located: true, key: key, cells: 100}); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 1 || got[0].Kind != KindWarning || got[0].Warn.Code != WarnExposureTooLong || len(d.warnings) != 1 {
		t.Errorf("events %+v, warnings %+v", got, d.warnings)
	}
}

// TestHostileDescriptor: a descriptor that passes its CRC but contradicts the header stops the
// decode (FORMAT.md section 10; SECURITY.md T2).
func TestHostileDescriptor(t *testing.T) {
	key, _ := fixture(t)
	g, err := newGeometry(key)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := ldpc.New(key.inner)
	desc := frame.Descriptor{Session: uint64(key.session), StreamLen: 3000, ShardSize: code.ShardSize(), K: 24, M: 5, Blocks: 1,
		Frames: int(key.frames) + 4, CodewordsPerFrame: g.cpf}
	shard := make([]byte, code.ShardSize())
	b := desc.Marshal()
	copy(shard, b[:])
	cw := make([]byte, (code.N()+7)/8)
	code.Encode(cw, shard)
	soft := make([]int8, g.cpf*g.sent)
	for t2 := range g.sent {
		soft[t2] = 100
		if cw[t2/8]>>(7-t2%8)&1 == 1 {
			soft[t2] = -100
		}
	}
	d := &decoder{c: &DecodeConfig{}, tmp: t.TempDir(), frames: map[uint32]*frameAcc{}, softCap: MaxSoftBytes}
	defer d.close()
	err = d.apply(captureResult{index: 0, located: true, key: key, cells: 1, clean: 1, phases: []phaseBits{{frame: 0, soft: soft}}})
	if !errors.Is(err, frame.ErrDescriptor) {
		t.Fatalf("got %v", err)
	}
}

// TestCheckHash: the stream's hash is checked; a short or altered stream and a cancelled check
// fail.
func TestCheckHash(t *testing.T) {
	data := strings.NewReader("qrvt stream bytes")
	n := int64(data.Len())
	var want [16]byte
	h := sha256.Sum256([]byte("qrvt stream bytes"))
	copy(want[:], h[:])
	ctx := context.Background()
	if err := checkHash(ctx, data, n, want); err != nil {
		t.Errorf("good stream: %v", err)
	}
	if err := checkHash(ctx, data, n+5, want); !errors.Is(err, ErrStreamHash) {
		t.Errorf("short stream: %v", err)
	}
	want[0] ^= 1
	if err := checkHash(ctx, data, n, want); !errors.Is(err, ErrStreamHash) {
		t.Errorf("altered stream: %v", err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := checkHash(cctx, data, n, want); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
	if e := (&ShortError{Blocks: []Shortfall{{0, 3, 5}, {2, 4, 5}}}).Error(); !strings.Contains(e, "2 blocks short by 3 shards") {
		t.Errorf("message %q", e)
	}
}

// TestHostileDescriptorRecording: a recording whose frames carry a descriptor that passes its CRC
// but contradicts the header stops the decode with that error and writes nothing.
func TestHostileDescriptorRecording(t *testing.T) {
	in, _ := writeInputs(t, 21, 3000)
	c := profile480(in)
	c.Output, c.Backend, c.TempDir = filepath.Join(t.TempDir(), "x.raw"), video.NewRaw(), t.TempDir()
	c.Random = &prngReader{}
	tmp := t.TempDir()
	spool, err := os.Create(filepath.Join(tmp, "stream"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = spool.Close() }()
	packed, err := payload.Pack(context.Background(), spool, payload.PackOptions{Inputs: c.Inputs, Compression: c.Compression})
	if err != nil {
		t.Fatal(err)
	}
	e, err := newEncoding(&c, packed)
	if err != nil {
		t.Fatal(err)
	}
	bad := e.desc
	bad.Frames += 4
	code, _ := ldpc.New(c.Inner)
	shard := make([]byte, code.ShardSize())
	b := bad.Marshal()
	copy(shard, b[:])
	code.Encode(e.descCW, shard)
	parity, err := os.Create(filepath.Join(tmp, "parity"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parity.Close() }()
	if err := e.protect(context.Background(), nil, spool, parity); err != nil {
		t.Fatal(err)
	}
	if _, err := e.writeVideo(context.Background(), nil, spool, parity, tmp); err != nil {
		t.Fatal(err)
	}
	out, _, err := decodeTo(t, c.Output)
	if !errors.Is(err, frame.ErrDescriptor) {
		t.Fatalf("got %v", err)
	}
	if fs := filesUnder(t, out); len(fs) != 0 {
		t.Errorf("files written: %v", fs)
	}
}

// TestEmbeddedInProcess: decoding through the embedded engine without engine worker processes (this
// test binary does not serve them) warns once that the engine ran in process.
func TestEmbeddedInProcess(t *testing.T) {
	if raceEnabled {
		t.Skip("the engine is slow under the race detector")
	}
	in, want := writeInputs(t, 22, 3000)
	be, err := video.NewEmbedded(video.Options{})
	if err != nil {
		t.Fatal(err)
	}
	c := profile480(in)
	c.Output, c.Backend = filepath.Join(t.TempDir(), "code.mp4"), be
	path, _ := encodeRaw(t, c)
	out := filepath.Join(t.TempDir(), "out")
	res, err := Decode(context.Background(), DecodeConfig{Recordings: []string{path}, Backend: be, TempDir: t.TempDir(),
		Unpack: payload.UnpackOptions{OutputDir: out}})
	if err != nil {
		t.Fatal(err)
	}
	checkOutput(t, out, want)
	if len(res.Warnings) != 1 || res.Warnings[0].Code != WarnEngineInProcess {
		t.Errorf("warnings %+v", res.Warnings)
	}
}
