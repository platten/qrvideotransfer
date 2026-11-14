package cli

import (
	"reflect"
	"testing"

	qrvt "github.com/platten/qrvideotransfer"
)

// defaultProfile is what encode uses without flags.
var defaultProfile = qrvt.Profile{Width: 1920, Height: 1080, Cell: 4, FPS: 12, Modulation: qrvt.Gray4, Protection: qrvt.Normal, ParityPercent: 20}

func profile(edit func(p *qrvt.Profile)) qrvt.Profile {
	p := defaultProfile
	edit(&p)
	return p
}

const gib = int64(1) << 30

// TestOptionMapping checks that every command and flag of DESIGN.md section 13 parses and reaches
// the root API as the right option.
func TestOptionMapping(t *testing.T) {
	f := newFiles(t)
	tests := []struct {
		name string
		args []string
		want any // the options the fake API must receive, with Progress and Passphrase left out
		pass bool
	}{
		{"encode defaults", []string{"encode", f.a, f.d, "-o", "out.mp4"},
			qrvt.EncodeOptions{Inputs: []string{f.a, f.d}, Output: "out.mp4", Profile: defaultProfile, Backend: "embedded"}, false},
		{"encode every flag", []string{"encode", f.a, "--output", "o.mp4", "--resolution", "1280x720", "--fps", "30", "--mode", "bw",
			"--protection", "robust", "--parity", "50", "--cell", "8", "--compress", "xz", "--dry-run", "--ffmpeg", "system", "--jobs", "3"},
			qrvt.EncodeOptions{Inputs: []string{f.a}, Output: "o.mp4", Backend: "system", Workers: 3, DryRun: true, Compression: qrvt.CompressXZ,
				Profile: qrvt.Profile{Width: 1280, Height: 720, Cell: 8, FPS: 30, Modulation: qrvt.Gray2, Protection: qrvt.Robust, ParityPercent: 50}}, false},
		{"encode 4k colour fast", []string{"encode", f.a, "-o", "o.mp4", "--resolution", "4K", "--mode", "color", "--protection", "fast", "--compress", "zstd"},
			qrvt.EncodeOptions{Inputs: []string{f.a}, Output: "o.mp4", Backend: "embedded", Compression: qrvt.CompressZstd,
				Profile: profile(func(p *qrvt.Profile) {
					p.Width, p.Height, p.Modulation, p.Protection = 3840, 2160, qrvt.RGB8, qrvt.Fast
				})}, false},
		{"encode presets", []string{"encode", f.a, "-o", "o.mp4", "--resolution", "480p", "--compress", "none", "--parity", "5"},
			qrvt.EncodeOptions{Inputs: []string{f.a}, Output: "o.mp4", Backend: "embedded", Compression: qrvt.CompressNone,
				Profile: profile(func(p *qrvt.Profile) { p.Width, p.Height, p.ParityPercent = 848, 480, 5 })}, false},
		{"encode 720p 1440p", []string{"encode", f.a, "-o", "o.mp4", "--resolution", "1440p", "--resolution", "720p", "--parity", "100"},
			qrvt.EncodeOptions{Inputs: []string{f.a}, Output: "o.mp4", Backend: "embedded",
				Profile: profile(func(p *qrvt.Profile) { p.Width, p.Height, p.ParityPercent = 1280, 720, 100 })}, false},
		{"encode size reduced to the cell", []string{"encode", f.a, "-o", "o.mp4", "--resolution", "1000x700", "--cell", "12"},
			qrvt.EncodeOptions{Inputs: []string{f.a}, Output: "o.mp4", Backend: "embedded",
				Profile: profile(func(p *qrvt.Profile) { p.Width, p.Height, p.Cell = 996, 696, 12 })}, false},
		{"encode cell 16 and 2160p", []string{"encode", f.a, "-o", "o.mp4", "--resolution", "2160p", "--cell", "16"},
			qrvt.EncodeOptions{Inputs: []string{f.a}, Output: "o.mp4", Backend: "embedded",
				Profile: profile(func(p *qrvt.Profile) { p.Width, p.Height, p.Cell = 3840, 2160, 16 })}, false},
		{"encode hdr", []string{"encode", f.a, "-o", "o.mp4", "--hdr"},
			qrvt.EncodeOptions{Inputs: []string{f.a}, Output: "o.mp4", Backend: "embedded",
				Profile: profile(func(p *qrvt.Profile) { p.Modulation = qrvt.Gray8 })}, false},
		{"encode encrypt sign", []string{"encode", f.a, "-o", "o.mp4", "--encrypt", "--passphrase-file", f.pass, "--sign-key", f.priv, "--ffmpeg", f.ffmpeg},
			qrvt.EncodeOptions{Inputs: []string{f.a}, Output: "o.mp4", Backend: f.ffmpeg, Profile: defaultProfile, SignKey: []byte(testPriv)}, true},
		{"decode defaults", []string{"decode", f.r1, f.r2, "-o", f.dir},
			qrvt.DecodeOptions{Recordings: []string{f.r1, f.r2}, OutputDir: f.dir, MaxSize: 16 * gib, Backend: "embedded"}, true},
		{"decode every flag", []string{"decode", f.r1, "-o", f.dir, "--state-dir", f.d, "--report", f.dir + "/report.json", "--overwrite",
			"--max-size", "2GiB", "--max-entries", "500000", "--verify-key", f.pub, "--verify-key", f.pub2, "--unsandboxed-hevc", "--jobs", "2",
			"--passphrase-file", f.pass, "--ffmpeg", "system"},
			qrvt.DecodeOptions{Recordings: []string{f.r1}, OutputDir: f.dir, StateDir: f.d, Report: f.dir + "/report.json", Overwrite: true,
				MaxSize: 2 * gib, MaxEntries: 500000, VerifyKeys: [][]byte{[]byte(testPub), []byte("qrvt-public-key-v1:AwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwM=")},
				UnsandboxedHEVC: true, Workers: 2, Backend: "system"}, true},
		{"decode inline key and decimal size", []string{"decode", f.r1, "-o", f.dir, "--verify-key", testPub, "--max-size", "1.5 GB"},
			qrvt.DecodeOptions{Recordings: []string{f.r1}, OutputDir: f.dir, MaxSize: 1_500_000_000, Backend: "embedded",
				VerifyKeys: [][]byte{[]byte(testPub)}}, true},
		{"calibrate make", []string{"calibrate", "make", "-o", "cal.mp4"},
			qrvt.CalibrationOptions{Output: "cal.mp4", Width: 1920, Height: 1080, Backend: "embedded"}, false},
		{"calibrate make flags", []string{"calibrate", "make", "-o", "cal.mp4", "--resolution", "2560x1440", "--ffmpeg", "system", "--jobs", "4"},
			qrvt.CalibrationOptions{Output: "cal.mp4", Width: 2560, Height: 1440, Backend: "system", Workers: 4}, false},
		{"calibrate read", []string{"calibrate", "read", f.r1, f.r2, "--unsandboxed-hevc", "--jobs", "1"},
			qrvt.AnalyzeOptions{Recordings: []string{f.r1, f.r2}, UnsandboxedHEVC: true, Workers: 1, Backend: "embedded"}, false},
		{"inspect", []string{"inspect", f.r1},
			qrvt.InspectOptions{Path: f.r1, Backend: "embedded"}, false},
		{"inspect flags", []string{"inspect", f.r1, "--unsandboxed-hevc", "--ffmpeg", "system", "--jobs", "8"},
			qrvt.InspectOptions{Path: f.r1, UnsandboxedHEVC: true, Backend: "system", Workers: 8}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fa := &fakeAPI{}
			args := append([]string{"--quiet"}, tc.args...)
			if _, errOut, err := run(t, fa, nil, args...); err != nil {
				t.Fatalf("%v\n%s", err, errOut)
			}
			var got any
			var progress bool
			var pass func() (string, error)
			switch {
			case fa.enc != nil:
				o := *fa.enc
				progress, pass = o.Progress != nil, o.Passphrase
				o.Progress, o.Passphrase = nil, nil
				got = o
			case fa.dec != nil:
				o := *fa.dec
				progress, pass = o.Progress != nil, o.Passphrase
				o.Progress, o.Passphrase = nil, nil
				got = o
			case fa.cal != nil:
				o := *fa.cal
				progress, o.Progress = o.Progress != nil, nil
				got = o
			case fa.ana != nil:
				o := *fa.ana
				progress, o.Progress = o.Progress != nil, nil
				got = o
			case fa.ins != nil:
				o := *fa.ins
				progress, o.Progress = o.Progress != nil, nil
				got = o
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("options\n got %+v\nwant %+v", got, tc.want)
			}
			if !progress {
				t.Error("no Progress callback")
			}
			if (pass != nil) != tc.pass {
				t.Errorf("Passphrase set %v, want %v", pass != nil, tc.pass)
			}
		})
	}
}
