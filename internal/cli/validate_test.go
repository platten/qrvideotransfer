package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// TestFlagValidation checks that every bad flag value is refused with a precise usage error (exit
// code 2) before the root API is called.
func TestFlagValidation(t *testing.T) {
	f := newFiles(t)
	enc := func(extra ...string) []string { return append([]string{"encode", f.a, "-o", "o.mp4"}, extra...) }
	dec := func(extra ...string) []string { return append([]string{"decode", f.r1, "-o", f.dir}, extra...) }
	missing := filepath.Join(f.dir, "missing")
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"encode", "-o", "o.mp4"}, "encode needs at least one file or folder"},
		{[]string{"encode", f.a}, "--output is required"},
		{[]string{"encode", f.a, "-o", ""}, "--output is required"},
		{[]string{"encode", missing, "-o", "o.mp4"}, "input " + missing},
		{enc("--resolution", "9999"), `--resolution "9999": use a preset (480p, 720p, 1080p, 1440p, 4k) or WxH`},
		{enc("--resolution", "0x0"), `--resolution "0x0": use a preset`},
		{enc("--resolution", "1920x-4"), `--resolution "1920x-4": use a preset`},
		{enc("--resolution", "640x400"), "--resolution 640x400: the height must be at least 480"},
		{enc("--resolution", "9000x2000"), "--resolution 9000x2000: 2250 cells across; the most is 2047, use a larger --cell"},
		{enc("--resolution", "2000x9000"), "--resolution 2000x9000: 2250 cells down; the most is 2047, use a larger --cell"},
		{enc("--resolution", "480x7400", "--cell", "4"), "--resolution 480x7400: the height must be less than 15 times the width"},
		{enc("--fps", "0"), "--fps 0: use 1 to 240"},
		{enc("--fps", "241"), "--fps 241: use 1 to 240"},
		{enc("--fps", "abc"), `invalid argument "abc" for "--fps"`},
		{enc("--mode", "grey"), `--mode "grey": use gray4, bw or color`},
		{enc("--protection", "max"), `--protection "max": use fast, normal or robust`},
		{enc("--compress", "gzip"), `--compress "gzip": use auto, zstd, xz or none`},
		{enc("--parity", "4"), "--parity 4: use 5 to 100"},
		{enc("--parity", "101"), "--parity 101: use 5 to 100"},
		{enc("--cell", "5"), "--cell 5: use 4, 8, 12 or 16"},
		{enc("--hdr", "--mode", "bw"), "--hdr selects eight greys; leave out --mode"},
		{enc("--passphrase-file", f.pass), "--passphrase-file needs --encrypt"},
		{enc("--jobs", "-1"), "--jobs -1: use 0 (the CPU count) to 1024"},
		{enc("--jobs", "1025"), "--jobs 1025: use 0 (the CPU count) to 1024"},
		{enc("--ffmpeg", ""), "--ffmpeg: use embedded, system or the path of an ffmpeg program"},
		{enc("--ffmpeg", missing), "--ffmpeg " + missing},
		{enc("--ffmpeg", f.d), "--ffmpeg " + f.d + ": is a folder"},
		{enc("--sign-key", missing), "--sign-key: no such file (the value is not shown"},
		{enc("--sign-key", testPriv), "--sign-key: give the path of the key file, not the key"},
		{enc("--sign-key", f.pub), "--sign-key: not a qrvt private key (the value is not shown"},
		{enc("--encrypt", "--passphrase-file", missing), "--passphrase-file: no such file (the value is not shown"},
		{[]string{"decode", "-o", f.dir}, "decode needs at least one recording"},
		{[]string{"decode", f.r1}, "--output is required"},
		{[]string{"decode", f.d, "-o", f.dir}, "recording " + f.d + ": is a folder"},
		{[]string{"decode", f.r1, "-o", f.a}, "--output " + f.a + ": not a folder"},
		{dec("--max-size", "0"), `--max-size "0": use a positive size such as 500MB or 16GiB`},
		{dec("--max-size", "12XB"), `--max-size "12XB": use a positive size such as 500MB or 16GiB`},
		{dec("--max-size", "-5"), `--max-size "-5": use a positive size`},
		{dec("--max-size", "99999999TiB"), `--max-size "99999999TiB": too large`},
		{dec("--verify-key", missing), "--verify-key: no such file, and not a qrvt public key (the value is not shown"},
		{dec("--verify-key", f.priv), "--verify-key: this is a private key; give the public key"},
		{dec("--verify-key", testPriv), "--verify-key: this is a private key; give the public key"},
		{dec("--verify-key", f.a), "--verify-key: not a qrvt public key"},
		{dec("--max-entries", "-1"), "--max-entries -1: use 1 to 1000000, or 0 for the default"},
		{dec("--max-entries", "1000001"), "--max-entries 1000001: use 1 to 1000000, or 0 for the default"},
		{dec("--verify-key", "qrvt-public-key-v1:"), "--verify-key: not a valid qrvt public key"},
		{dec("--report", filepath.Join(missing, "r.json")), "--report " + filepath.Join(missing, "r.json") + ": the folder"},
		{dec("--report", f.d), "--report " + f.d + ": is a folder"},
		{dec("--state-dir", f.a), "--state-dir " + f.a + ": not a folder"},
		{dec("--passphrase-file", missing), "--passphrase-file: no such file (the value is not shown"},
		{dec("--ffmpeg", "  "), "--ffmpeg: use embedded, system or the path"},
		{[]string{"--json", "--plain", "inspect", f.r1}, "--json and --plain cannot be used together"},
		{[]string{"--quiet", "--verbose", "inspect", f.r1}, "--quiet and --verbose cannot be used together"},
		{[]string{"--json", "--quiet", "inspect", f.r1}, "--json and --quiet cannot be used together"},
		{[]string{"inspect"}, "inspect needs one video or recording"},
		{[]string{"inspect", f.r1, f.r2}, "inspect needs one video or recording"},
		{[]string{"inspect", missing}, "recording " + missing},
		{[]string{"calibrate", "make"}, "--output is required"},
		{[]string{"calibrate", "make", "-o", "c.mp4", "--resolution", "huge"}, `--resolution "huge"`},
		{[]string{"calibrate", "make", "-o", "c.mp4", "--resolution", "1x1"}, "--resolution 1x1: the height must be at least 480"},
		{[]string{"calibrate", "make", "-o", "c.mp4", "--resolution", "9000x2000"}, "--resolution 9000x2000: 2250 cells across"},
		{[]string{"calibrate", "make", "-o", "c.mp4", "x"}, "calibrate make takes no arguments"},
		{[]string{"calibrate", "read"}, "calibrate read needs at least one recording"},
		{[]string{"calibrate", "bogus"}, `unknown command "bogus" for "qrvt calibrate"`},
		{[]string{"keygen"}, "--output is required"},
		{[]string{"keygen", "-o", "k", "extra"}, "keygen takes no arguments"},
		{[]string{"profiles", "x"}, "profiles takes no arguments"},
		{[]string{"version", "x"}, "version takes no arguments"},
		{[]string{"bogus"}, `unknown command "bogus" for "qrvt"`},
		{[]string{"encode", "--bogus"}, "unknown flag: --bogus"},
		{[]string{"decode", f.r1, "-o", f.dir, "--passphrase", "secret"}, "unknown flag: --passphrase"},
		{[]string{"encode", f.a, "-o", "o.mp4", "--password=secret"}, "unknown flag: --password"},
	}
	for _, tc := range tests {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			fa := &fakeAPI{}
			out, errOut, err := run(t, fa, nil, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want it to contain %q", err, tc.want)
			}
			shown := errOut // with --json, the error is a line on stdout instead
			if tc.args[0] == "--json" {
				shown = out
			}
			if !strings.Contains(shown, tc.want) {
				t.Errorf("output %q does not show the error", shown)
			}
			if code := ExitCode(err); code != 2 {
				t.Errorf("exit code %d, want 2", code)
			}
			if fa.enc != nil || fa.dec != nil || fa.cal != nil || fa.ana != nil || fa.ins != nil || fa.keygen {
				t.Error("the API was called despite the error")
			}
		})
	}
}

func TestParseSize(t *testing.T) {
	tests := map[string]int64{
		"1": 1, "1024": 1024, "1B": 1, "1k": 1024, "1K": 1024, "1KiB": 1024, "1kB": 1000, "1KB": 1000,
		"2M": 2 << 20, "2MiB": 2 << 20, "2MB": 2_000_000, "16GiB": 16 << 30, "16G": 16 << 30, "1GB": 1e9,
		"1T": 1 << 40, "1TiB": 1 << 40, "1TB": 1e12, "0.5GiB": 1 << 29, " 3 MiB ": 3 << 20, "1.5 gb": 1_500_000_000,
	}
	for s, want := range tests {
		got, err := parseSize(s)
		if err != nil || got != want {
			t.Errorf("parseSize(%q) = %d, %v; want %d", s, got, err, want)
		}
	}
	for _, s := range []string{"", "x", "1X", "-1", "0", "0.1B", "1e3", "NaN", "Inf", "9223372036854775808", "10000000TB", "1..5M"} {
		if got, err := parseSize(s); err == nil {
			t.Errorf("parseSize(%q) = %d, want an error", s, got)
		}
	}
}

func TestParseResolution(t *testing.T) {
	tests := map[string][2]int{
		"480p": {848, 480}, "720p": {1280, 720}, "1080p": {1920, 1080}, "1440p": {2560, 1440},
		"4k": {3840, 2160}, "4K": {3840, 2160}, "2160p": {3840, 2160}, "1920x1080": {1920, 1080}, "800X600": {800, 600},
	}
	for s, want := range tests {
		w, h, err := parseResolution(s)
		if err != nil || w != want[0] || h != want[1] {
			t.Errorf("parseResolution(%q) = %d, %d, %v", s, w, h, err)
		}
	}
	for _, s := range []string{"", "x", "1920", "1920x", "x1080", "axb", "0x1", "1x0", "-1x5", "99999x5", "5x99999", "1x2x3"} {
		if _, _, err := parseResolution(s); err == nil {
			t.Errorf("parseResolution(%q) accepted", s)
		}
	}
}

// TestNoPassphraseArgument checks that no command accepts a passphrase or password as an argument
// (SECURITY.md T10): the only passphrase flag names a file.
func TestNoPassphraseArgument(t *testing.T) {
	root := newRoot(&Env{API: &fakeAPI{}, In: strings.NewReader(""), Out: os.Stdout, Err: os.Stderr})
	seen := 0
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, fs := range []*pflag.FlagSet{c.Flags(), c.PersistentFlags()} {
			fs.VisitAll(func(fl *pflag.Flag) {
				name := strings.ToLower(fl.Name)
				if strings.Contains(name, "pass") || strings.Contains(name, "secret") || strings.Contains(name, "pwd") {
					if name != "passphrase-file" {
						t.Errorf("%s has flag --%s", c.CommandPath(), fl.Name)
					}
					seen++
				}
			})
		}
		for _, s := range c.Commands() {
			walk(s)
		}
	}
	walk(root)
	if seen != 2 {
		t.Errorf("--passphrase-file found on %d commands, want 2 (encode and decode)", seen)
	}
}
