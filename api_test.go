package qrvt

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/platten/qrvideotransfer/internal/payload"
	"github.com/platten/qrvideotransfer/internal/pipeline"
	"github.com/platten/qrvideotransfer/internal/prng"
	"github.com/platten/qrvideotransfer/internal/video"
)

// testSpec names the raw-frame test backend, which the tests register in testBackends.
const testSpec = "raw-test"

func init() { testBackends = map[string]video.Backend{testSpec: video.NewRaw()} }

// payloadDir writes a folder of pseudo-random files and returns it with each file's SHA-256 by its
// path as a decode recreates it.
func payloadDir(t testing.TB, seed uint64, sizes ...int) (string, map[string][32]byte) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "docs")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	g := prng.SplitMix64{State: seed}
	want := map[string][32]byte{}
	for i, n := range sizes {
		b := make([]byte, n)
		for j := range b {
			b[j] = byte(g.Next())
		}
		name := "file" + string(rune('a'+i)) + ".bin"
		if err := os.WriteFile(filepath.Join(root, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
		want["docs/"+name] = sha256.Sum256(b)
	}
	return root, want
}

// sameFiles compares the regular files under dir with want.
func sameFiles(t testing.TB, dir string, want map[string][32]byte) {
	t.Helper()
	n := 0
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			n++
		}
		return nil
	})
	if n != len(want) {
		t.Errorf("%d files written, want %d", n, len(want))
	}
	for p, h := range want {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil {
			t.Errorf("%s: %v", p, err)
		} else if sha256.Sum256(b) != h {
			t.Errorf("%s: SHA-256 differs", p)
		}
	}
}

// profile480 is a 480p Gray4 `normal` profile at 12 fps with parity 20.
var profile480 = Profile{Width: 848, Height: 480, Cell: 4, FPS: 12, Modulation: Gray4, Protection: Normal, ParityPercent: 20}

// encodeTest encodes inputs with the raw-frame test backend and returns the video.
func encodeTest(t testing.TB, o EncodeOptions) (string, *EncodeReport) {
	t.Helper()
	if o.Output == "" {
		o.Output = filepath.Join(t.TempDir(), "code.raw")
	}
	if o.Profile == (Profile{}) {
		o.Profile = profile480
	}
	o.Backend, o.TempDir = testSpec, t.TempDir()
	rep, err := Encode(context.Background(), o)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return o.Output, rep
}

// decodeTest decodes recordings with the raw-frame test backend into a new folder.
func decodeTest(t testing.TB, o DecodeOptions) (string, *DecodeReport, error) {
	t.Helper()
	if o.OutputDir == "" {
		o.OutputDir = filepath.Join(t.TempDir(), "out")
	}
	o.Backend = testSpec
	rep, err := Decode(context.Background(), o)
	return o.OutputDir, rep, err
}

// TestEncodeDecode is the API's round trip on a perfect capture (the video itself), with
// encryption and a signature, and the JSON report.
func TestEncodeDecode(t *testing.T) {
	in, want := payloadDir(t, 1, 5000, 700)
	pub, priv, err := payload.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sign := strings.TrimSpace(string(payload.MarshalPrivateKey(priv)))
	verify := []byte(strings.TrimSpace(string(payload.MarshalPublicKey(pub))))
	pass := func() (string, error) { return "correct horse", nil }
	var events []Event
	path, rep := encodeTest(t, EncodeOptions{Inputs: []string{in}, Passphrase: pass, SignKey: []byte(sign),
		Progress: func(e Event) { events = append(events, e) }})
	if rep.Output != path || rep.Files != 2 || rep.Frames < 4 || rep.Profile != profile480 || rep.Session == 0 || rep.VideoBytes <= 0 {
		t.Errorf("encode report %+v", rep)
	}
	if len(events) == 0 || events[0].Stage != StagePack {
		t.Errorf("encode events %+v", events)
	}
	report := filepath.Join(t.TempDir(), "report.json")
	out, dr, err := decodeTest(t, DecodeOptions{Recordings: []string{path}, Passphrase: pass, VerifyKeys: [][]byte{verify}, Report: report})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	sameFiles(t, out, want)
	if dr.Session != rep.Session || !dr.Encrypted || !dr.Signed || string(dr.SignerKey) != string(pub) || dr.Files != 2 ||
		dr.Bytes != 5700 || dr.FramesTotal != rep.Frames || dr.OutputDir != out || dr.Captures < int64(rep.Frames) {
		t.Errorf("decode report %+v", dr)
	}
	b, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	var got DecodeReport
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Session != dr.Session || got.Files != dr.Files || got.Bytes != dr.Bytes || got.FramesTotal != dr.FramesTotal {
		t.Errorf("JSON report %+v, returned %+v", got, dr)
	}
}

// TestEncodeDefaults: a zero profile field takes the default of DESIGN.md section 13.
func TestEncodeDefaults(t *testing.T) {
	in, _ := payloadDir(t, 2, 100)
	rep, err := Encode(context.Background(), EncodeOptions{Inputs: []string{in}, Output: filepath.Join(t.TempDir(), "x.mp4"),
		DryRun: true, Profile: Profile{Width: 848, Height: 480}})
	if err != nil {
		t.Fatal(err)
	}
	want := Profile{Width: 848, Height: 480, Cell: 4, FPS: 12, Modulation: Gray4, Protection: Normal, ParityPercent: 20}
	if rep.Profile != want {
		t.Errorf("profile %+v, want %+v", rep.Profile, want)
	}
}

// TestDryRun reports the video's length, size and loop time without rendering anything.
func TestDryRun(t *testing.T) {
	in, _ := payloadDir(t, 3, 40000)
	out := filepath.Join(t.TempDir(), "x.mp4")
	rep, err := Encode(context.Background(), EncodeOptions{Inputs: []string{in}, Output: out, Profile: profile480, DryRun: true, TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Frames < 4 || rep.Duration <= 0 || rep.VideoBytes <= 0 || rep.StreamLen < 40000 || rep.Output != "" {
		t.Errorf("report %+v", rep)
	}
	if want := int64(rep.Frames) * 1e9 / 12; int64(rep.Duration) != want {
		t.Errorf("duration %v for %d frames at 12 fps", rep.Duration, rep.Frames)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("video written")
	}
}

// TestTypedErrors: each failure a caller can act on comes back as its typed error
// (DESIGN.md section 14).
func TestTypedErrors(t *testing.T) {
	in, _ := payloadDir(t, 4, 6000)
	pub, priv, _ := payload.GenerateKey(rand.Reader)
	other, _, _ := payload.GenerateKey(rand.Reader)
	keyLine := func(b []byte) []byte { return []byte(strings.TrimSpace(string(b))) }
	pass := func(p string) func() (string, error) { return func() (string, error) { return p, nil } }
	encrypted, _ := encodeTest(t, EncodeOptions{Inputs: []string{in}, Passphrase: pass("right")})
	signed, _ := encodeTest(t, EncodeOptions{Inputs: []string{in}, SignKey: keyLine(payload.MarshalPrivateKey(priv))})
	plain, rep := encodeTest(t, EncodeOptions{Inputs: []string{in}})
	_ = pub

	check := func(name string, err error, ok func(error) bool) {
		t.Helper()
		if err == nil || !ok(err) {
			t.Errorf("%s: got %v (%T)", name, err, err)
		}
	}
	is := func(target error) func(error) bool { return func(err error) bool { return errors.Is(err, target) } }

	_, _, err := decodeTest(t, DecodeOptions{Recordings: []string{encrypted}, Passphrase: pass("wrong")})
	check("wrong passphrase", err, is(ErrWrongPassphrase))
	_, _, err = decodeTest(t, DecodeOptions{Recordings: []string{encrypted}})
	check("no passphrase", err, is(ErrWrongPassphrase))
	_, _, err = decodeTest(t, DecodeOptions{Recordings: []string{plain}, VerifyKeys: [][]byte{keyLine(payload.MarshalPublicKey(pub))}})
	check("unsigned", err, is(ErrMissingSignature))
	_, _, err = decodeTest(t, DecodeOptions{Recordings: []string{signed}, VerifyKeys: [][]byte{keyLine(payload.MarshalPublicKey(other))}})
	check("signed by another key", err, is(ErrMissingSignature))
	_, _, err = decodeTest(t, DecodeOptions{Recordings: []string{plain}, VerifyKeys: [][]byte{[]byte("not a key")}})
	check("bad verify key", err, func(err error) bool { return err != nil })
	_, _, err = decodeTest(t, DecodeOptions{Recordings: []string{plain}, MaxSize: 100})
	check("size limit", err, func(err error) bool {
		var l *LimitError
		return errors.As(err, &l) && l.Flag == "--max-size" && l.Max == 100
	})

	// A recording of the first half of the loop: the blocks are named with their shortfall.
	frames := readRaw(t, plain)
	half := writeRawFrames(t, frames[:rep.Frames/2], 12)
	_, _, err = decodeTest(t, DecodeOptions{Recordings: []string{half}})
	check("half the loop", err, func(err error) bool {
		var s *NotEnoughDataError
		return errors.As(err, &s) && len(s.Blocks) == rep.Blocks && s.Blocks[0].Need == rep.K && s.Blocks[0].Have < rep.K
	})

	_, err = Encode(context.Background(), EncodeOptions{Inputs: []string{in}, Output: filepath.Join(t.TempDir(), "x.mp4"),
		Profile: Profile{Width: 848, Height: 480, Modulation: Gray8}, Backend: testSpec})
	check("eight greys", err, is(ErrNotImplemented))
	_, err = Encode(context.Background(), EncodeOptions{Inputs: []string{in}, Output: filepath.Join(t.TempDir(), "x.mp4"),
		SignKey: []byte("not a key"), Backend: testSpec})
	check("bad sign key", err, func(err error) bool { return err != nil })
	_, err = Encode(context.Background(), EncodeOptions{Output: filepath.Join(t.TempDir(), "x.mp4"), Backend: testSpec})
	check("no inputs", err, func(err error) bool { return err != nil })
	_, err = Encode(context.Background(), EncodeOptions{Inputs: []string{in}, Backend: testSpec})
	check("no output", err, func(err error) bool { return err != nil })
	_, err = Decode(context.Background(), DecodeOptions{OutputDir: t.TempDir(), Backend: testSpec})
	check("no recordings", err, func(err error) bool { return err != nil })
	_, err = Decode(context.Background(), DecodeOptions{Recordings: []string{plain}, Backend: testSpec})
	check("no output folder", err, func(err error) bool { return err != nil })
	_, err = Decode(context.Background(), DecodeOptions{Recordings: []string{plain}, OutputDir: t.TempDir(), Backend: testSpec, StateDir: t.TempDir()})
	check("state folder", err, is(ErrNotImplemented))
	_, err = Encode(context.Background(), EncodeOptions{Inputs: []string{in}, Output: filepath.Join(t.TempDir(), "x.mp4"), Backend: "/nonexistent/ffmpeg"})
	check("bad backend", err, func(err error) bool { return err != nil })
}

// TestConversions: the pipeline's events, stats and errors map onto the root package's.
func TestConversions(t *testing.T) {
	pairs := [][2]int{
		{int(pipeline.StagePack), int(StagePack)}, {int(pipeline.StageUnpack), int(StageUnpack)},
		{int(pipeline.StageDemodulate), int(StageDemodulate)}, {int(pipeline.KindStarted), int(KindStarted)},
		{int(pipeline.KindWarning), int(KindWarning)}, {int(pipeline.UnitBytes), int(UnitBytes)},
		{int(pipeline.UnitBlocks), int(UnitBlocks)},
	}
	for _, p := range pairs {
		if p[0] != p[1] {
			t.Errorf("pipeline value %d, root %d", p[0], p[1])
		}
	}
	for _, c := range [][2]string{
		{pipeline.WarnExposureTooLong, string(WarnExposureTooLong)}, {pipeline.WarnEngineInProcess, string(WarnEngineInProcess)},
		{pipeline.WarnSkippedFile, string(WarnSkippedFile)}, {pipeline.WarnReceiverLimit, string(WarnReceiverLimit)},
	} {
		if c[0] != c[1] {
			t.Errorf("warning %q in the pipeline, %q in the root", c[0], c[1])
		}
	}
	st := &pipeline.Stats{Captures: 3, FramesSeen: 2, FramesTotal: 4, CleanFraction: 0.5, RawBitErrors: 0.01, CodewordsOK: 9, BlocksReady: 1, BlocksTotal: 2}
	ev := convertEvent(pipeline.Event{Stage: pipeline.StageRepair, Kind: pipeline.KindStats, Done: 1, Total: 2, Unit: pipeline.UnitBlocks, Stats: st,
		Warn: &pipeline.Warning{Code: "x", Text: "y"}})
	if ev.Stage != StageRepair || ev.Kind != KindStats || ev.Done != 1 || ev.Total != 2 || ev.Unit != UnitBlocks ||
		*ev.Stats != (Stats{3, 2, 4, 0.5, 0.01, 9, 1, 2}) || *ev.Warn != (Warning{"x", "y"}) {
		t.Errorf("converted %+v", ev)
	}
	for _, c := range []struct {
		in error
		ok func(error) bool
	}{
		{&pipeline.ShortError{Blocks: []pipeline.Shortfall{{Block: 2, Have: 1, Need: 3}}}, func(err error) bool {
			var s *NotEnoughDataError
			return errors.As(err, &s) && s.Blocks[0] == Shortfall{2, 1, 3}
		}},
		{&payload.UnsafeArchiveError{Path: "a", Reason: "b"}, func(err error) bool {
			var u *UnsafeArchiveError
			return errors.As(err, &u) && *u == UnsafeArchiveError{"a", "b"}
		}},
		{&payload.UnsupportedVersionError{What: "w", Value: 7}, func(err error) bool {
			var u *UnsupportedVersionError
			return errors.As(err, &u) && *u == UnsupportedVersionError{"w", 7}
		}},
		{&payload.LimitError{Limit: "l", Value: 2, Max: 1, Flag: "--f"}, func(err error) bool {
			var l *LimitError
			return errors.As(err, &l) && *l == LimitError{"l", 2, 1, "--f"}
		}},
		{payload.ErrBadSignature, func(err error) bool { return errors.Is(err, ErrBadSignature) }},
		{payload.ErrMissingSignature, func(err error) bool { return errors.Is(err, ErrMissingSignature) }},
		{payload.ErrWrongPassphrase, func(err error) bool { return errors.Is(err, ErrWrongPassphrase) }},
		{payload.ErrPassphraseRequired, func(err error) bool { return errors.Is(err, ErrWrongPassphrase) }},
		{context.Canceled, func(err error) bool { return errors.Is(err, context.Canceled) }},
	} {
		if got := convertError(c.in); !c.ok(got) {
			t.Errorf("%v converted to %v (%T)", c.in, got, got)
		}
	}
	if convertError(nil) != nil {
		t.Error("nil converted to an error")
	}
}
