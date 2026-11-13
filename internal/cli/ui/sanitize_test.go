package ui

import (
	"strings"
	"testing"
	"unicode"
)

// hostile holds strings as an attacker would put them in file names or warning texts.
var hostile = []string{
	"\x1b[31mred\x1b[0m", // SGR
	"\x1b]8;;https://evil.example\x07link\x1b]8;;\x07", // OSC 8 hyperlink
	"\x1b]0;title\x1b\\",                // OSC window title, ST-terminated
	"\x1bP1;2qdcs\x1b\\",                // DCS
	"\x1b[2J\x1b[H",                     // clear screen, home
	"\x9b31m8bit",                       // raw 8-bit CSI (invalid UTF-8)
	"\u009b31mc1csi",                    // UTF-8 encoded C1 CSI
	"\u009d0;t\u009cc1osc",              // UTF-8 encoded C1 OSC, C1 ST
	"\u0085nel\u0090dcs",                // other C1 controls
	"\u202eevil.exe\u202c",              // right-to-left override
	"\u2066iso\u2069\u200e\u200f\u061c", // isolates and marks
	"bell\x07bs\x08del\x7fnul\x00",      // C0 and DEL
	"tab\tline\nret\rvt\vff\f",          // white-space controls
	"ls\u2028ps\u2029",                  // line and paragraph separators
	"tail\x1b[",                         // unterminated sequence
	"tail\x1b",                          // lone escape
	"bad\xff\xfeutf8",                   // invalid UTF-8
}

// assertClean fails when s holds any character Clean must remove.
func assertClean(t *testing.T, where, s string) {
	t.Helper()
	for i, r := range s {
		if r == '\n' {
			continue // renderers end lines; TestClean checks that Clean itself never leaves one
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || r == '\u2028' || r == '\u2029' {
			t.Errorf("%s: control character %U at byte %d in %q", where, r, i, s)
		}
	}
}

func TestClean(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain name.txt", "plain name.txt"},
		{"日本語 ファイル", "日本語 ファイル"},
		{"\x1b[31mred\x1b[0m", "red"},
		{"\x1b]8;;https://evil.example\x07link\x1b]8;;\x07", "link"},
		{"\x1b]0;title\x1b\\x", "x"},
		{"\x1bP1;2qdcs\x1b\\ok", "ok"},
		{"\u009b31mc1csi", "c1csi"},
		{"\u009d0;t\u009cc1osc", "c1osc"},
		{"\u202eevil.exe\u202c", "evil.exe"},
		{"\u2066iso\u2069\u200e\u200f\u061c", "iso"},
		{"bell\x07bs\x08del\x7fnul\x00", "bellbsdelnul"},
		{"tab\tline\nret\r", "tab line ret "},
		{"ls\u2028ps\u2029", "ls ps "},
		{"tail\x1b[", "tail"},
		{"bad\xffutf8", "bad�utf8"},
	}
	for _, tc := range tests {
		if got := Clean(tc.in); got != tc.want {
			t.Errorf("Clean(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	for _, s := range hostile {
		got := Clean(s)
		if strings.Contains(got, "\n") {
			t.Errorf("Clean(%q) keeps a line break", s)
		}
		assertClean(t, "Clean", got)
	}
}
