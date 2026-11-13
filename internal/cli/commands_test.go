package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	qrvt "github.com/platten/qrvideotransfer"
	"github.com/platten/qrvideotransfer/internal/cli/ui"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func TestExitCode(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{nil, 0},
		{errors.New("other"), 1},
		{qrvt.ErrNotImplemented, 1},
		{usagef("bad flag"), 2},
		{&qrvt.NotEnoughDataError{}, 3},
		{fmt.Errorf("wrapped: %w", &qrvt.NotEnoughDataError{}), 3},
		{qrvt.ErrWrongPassphrase, 4},
		{qrvt.ErrBadSignature, 4},
		{qrvt.ErrMissingSignature, 4},
		{&qrvt.UnsafeArchiveError{}, 5},
		{&qrvt.LimitError{}, 5},
		{&qrvt.UnsupportedVersionError{}, 5},
		{context.Canceled, 130},
		{guide(qrvt.ErrWrongPassphrase, nil, nil), 4},
	}
	for _, tc := range tests {
		if got := ExitCode(tc.err); got != tc.want {
			t.Errorf("ExitCode(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
}

func TestGuidance(t *testing.T) {
	short := &qrvt.NotEnoughDataError{Blocks: []qrvt.Shortfall{{Block: 0, Have: 980, Need: 1000}, {Block: 3, Have: 970, Need: 1000}}}
	stats := &qrvt.Stats{BlocksTotal: 2}
	tests := []struct {
		err   error
		stats *qrvt.Stats
		o     *qrvt.DecodeOptions
		want  string
	}{
		{short, stats, &qrvt.DecodeOptions{Recordings: []string{"a"}}, "2.5% short: record one more loop and pass both files"},
		{short, stats, &qrvt.DecodeOptions{Recordings: []string{"a", "b"}}, "2.5% short: record one more loop and pass all the recordings"},
		{short, stats, &qrvt.DecodeOptions{Recordings: []string{"a"}, StateDir: "s"}, "2.5% short: record one more loop and decode it with the same --state-dir"},
		{short, nil, &qrvt.DecodeOptions{Recordings: []string{"a"}}, "2.5% short"},
		{&qrvt.NotEnoughDataError{Blocks: []qrvt.Shortfall{{Have: 1, Need: 4}}}, &qrvt.Stats{BlocksTotal: 10}, nil, "7.5% short"},
		{&qrvt.NotEnoughDataError{}, nil, nil, "record one more loop"},
		{qrvt.ErrWrongPassphrase, nil, nil, "check the passphrase"},
		{qrvt.ErrBadSignature, nil, nil, "altered or damaged"},
		{qrvt.ErrMissingSignature, nil, nil, "not signed by any key given with --verify-key"},
		{&qrvt.UnsafeArchiveError{Path: "../x", Reason: "leaves the folder"}, nil, nil, "Nothing was written"},
		{&qrvt.LimitError{Limit: "unpacked size", Value: 2, Max: 1, Flag: "--max-size"}, nil, nil, "--max-size"},
		{&qrvt.LimitError{Limit: "unpacked size", Value: 2, Max: 1}, nil, nil, "no flag raises it"},
		{&qrvt.LimitError{Limit: "stream size", Value: 2, Max: 1, Flag: "--other"}, nil, nil, "no flag raises it"},
		{&qrvt.LimitError{Limit: "manifest entries", Value: 2, Max: 1}, nil, nil, "no flag raises it"},
		{&qrvt.UnsupportedVersionError{What: "format version", Value: 9}, nil, nil, "newer qrvt"},
		{qrvt.ErrNotImplemented, nil, nil, "not implemented yet"},
		{context.Canceled, nil, &qrvt.DecodeOptions{StateDir: "s"}, "run the same command again to resume"},
		{context.Canceled, nil, nil, "Stopped"},
	}
	for _, tc := range tests {
		g := guide(tc.err, tc.stats, tc.o)
		if !errors.Is(g, tc.err) && !errors.As(tc.err, new(*qrvt.NotEnoughDataError)) {
			t.Errorf("guide(%v) does not wrap the error", tc.err)
		}
		if !strings.Contains(strings.ToLower(g.Error()), strings.ToLower(tc.want)) {
			t.Errorf("guide(%v) = %q, want it to contain %q", tc.err, g.Error(), tc.want)
		}
	}
	if guide(nil, nil, nil) != nil {
		t.Error("guide(nil) is not nil")
	}
	other := errors.New("disk full")
	if g := guide(other, nil, nil); g.Error() != "disk full" {
		t.Errorf("guide adds text to an error it knows nothing about: %q", g.Error())
	}
}

func TestDecodeFailureGuidance(t *testing.T) {
	f := newFiles(t)
	fa := &fakeAPI{
		events: []qrvt.Event{{Kind: qrvt.KindStats, Stats: &qrvt.Stats{BlocksTotal: 2}}},
		err:    &qrvt.NotEnoughDataError{Blocks: []qrvt.Shortfall{{Have: 980, Need: 1000}, {Block: 1, Have: 970, Need: 1000}}},
	}
	_, errOut, err := run(t, fa, nil, "--plain", "decode", f.r1, "-o", f.dir)
	if ExitCode(err) != 3 || !strings.Contains(errOut, "2.5% short: record one more loop and pass both files") {
		t.Errorf("exit %d, stderr %q", ExitCode(err), errOut)
	}
}

func TestErrorsAreSanitised(t *testing.T) {
	f := newFiles(t)
	fa := &fakeAPI{err: &qrvt.UnsafeArchiveError{Path: "\x1b]0;pwned\x07\u202eevil", Reason: "\x1b[2Jbad"}}
	out, errOut, err := run(t, fa, nil, "decode", f.r1, "-o", f.dir)
	if ExitCode(err) != 5 {
		t.Errorf("exit code %d", ExitCode(err))
	}
	for _, s := range []string{out, errOut} {
		if strings.ContainsAny(s, "\x1b\x07\u202e") {
			t.Errorf("control characters printed: %q", s)
		}
	}
	if !strings.Contains(errOut, "Nothing was written") {
		t.Errorf("no guidance: %q", errOut)
	}
}

// jsonLines decodes NDJSON output into objects with their type, message, guidance and exit code.
type jsonLine struct {
	Type, Message, Guidance string
	Exit                    int
	Warning                 *struct{ Code, Text string }
}

func jsonLines(t *testing.T, out string) []jsonLine {
	t.Helper()
	var r []jsonLine
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		var j jsonLine
		if err := json.Unmarshal([]byte(l), &j); err != nil {
			t.Fatalf("line %q: %v", l, err)
		}
		r = append(r, j)
	}
	return r
}

// TestJSONFailure checks that with --json every failure, usage errors included, is exactly one
// error line on stdout, and that nothing is written on stderr.
func TestJSONFailure(t *testing.T) {
	f := newFiles(t)
	tests := []struct {
		name  string
		fa    *fakeAPI
		args  []string
		exit  int
		guide string
	}{
		{"job error", &fakeAPI{err: qrvt.ErrWrongPassphrase}, []string{"--json", "decode", f.r1, "-o", f.dir}, 4, "passphrase"},
		{"usage error", &fakeAPI{}, []string{"--json", "encode", "-o", "o.mp4"}, 2, ""},
		{"flag error", &fakeAPI{}, []string{"--json", "decode", f.r1, "--bogus"}, 2, ""},
		{"flag before --json", &fakeAPI{}, []string{"inspect", "--jobs", "x", "--json"}, 2, ""},
		{"output flags", &fakeAPI{}, []string{"--json", "--plain", "inspect", f.r1}, 2, ""},
		{"keygen error", &fakeAPI{err: qrvt.ErrNotImplemented}, []string{"--json", "keygen", "-o", filepath.Join(f.dir, "k")}, 1, "not implemented"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, errOut, err := run(t, tc.fa, nil, tc.args...)
			if ExitCode(err) != tc.exit {
				t.Fatalf("exit %d, want %d (%v)", ExitCode(err), tc.exit, err)
			}
			if errOut != "" {
				t.Errorf("stderr in JSON mode: %q", errOut)
			}
			errs := 0
			ls := jsonLines(t, out)
			for _, l := range ls {
				if l.Type == "error" {
					errs++
				}
			}
			last := ls[len(ls)-1]
			if errs != 1 || last.Type != "error" || last.Exit != tc.exit || last.Message == "" || !strings.Contains(last.Guidance, tc.guide) {
				t.Errorf("output %q", out)
			}
		})
	}
}

func TestJSONWarningGoesThroughRenderer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not meaningful on Windows")
	}
	f := newFiles(t)
	if err := os.Chmod(f.priv, 0o644); err != nil { //nolint:gosec // the test needs a key readable by others
		t.Fatal(err)
	}
	out, errOut, err := run(t, &fakeAPI{}, nil, "--json", "encode", f.a, "-o", "o.mp4", "--sign-key", f.priv)
	if err != nil || errOut != "" {
		t.Fatalf("%v, stderr %q", err, errOut)
	}
	found := false
	for _, l := range jsonLines(t, out) {
		found = found || l.Warning != nil && strings.Contains(l.Warning.Text, "readable by others")
	}
	if !found {
		t.Errorf("no warning event: %s", out)
	}
}

func TestSecretValuesNotEchoed(t *testing.T) {
	f := newFiles(t)
	for _, args := range [][]string{
		{"decode", f.r1, "-o", f.dir, "--passphrase-file", "hunter2 secret"},
		{"encode", f.a, "-o", "o.mp4", "--encrypt", "--passphrase-file", "hunter2 secret"},
		{"decode", f.r1, "-o", f.dir, "--verify-key", "hunter2 secret"},
		{"decode", f.r1, "-o", f.dir, "--verify-key", "qrvt-public-key-v1:hunter2"},
		{"decode", f.r1, "-o", f.dir, "--verify-key", "qrvt-private-key-v1:hunter2"},
	} {
		out, errOut, err := run(t, &fakeAPI{}, nil, args...)
		if ExitCode(err) != 2 || strings.Contains(out+errOut+err.Error(), "hunter2") {
			t.Errorf("%v: exit %d, echoed: %q", args, ExitCode(err), errOut)
		}
	}
}

func TestPassphraseFileContent(t *testing.T) {
	f := newFiles(t)
	write := func(name, s string) string {
		p := filepath.Join(f.dir, name)
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	long := write("long", strings.Repeat("a", maxSecretFile+10))
	exact := write("exact", strings.Repeat("a", maxSecretFile))
	for _, tc := range []struct{ path, want string }{
		{long, "the first line is longer than 4096 bytes"},
		{exact, "the first line is longer than 4096 bytes"},
		{write("nul", "pass\x00word\n"), "the first line holds control characters"},
		{write("esc", "pass\x1b[2Jword"), "the first line holds control characters"},
		{write("bin", "\x01\x02\x03"), "the first line holds control characters"},
		{write("bomonly", "\ufeff\n"), "the first line is empty"},
	} {
		_, _, err := run(t, &fakeAPI{askPass: true}, nil, "decode", f.r1, "-o", f.dir, "--passphrase-file", tc.path)
		if err == nil || !strings.Contains(err.Error(), tc.want) || ExitCode(err) != 2 {
			t.Errorf("%s: %v", filepath.Base(tc.path), err)
		}
	}
	fa := &fakeAPI{askPass: true}
	bom := write("bom", "\ufeffcorrect horse\r\n")
	if _, _, err := run(t, fa, nil, "decode", f.r1, "-o", f.dir, "--passphrase-file", bom); err != nil || fa.pass[0] != "correct horse" {
		t.Errorf("BOM not stripped: %v %q", err, fa.pass)
	}
	fa = &fakeAPI{askPass: true}
	ok := write("ok", strings.Repeat("b", maxSecretFile-1)+"\n")
	if _, _, err := run(t, fa, nil, "decode", f.r1, "-o", f.dir, "--passphrase-file", ok); err != nil || len(fa.pass[0]) != maxSecretFile-1 {
		t.Errorf("a passphrase of the largest length was refused: %v", err)
	}
}

func TestResolutionHelpNames2160p(t *testing.T) {
	for _, args := range [][]string{{"encode", "--help"}, {"calibrate", "make", "--help"}} {
		out, _, _ := run(t, &fakeAPI{}, nil, args...)
		if !strings.Contains(out, "2160p") {
			t.Errorf("%v does not name 2160p", args)
		}
	}
}

func TestOutputModes(t *testing.T) {
	f := newFiles(t)
	events := ui.DemoEvents(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	t.Run("plain when not a terminal", func(t *testing.T) {
		out, errOut, err := run(t, &fakeAPI{events: events}, nil, "decode", f.r1, "-o", f.dir)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out+errOut, "\x1b") || !strings.Contains(errOut, "decode-video") || !strings.Contains(out, "Decoded 3 files") {
			t.Errorf("stdout %q\nstderr %q", out, errOut)
		}
	})
	t.Run("dashboard on a terminal", func(t *testing.T) {
		out, _, err := run(t, &fakeAPI{events: events}, &fakeTerm{out: true}, "decode", f.r1, "-o", f.dir)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "\x1b[") || !strings.Contains(out, "Signal") || !strings.Contains(out, "Decoded 3 files") {
			t.Errorf("no dashboard: %q", out)
		}
	})
	t.Run("plain forced", func(t *testing.T) {
		out, _, err := run(t, &fakeAPI{events: events}, &fakeTerm{out: true}, "--plain", "decode", f.r1, "-o", f.dir)
		if err != nil || strings.Contains(out, "\x1b") {
			t.Errorf("%v %q", err, out)
		}
	})
	t.Run("json", func(t *testing.T) {
		out, errOut, err := run(t, &fakeAPI{events: events}, &fakeTerm{out: true}, "--json", "decode", f.r1, "-o", f.dir)
		if err != nil || errOut != "" {
			t.Fatalf("%v %q", err, errOut)
		}
		ls := strings.Split(strings.TrimSpace(out), "\n")
		if len(ls) != len(events)+2 {
			t.Errorf("%d lines for %d events", len(ls), len(events))
		}
	})
	t.Run("quiet", func(t *testing.T) {
		out, errOut, err := run(t, &fakeAPI{events: events}, &fakeTerm{out: true}, "-q", "decode", f.r1, "-o", f.dir)
		if err != nil || out != "" || errOut != "" {
			t.Errorf("%v %q %q", err, out, errOut)
		}
	})
	t.Run("verbose", func(t *testing.T) {
		_, errOut, err := run(t, &fakeAPI{events: events}, nil, "-v", "decode", f.r1, "-o", f.dir)
		if err != nil || !strings.Contains(errOut, "stats:") {
			t.Errorf("%v %q", err, errOut)
		}
	})
}

func TestEncodeSummaries(t *testing.T) {
	f := newFiles(t)
	out, _, err := run(t, &fakeAPI{}, nil, "encode", f.a, "-o", "o.mp4", "--dry-run", "--resolution", "1000x700", "--cell", "12")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Dry run", "996x696", "reduced from 1000x700", "Loop length", "1:10", "Video size", "85.0 MiB", "Filming time", "2:20"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run summary lacks %q:\n%s", want, out)
		}
	}
	out, _, err = run(t, &fakeAPI{}, nil, "encode", f.a, "-o", "o.mp4")
	if err != nil || !strings.Contains(out, "Wrote o.mp4") || strings.Contains(out, "reduced") {
		t.Errorf("%v\n%s", err, out)
	}
	rep := &qrvt.EncodeReport{Output: "x.mp4", Profile: defaultProfile, Blocks: 2, K: 100, M: 20,
		Warnings: []qrvt.Warning{{Code: qrvt.WarnDownscaled, Text: "made \x1b[31msmaller"}}}
	out, errOut, err := run(t, &fakeAPI{encRep: rep}, nil, "encode", f.a, "-o", "x.mp4")
	if err != nil || !strings.Contains(out, "2 x (100 + 20)") || !strings.Contains(errOut, "made smaller") {
		t.Errorf("%v\n%s\n%s", err, out, errOut)
	}
}

func TestDecodeSignatureSummary(t *testing.T) {
	f := newFiles(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = 1
	}
	tests := []struct {
		rep  qrvt.DecodeReport
		args []string
		want string
	}{
		{qrvt.DecodeReport{Files: 1}, nil, "unsigned"},
		{qrvt.DecodeReport{Files: 1, Signed: true, SignerKey: key}, nil, "signed by " + testFP + "; not checked against a trusted key"},
		{qrvt.DecodeReport{Files: 1, Signed: true, SignerKey: key}, []string{"--verify-key", f.pub}, "verified, signed by " + testFP},
		{qrvt.DecodeReport{Files: 1, Encrypted: true}, nil, "with a passphrase"},
		{qrvt.DecodeReport{Files: 1, Warnings: []qrvt.Warning{{Code: qrvt.WarnSkippedFile, Text: "skipped \x1b[31mx"}}}, nil, "skipped x"},
	}
	for _, tc := range tests {
		rep := tc.rep
		args := append([]string{"decode", f.r1, "-o", f.dir}, tc.args...)
		out, errOut, err := run(t, &fakeAPI{decRep: &rep}, nil, args...)
		if err != nil || !strings.Contains(out+errOut, tc.want) {
			t.Errorf("%v: want %q in\n%s%s", err, tc.want, out, errOut)
		}
	}
}

func TestInspectAndCalibrateSummaries(t *testing.T) {
	f := newFiles(t)
	out, _, err := run(t, &fakeAPI{}, nil, "inspect", f.r1)
	for _, want := range []string{"Session", "0000000000000abc", "1920x1080", "480x270", "gray4", "normal", "840", "12 fps", "1.0 MiB", "128 bytes", "1 x (8192 + 1639)"} {
		if err != nil || !strings.Contains(out, want) {
			t.Errorf("%v: inspect lacks %q:\n%s", err, want, out)
		}
	}
	out, _, err = run(t, &fakeAPI{}, nil, "calibrate", "read", f.r1)
	for _, want := range []string{"qrvt encode --resolution 1440p --fps 30", "2560x1440", "488.3 KiB/s"} {
		if err != nil || !strings.Contains(out, want) {
			t.Errorf("%v: calibrate read lacks %q:\n%s", err, want, out)
		}
	}
	out, _, err = run(t, &fakeAPI{}, nil, "calibrate", "make", "-o", "cal.mp4")
	if err != nil || !strings.Contains(out, "Wrote cal.mp4") {
		t.Errorf("%v %q", err, out)
	}
	for _, args := range [][]string{{"inspect", f.r1}, {"calibrate", "read", f.r1}, {"calibrate", "make", "-o", "c.mp4"}, {"encode", f.a, "-o", "o.mp4"}} {
		if _, _, err := run(t, &fakeAPI{err: qrvt.ErrNotImplemented}, nil, args...); !errors.Is(err, qrvt.ErrNotImplemented) {
			t.Errorf("%v: error %v", args, err)
		}
	}
}

func TestPassphrases(t *testing.T) {
	f := newFiles(t)
	t.Run("encode prompts twice", func(t *testing.T) {
		fa := &fakeAPI{askPass: true}
		tm := &fakeTerm{in: true, answers: []string{"pw", "pw"}}
		if _, _, err := run(t, fa, tm, "encode", f.a, "-o", "o.mp4", "--encrypt"); err != nil {
			t.Fatal(err)
		}
		if len(tm.prompts) != 2 || strings.Join(fa.pass, ",") != "pw,pw" {
			t.Errorf("prompts %q, passphrases %q", tm.prompts, fa.pass)
		}
	})
	t.Run("encode mismatch", func(t *testing.T) {
		_, _, err := run(t, &fakeAPI{}, &fakeTerm{in: true, answers: []string{"a", "b"}}, "encode", f.a, "-o", "o.mp4", "--encrypt")
		if err == nil || !strings.Contains(err.Error(), "the passphrases do not match") {
			t.Errorf("error %v", err)
		}
	})
	t.Run("encode empty", func(t *testing.T) {
		_, _, err := run(t, &fakeAPI{}, &fakeTerm{in: true, answers: []string{""}}, "encode", f.a, "-o", "o.mp4", "--encrypt")
		if err == nil || !strings.Contains(err.Error(), "the passphrase is empty") {
			t.Errorf("error %v", err)
		}
	})
	t.Run("encode prompt fails", func(t *testing.T) {
		_, _, err := run(t, &fakeAPI{}, &fakeTerm{in: true}, "encode", f.a, "-o", "o.mp4", "--encrypt")
		if err == nil || !strings.Contains(err.Error(), "no more answers") {
			t.Errorf("error %v", err)
		}
	})
	t.Run("encode no terminal", func(t *testing.T) {
		fa := &fakeAPI{}
		_, _, err := run(t, fa, nil, "encode", f.a, "-o", "o.mp4", "--encrypt")
		if err == nil || !strings.Contains(err.Error(), "use --passphrase-file") || fa.enc != nil {
			t.Errorf("error %v", err)
		}
	})
	t.Run("file first line", func(t *testing.T) {
		fa := &fakeAPI{askPass: true}
		if _, _, err := run(t, fa, nil, "decode", f.r1, "-o", f.dir, "--passphrase-file", f.pass); err != nil {
			t.Fatal(err)
		}
		if strings.Join(fa.pass, ",") != "correct horse,correct horse" {
			t.Errorf("passphrases %q", fa.pass)
		}
	})
	t.Run("file empty", func(t *testing.T) {
		empty := filepath.Join(f.dir, "empty")
		if err := os.WriteFile(empty, []byte("\nsecond"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, err := run(t, &fakeAPI{askPass: true}, nil, "decode", f.r1, "-o", f.dir, "--passphrase-file", empty)
		if err == nil || !strings.Contains(err.Error(), "first line is empty") {
			t.Errorf("error %v", err)
		}
	})
	t.Run("decode prompts once, when needed", func(t *testing.T) {
		fa := &fakeAPI{askPass: true}
		tm := &fakeTerm{in: true, answers: []string{"pw"}}
		if _, _, err := run(t, fa, tm, "decode", f.r1, "-o", f.dir); err != nil {
			t.Fatal(err)
		}
		if len(tm.prompts) != 1 || strings.Join(fa.pass, ",") != "pw,pw" {
			t.Errorf("prompts %q, passphrases %q", tm.prompts, fa.pass)
		}
		tm = &fakeTerm{in: true}
		if _, _, err := run(t, &fakeAPI{}, tm, "decode", f.r1, "-o", f.dir); err != nil || len(tm.prompts) != 0 {
			t.Errorf("prompted for an unencrypted transfer: %v %q", err, tm.prompts)
		}
	})
	t.Run("decode dashboard pauses for the prompt", func(t *testing.T) {
		fa := &fakeAPI{askPass: true}
		tm := &fakeTerm{in: true, out: true, answers: []string{"pw"}}
		if _, _, err := run(t, fa, tm, "decode", f.r1, "-o", f.dir); err != nil {
			t.Fatal(err)
		}
		if strings.Join(fa.pass, ",") != "pw,pw" {
			t.Errorf("passphrases %q", fa.pass)
		}
	})
	t.Run("decode no terminal", func(t *testing.T) {
		_, _, err := run(t, &fakeAPI{askPass: true}, nil, "decode", f.r1, "-o", f.dir)
		if err == nil || !strings.Contains(err.Error(), "the transfer is encrypted") {
			t.Errorf("error %v", err)
		}
	})
}

func TestKeygen(t *testing.T) {
	f := newFiles(t)
	name := filepath.Join(f.dir, "carol")
	out, _, err := run(t, &fakeAPI{}, nil, "keygen", "-o", name)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, testFP) || !strings.Contains(out, name+".key") || !strings.Contains(out, name+".pub") {
		t.Errorf("output %q", out)
	}
	for ext, want := range map[string]string{".key": testPriv + "\n", ".pub": testPub + "\n"} {
		b, err := os.ReadFile(name + ext)
		if err != nil || string(b) != want {
			t.Errorf("%s: %q %v", ext, b, err)
		}
	}
	if runtime.GOOS != "windows" {
		for ext, want := range map[string]os.FileMode{".key": 0o600, ".pub": 0o644} {
			st, err := os.Stat(name + ext)
			if err != nil || st.Mode().Perm() != want {
				t.Errorf("%s mode %v, want %v (%v)", ext, st.Mode().Perm(), want, err)
			}
		}
	}
	if _, _, err := run(t, &fakeAPI{}, nil, "keygen", "-o", name); err == nil || !strings.Contains(err.Error(), name+".key already exists") {
		t.Errorf("overwrote a key: %v", err)
	}
	if err := os.Remove(name + ".key"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, &fakeAPI{}, nil, "keygen", "-o", name); err == nil || !strings.Contains(err.Error(), name+".pub already exists") {
		t.Errorf("overwrote a public key: %v", err)
	}
	if _, err := os.Stat(name + ".key"); !errors.Is(err, os.ErrNotExist) {
		t.Error("a private key was left behind by a failed keygen")
	}
	if _, _, err := run(t, &fakeAPI{err: qrvt.ErrNotImplemented}, nil, "keygen", "-o", filepath.Join(f.dir, "x")); !errors.Is(err, qrvt.ErrNotImplemented) {
		t.Errorf("error %v", err)
	}
	if _, _, err := run(t, &fakeAPI{}, nil, "keygen", "-o", filepath.Join(f.dir, "nodir", "x")); err == nil {
		t.Error("keygen into a missing folder succeeded")
	}
	out, _, err = run(t, &fakeAPI{pub: []byte("odd"), priv: []byte("odd2")}, nil, "keygen", "-o", filepath.Join(f.dir, "odd"))
	if err != nil || !strings.Contains(out, "unknown: the public key") {
		t.Errorf("%v %q", err, out)
	}
}

func TestSignKeyPermissionWarning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not meaningful on Windows")
	}
	f := newFiles(t)
	if err := os.Chmod(f.priv, 0o644); err != nil { //nolint:gosec // the test needs a key readable by others
		t.Fatal(err)
	}
	_, errOut, err := run(t, &fakeAPI{}, nil, "encode", f.a, "-o", "o.mp4", "--sign-key", f.priv)
	if err != nil || !strings.Contains(errOut, "readable by others") {
		t.Errorf("%v %q", err, errOut)
	}
	if err := os.Chmod(f.priv, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, errOut, _ = run(t, &fakeAPI{}, nil, "encode", f.a, "-o", "o.mp4", "--sign-key", f.priv); strings.Contains(errOut, "readable") {
		t.Errorf("warned about an owner-only key: %q", errOut)
	}
}

func TestHelp(t *testing.T) {
	out, _, err := run(t, &fakeAPI{}, nil, "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"encode", "decode", "calibrate", "inspect", "keygen", "profiles", "version", "completion"} {
		if !strings.Contains(out, c) {
			t.Errorf("--help lacks %s", c)
		}
	}
	if strings.Contains(out, "demo") {
		t.Error("the demo command is not hidden")
	}
	for _, args := range [][]string{{"encode", "--help"}, {"decode", "-h"}, {"calibrate", "make", "--help"}, {"calibrate", "read", "--help"}, {"inspect", "--help"}, {"keygen", "--help"}} {
		out, _, err := run(t, &fakeAPI{}, nil, args...)
		if err != nil || !strings.Contains(strings.ToLower(out), "usage") {
			t.Errorf("%v: %v\n%s", args, err, out)
		}
	}
	out, _, _ = run(t, &fakeAPI{}, nil, "decode", "--help")
	for _, want := range []string{"recordings you made yourself", "outside the sandbox", "not bounded against a crafted stream"} {
		if !strings.Contains(out, want) {
			t.Errorf("decode --help lacks %q", want)
		}
	}
	out, _, _ = run(t, &fakeAPI{}, nil, "calibrate", "--help")
	if !strings.Contains(out, "make") || !strings.Contains(out, "read") {
		t.Errorf("calibrate --help: %s", out)
	}
	out, _, _ = run(t, &fakeAPI{}, nil, "calibrate")
	if !strings.Contains(out, "make") {
		t.Errorf("calibrate alone does not show help: %s", out)
	}
}

// TestFlagHelp checks that every flag has a description with an example, and that the two flags
// that leave the sandbox say so (SECURITY.md section 4).
func TestFlagHelp(t *testing.T) {
	root := newRoot(&Env{API: &fakeAPI{}})
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		c.LocalFlags().VisitAll(func(fl *pflag.Flag) {
			if fl.Name == "help" || fl.Hidden {
				return
			}
			if !strings.Contains(fl.Usage, "Example: ") {
				t.Errorf("%s --%s: no example in %q", c.CommandPath(), fl.Name, fl.Usage)
			}
			var musts []string
			switch fl.Name {
			case "unsandboxed-hevc":
				musts = []string{"recordings you made yourself", "outside the sandbox", "memory is not bounded against a crafted stream"}
			case "ffmpeg":
				musts = []string{"recordings you made yourself", "outside the sandbox"}
			}
			for _, m := range musts {
				if !strings.Contains(fl.Usage, m) {
					t.Errorf("%s --%s does not say %q", c.CommandPath(), fl.Name, m)
				}
			}
		})
		for _, s := range c.Commands() {
			walk(s)
		}
	}
	walk(root)
}

func TestVersionManCompletionProfiles(t *testing.T) {
	out, _, err := run(t, &fakeAPI{}, nil, "version")
	if err != nil || !strings.HasPrefix(out, "qrvt 1.2.3") {
		t.Errorf("version: %v %q", err, out)
	}
	out, _, err = run(t, &fakeAPI{}, nil, "--version")
	if err != nil || !strings.Contains(out, "1.2.3") {
		t.Errorf("--version: %v %q", err, out)
	}
	out, _, err = run(t, &fakeAPI{}, nil, "man")
	if err != nil || !strings.Contains(out, ".TH") || !strings.Contains(out, "unsandboxed") {
		t.Errorf("man: %v %.200q", err, out)
	}
	out, _, err = run(t, &fakeAPI{}, nil, "completion", "bash")
	if err != nil || !strings.Contains(out, "bash completion") {
		t.Errorf("completion: %v %.200q", err, out)
	}
	out, _, err = run(t, &fakeAPI{}, nil, "profiles")
	for _, want := range []string{"480p", "848x480", "4k", "3840x2160", "gray4", "bw", "color", "fast", "normal", "robust", "15.6 KiB"} {
		if err != nil || !strings.Contains(out, want) {
			t.Errorf("profiles lacks %q: %v\n%s", want, err, out)
		}
	}
}

func TestDemo(t *testing.T) {
	out, errOut, err := run(t, &fakeAPI{}, nil, "demo", "--delay", "0s")
	if err != nil || !strings.Contains(out, ui.DemoSummary().Title) || !strings.Contains(errOut, "unpack") {
		t.Errorf("%v\n%s\n%s", err, out, errOut)
	}
	out, _, err = run(t, &fakeAPI{}, &fakeTerm{out: true}, "demo", "--delay", "1ms")
	if err != nil || !strings.Contains(out, "Signal") {
		t.Errorf("%v %q", err, out)
	}
	if _, _, err := run(t, &fakeAPI{}, nil, "demo", "--delay", "-1s"); ExitCode(err) != 2 {
		t.Errorf("negative delay: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	env := &Env{API: &fakeAPI{}, In: strings.NewReader(""), Out: &strings.Builder{}, Err: &strings.Builder{}}
	if err := Execute(ctx, env, []string{"demo", "--delay", "1s"}); ExitCode(err) != 130 {
		t.Errorf("cancelled demo: %v", err)
	}
}

func TestNewEnv(t *testing.T) {
	env := NewEnv("v9")
	if env.Version != "v9" || env.In != os.Stdin || env.Out != os.Stdout || env.Err != os.Stderr || env.API == nil {
		t.Errorf("%+v", env)
	}
	if env.IsTerminal(strings.NewReader("")) {
		t.Error("a reader is a terminal")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	if env.IsTerminal(r) {
		t.Error("a pipe is a terminal")
	}
	env.In = r
	if _, err := env.ReadPassword("x"); err == nil {
		t.Error("read a password from a pipe")
	}
	// Empty options must fail, whether the root package is implemented yet or not.
	ctx := context.Background()
	if _, err := (Root{}).Encode(ctx, qrvt.EncodeOptions{}); err == nil {
		t.Error("Root.Encode accepted empty options")
	}
	if _, err := (Root{}).Decode(ctx, qrvt.DecodeOptions{}); err == nil {
		t.Error("Root.Decode accepted empty options")
	}
	if err := (Root{}).CalibrationVideo(ctx, qrvt.CalibrationOptions{}); err == nil {
		t.Error("Root.CalibrationVideo accepted empty options")
	}
	if _, err := (Root{}).AnalyzeCalibration(ctx, qrvt.AnalyzeOptions{}); err == nil {
		t.Error("Root.AnalyzeCalibration accepted empty options")
	}
	if _, err := (Root{}).Inspect(ctx, qrvt.InspectOptions{}); err == nil {
		t.Error("Root.Inspect accepted empty options")
	}
	_, _, _ = (Root{}).GenerateKey()
}
