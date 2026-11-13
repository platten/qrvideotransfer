package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	qrvt "github.com/platten/qrvideotransfer"
)

// fakeAPI records the options of each call and plays scripted events through Progress.
type fakeAPI struct {
	enc     *qrvt.EncodeOptions
	dec     *qrvt.DecodeOptions
	cal     *qrvt.CalibrationOptions
	ana     *qrvt.AnalyzeOptions
	ins     *qrvt.InspectOptions
	keygen  bool
	events  []qrvt.Event
	err     error
	askPass bool     // the transfer is encrypted: call Passphrase
	pass    []string // passphrases obtained, one per call of Passphrase
	passErr error    // the error Passphrase returned

	encRep *qrvt.EncodeReport
	decRep *qrvt.DecodeReport
	rec    *qrvt.Recommendation
	info   *qrvt.StreamInfo
	pub    []byte
	priv   []byte
}

// play sends the scripted events and asks for the passphrase (twice, to check it is cached).
func (f *fakeAPI) play(progress func(qrvt.Event), pass func() (string, error)) {
	for _, e := range f.events {
		if progress != nil {
			progress(e)
		}
	}
	if pass != nil && f.askPass {
		for range 2 {
			p, err := pass()
			if err != nil {
				f.passErr = err
				return
			}
			f.pass = append(f.pass, p)
		}
	}
}

func (f *fakeAPI) Encode(_ context.Context, o qrvt.EncodeOptions) (*qrvt.EncodeReport, error) {
	f.play(o.Progress, o.Passphrase)
	f.enc = &o
	if f.passErr != nil {
		return nil, f.passErr
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.encRep != nil {
		return f.encRep, nil
	}
	return &qrvt.EncodeReport{Output: o.Output, Profile: o.Profile, Files: 2, Frames: 840, Duration: 70 * time.Second, VideoBytes: 85 << 20}, nil
}

func (f *fakeAPI) Decode(_ context.Context, o qrvt.DecodeOptions) (*qrvt.DecodeReport, error) {
	f.play(o.Progress, o.Passphrase)
	f.dec = &o
	if f.passErr != nil {
		return nil, f.passErr
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.decRep != nil {
		return f.decRep, nil
	}
	return &qrvt.DecodeReport{OutputDir: o.OutputDir, Files: 3, Bytes: 4096}, nil
}

func (f *fakeAPI) CalibrationVideo(_ context.Context, o qrvt.CalibrationOptions) error {
	f.play(o.Progress, nil)
	f.cal = &o
	return f.err
}

func (f *fakeAPI) AnalyzeCalibration(_ context.Context, o qrvt.AnalyzeOptions) (*qrvt.Recommendation, error) {
	f.play(o.Progress, nil)
	f.ana = &o
	if f.err != nil {
		return nil, f.err
	}
	if f.rec != nil {
		return f.rec, nil
	}
	return &qrvt.Recommendation{Profile: qrvt.Profile{Width: 2560, Height: 1440, Cell: 4, FPS: 30, Modulation: qrvt.Gray4, Protection: qrvt.Normal, ParityPercent: 20},
		Command: "qrvt encode --resolution 1440p --fps 30", BytesPerSec: 500e3}, nil
}

func (f *fakeAPI) Inspect(_ context.Context, o qrvt.InspectOptions) (*qrvt.StreamInfo, error) {
	f.play(o.Progress, nil)
	f.ins = &o
	if f.err != nil {
		return nil, f.err
	}
	if f.info != nil {
		return f.info, nil
	}
	return &qrvt.StreamInfo{Session: 0xabc, Width: 1920, Height: 1080, GridX: 480, GridY: 270, Modulation: qrvt.Gray4,
		Protection: qrvt.Normal, Frames: 840, FPS: 12, StreamLen: 1 << 20, ShardSize: 128, Blocks: 1, K: 8192, M: 1639}, nil
}

func (f *fakeAPI) GenerateKey() ([]byte, []byte, error) {
	f.keygen = true
	if f.err != nil {
		return nil, nil, f.err
	}
	if f.pub != nil {
		return f.pub, f.priv, nil
	}
	return []byte(testPub), []byte(testPriv), nil
}

// A key pair in the encoding of SECURITY.md section 6 (not a real key: 32 bytes of 0x01 and 0x02).
const (
	testPub  = "qrvt-public-key-v1:AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE="
	testPriv = "qrvt-private-key-v1:AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI="
	// testFP is the first 16 hex digits of the SHA-256 of the 32 bytes of 0x01.
	testFP = "72cd6e8422c407fb"
)

// fakeTerm describes which streams the fake environment treats as terminals and what its prompt reads.
type fakeTerm struct {
	in, out bool
	answers []string // successive ReadPassword results
	prompts []string // prompts shown
}

// run executes args against f and returns stdout, stderr and the error.
func run(t *testing.T, f *fakeAPI, tm *fakeTerm, args ...string) (string, string, error) {
	t.Helper()
	if tm == nil {
		tm = &fakeTerm{}
	}
	var out, errOut bytes.Buffer
	in := strings.NewReader("")
	env := &Env{
		API: f, In: in, Out: &out, Err: &errOut, Version: "1.2.3",
		IsTerminal: func(s any) bool {
			switch s {
			case any(in):
				return tm.in
			case any(&out):
				return tm.out
			}
			return false
		},
		ReadPassword: func(prompt string) (string, error) {
			tm.prompts = append(tm.prompts, prompt)
			if len(tm.answers) == 0 {
				return "", errors.New("no more answers")
			}
			a := tm.answers[0]
			tm.answers = tm.answers[1:]
			return a, nil
		},
	}
	err := Execute(context.Background(), env, args)
	return out.String(), errOut.String(), err
}

// files creates a scratch tree: two inputs, two recordings, keys and a passphrase file.
type files struct {
	dir, a, d, r1, r2, pub, pub2, priv, pass, ffmpeg string
}

func newFiles(t *testing.T) files {
	t.Helper()
	dir := t.TempDir()
	f := files{
		dir: dir, a: filepath.Join(dir, "a.txt"), d: filepath.Join(dir, "d"),
		r1: filepath.Join(dir, "r1.mov"), r2: filepath.Join(dir, "r2.mov"),
		pub: filepath.Join(dir, "alice.pub"), pub2: filepath.Join(dir, "bob.pub"), priv: filepath.Join(dir, "me.key"),
		pass: filepath.Join(dir, "pass.txt"), ffmpeg: filepath.Join(dir, "ffmpeg"),
	}
	write := func(p, s string, mode os.FileMode) {
		if err := os.WriteFile(p, []byte(s), mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(f.d, 0o700); err != nil {
		t.Fatal(err)
	}
	write(f.a, "hello", 0o600)
	write(f.r1, "rec", 0o600)
	write(f.r2, "rec", 0o600)
	write(f.pub, testPub+"\n", 0o600)
	write(f.pub2, "qrvt-public-key-v1:AwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwM=\n", 0o600)
	write(f.priv, testPriv+"\n", 0o600)
	write(f.pass, "correct horse\r\nsecond line\n", 0o600)
	write(f.ffmpeg, "", 0o700)
	return f
}
