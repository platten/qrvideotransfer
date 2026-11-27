package cli

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"unicode"

	qrvt "github.com/platten/qrvideotransfer"
)

// guidanceVerbs are the imperatives a guidance's action sentence may start with.
var guidanceVerbs = []string{"Record", "Film", "Decode", "Update"}

// sentences splits a guidance into sentences: a full stop followed by a space and a capital ends
// one.
func sentences(s string) []string {
	var out []string
	start := 0
	for i := 0; i+2 < len(s); i++ {
		if s[i] == '.' && s[i+1] == ' ' && unicode.IsUpper(rune(s[i+2])) {
			out = append(out, s[start:i+1])
			start = i + 2
		}
	}
	return append(out, s[start:])
}

// TestFailureClasses: the guidance of each failure class of DESIGN.md section 13 is one sentence
// saying what is missing, with its numbers, and one action sentence starting with an imperative
// from a fixed list; each class's own numbers and words appear.
func TestFailureClasses(t *testing.T) {
	one := &qrvt.DecodeOptions{Recordings: []string{"a"}}
	stats := &qrvt.Stats{BlocksTotal: 4, CleanFraction: 0.35}
	short := []qrvt.Shortfall{{Block: 0, Have: 980, Need: 1000}, {Block: 3, Have: 970, Need: 1000}}
	cases := []struct {
		name  string
		err   error
		exit  int
		words []string
	}{
		{"no code", &qrvt.NotEnoughDataError{Captures: 412}, 3, []string{"412 captures", "Film the whole code"}},
		{"finders, header refused", &qrvt.NotEnoughDataError{Captures: 412, Located: 37}, 3,
			[]string{"37 of 412 captures", "no frame header"}},
		{"header refused, agreed", &qrvt.NotEnoughDataError{Captures: 412, Located: 37, Reject: "header reserved bits are not zero"}, 3,
			[]string{"37 of 412 captures", "header reserved bits are not zero"}},
		{"headers, no descriptor", &qrvt.NotEnoughDataError{Captures: 412, Located: 130, HeadersRead: 120, FramesSeen: 30, FramesTotal: 96}, 3,
			[]string{"120 of 412 captures", "30 of 96 code frames", "stream descriptor", "Record one more loop"}},
		{"blocks short", &qrvt.NotEnoughDataError{Blocks: short, Captures: 412, Located: 400, HeadersRead: 400, FramesSeen: 90,
			FramesTotal: 96, Descriptor: true}, 3,
			[]string{"1.2% short", "50 shards missing in 2 of 4 blocks", "90 of 96 code frames", "Record one more loop and pass both files"}},
		{"other session", &qrvt.NotEnoughDataError{Captures: 412, Located: 300, HeadersRead: 290, Other: 210, FramesSeen: 20, FramesTotal: 96}, 3,
			[]string{"210 of the 290 captures", "another transfer", "Decode each transfer"}},
		// Another transfer beside a blocks shortfall: the shortfall's numbers and action stay.
		{"other session, blocks short", &qrvt.NotEnoughDataError{Blocks: short, Captures: 412, Located: 400, HeadersRead: 400,
			Other: 12, FramesSeen: 90, FramesTotal: 96, Descriptor: true}, 3,
			[]string{"1.2% short", "50 shards missing", "12 of the 400 captures", "another transfer", "Record one more loop"}},
		{"unsupported version", &qrvt.UnsupportedVersionError{What: "format version", Value: 2}, 5,
			[]string{"format version 2", "newer qrvt", "Update qrvt"}},
		{"locked rate, blocks short", &qrvt.NotEnoughDataError{Blocks: short, Captures: 16, Located: 16, HeadersRead: 16, FramesSeen: 4,
			FramesTotal: 4, Descriptor: true, LockedRate: true}, 3,
			[]string{"1.2% short", "35% of each capture clean on average", "locked to the display rate", "at least twice the display rate"}},
		{"locked rate, no descriptor", &qrvt.NotEnoughDataError{Captures: 16, Located: 16, HeadersRead: 16, FramesSeen: 4, FramesTotal: 4,
			LockedRate: true}, 3, []string{"stream descriptor", "locked to the display rate", "at least twice the display rate"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := guide(tc.err, stats, one)
			if ExitCode(g) != tc.exit {
				t.Errorf("exit %d, want %d", ExitCode(g), tc.exit)
			}
			_, text := splitGuidance(g)
			ss := sentences(text)
			if len(ss) != 2 {
				t.Fatalf("%d sentences, want 2: %q", len(ss), text)
			}
			if !strings.ContainsAny(ss[0], "0123456789") || !strings.HasSuffix(ss[0], ".") {
				t.Errorf("the missing sentence has no quantity: %q", ss[0])
			}
			verb, _, _ := strings.Cut(ss[1], " ")
			if !slices.Contains(guidanceVerbs, verb) || !strings.HasSuffix(ss[1], ".") {
				t.Errorf("the action sentence does not start with an imperative of %v: %q", guidanceVerbs, ss[1])
			}
			for _, w := range tc.words {
				if !strings.Contains(text, w) {
					t.Errorf("%q does not contain %q", text, w)
				}
			}
		})
	}
	// Without a locked rate, the shortfall gets the generic action.
	if _, text := splitGuidance(guide(&qrvt.NotEnoughDataError{Blocks: short, Captures: 16, Descriptor: true, Located: 16,
		HeadersRead: 16}, stats, one)); strings.Contains(text, "twice") {
		t.Errorf("generic shortfall: %q", text)
	}
}

// TestFailureClassJSON: in --json mode a class's guidance is the error line's guidance field, and
// the exit code follows the class: 5 for an unsupported version.
func TestFailureClassJSON(t *testing.T) {
	f := newFiles(t)
	for _, c := range []struct {
		err  error
		exit int
		want string
	}{
		{&qrvt.NotEnoughDataError{Captures: 12, Located: 9}, 3, "9 of 12 captures"},
		{&qrvt.UnsupportedVersionError{What: "format version", Value: 2}, 5, "format version 2"},
	} {
		out, _, err := run(t, &fakeAPI{err: c.err}, nil, "--json", "decode", f.r1, "-o", f.dir)
		if ExitCode(err) != c.exit {
			t.Errorf("%v: exit %d, want %d", c.err, ExitCode(err), c.exit)
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		var last struct{ Type, Guidance string }
		if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil || last.Type != "error" ||
			!strings.Contains(last.Guidance, c.want) {
			t.Errorf("%v: last line %q, want an error line whose guidance names %q", c.err, lines[len(lines)-1], c.want)
		}
	}
}
