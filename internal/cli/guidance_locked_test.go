package cli

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	qrvt "github.com/platten/qrvideotransfer"
)

// TestLockedRateGuidanceText: a shortfall with the locked rate set asks for a camera at least twice
// the display rate; the same shortfall without it gets the generic action (DESIGN.md section 13).
// The always-on check of the locked-rate guidance; the filmed case is internal/pipeline's
// TestLockedRateGuidance, long only.
func TestLockedRateGuidanceText(t *testing.T) {
	one := &qrvt.DecodeOptions{Recordings: []string{"a"}}
	e := qrvt.NotEnoughDataError{Blocks: []qrvt.Shortfall{{Have: 0, Need: 27}}, Captures: 8, Located: 8, HeadersRead: 8,
		FramesSeen: 4, FramesTotal: 4, Descriptor: true}
	generic, locked := e, e
	locked.LockedRate = true
	for _, c := range []struct {
		err       *qrvt.NotEnoughDataError
		want, not string
	}{
		{&locked, "Film again with a camera at least twice the display rate", "Record one more loop"},
		{&generic, "Record one more loop and pass both files to qrvt decode.", "twice the display rate"},
	} {
		_, text := splitGuidance(guide(c.err, &qrvt.Stats{BlocksTotal: 1, CleanFraction: 0.35}, one))
		if !strings.Contains(text, c.want) || strings.Contains(text, c.not) {
			t.Errorf("locked rate %v: %q, want %q and not %q", c.err.LockedRate, text, c.want, c.not)
		}
	}
}

// TestFailureJSONUnchanged: for the failure classes that existed before the guidance by class,
// --json's error line keeps its fields (type, message, guidance, exit), its message and its exit
// code; only the guidance text of a shortfall naming blocks and of an unsupported version changed.
// The other classes' guidance is pinned to its text before (main at 9e4f53a), byte for byte; "-"
// marks the two that changed, and "" an error line without guidance.
func TestFailureJSONUnchanged(t *testing.T) {
	f := newFiles(t)
	for _, c := range []struct {
		err      error
		exit     int
		guidance string
	}{
		{&qrvt.NotEnoughDataError{Blocks: []qrvt.Shortfall{{Have: 1, Need: 4}}}, 3, "-"},
		{&qrvt.NotEnoughDataError{}, 3, "Record one more loop and pass both files to qrvt decode."},
		{&qrvt.UnsupportedVersionError{What: "stream version", Value: 9}, 5, "-"},
		{&qrvt.UnsafeArchiveError{Path: "../x", Reason: "leaves the folder"}, 5,
			"Nothing was written. The archive tries to do something unsafe; do not trust its sender."},
		{&qrvt.LimitError{Limit: "unpacked size", Value: 2, Max: 1, Flag: "--max-size"}, 5,
			"If you expect a transfer this large, raise the limit with --max-size."},
		{qrvt.ErrWrongPassphrase, 4, "Check the passphrase (and the --passphrase-file, if you used one) and try again."},
		{qrvt.ErrBadSignature, 4, "The video was altered or damaged after it was signed. Nothing was decrypted or unpacked."},
		{qrvt.ErrMissingSignature, 4, "The transfer is not signed by any key given with --verify-key. Nothing was decrypted or " +
			"unpacked. Check that you have the sender's current public key."},
		{errors.New("disk full"), 1, ""},
	} {
		out, _, err := run(t, &fakeAPI{err: c.err}, nil, "--json", "decode", f.r1, "-o", f.dir)
		if ExitCode(err) != c.exit {
			t.Errorf("%v: exit %d, want %d", c.err, ExitCode(err), c.exit)
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		var line map[string]any
		if err := json.Unmarshal([]byte(lines[len(lines)-1]), &line); err != nil {
			t.Fatalf("%v: %v", c.err, err)
		}
		keys := slices.Sorted(maps.Keys(line))
		want := []string{"exit", "guidance", "message", "type"}
		if c.exit == 1 {
			want = []string{"exit", "message", "type"} // no guidance for an error it knows nothing about
		}
		if !slices.Equal(keys, want) || line["type"] != "error" || line["message"] != c.err.Error() || line["exit"] != float64(c.exit) {
			t.Errorf("%v: error line %v, want fields %v, message %q, exit %d", c.err, line, want, c.err.Error(), c.exit)
		}
		if g, _ := line["guidance"].(string); c.guidance != "-" && g != c.guidance {
			t.Errorf("%v: guidance %q, want it unchanged, %q", c.err, g, c.guidance)
		}
	}
}
