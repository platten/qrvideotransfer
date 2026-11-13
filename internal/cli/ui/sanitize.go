package ui

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// Clean returns s without control characters, for printing a string that may come from the input
// (SECURITY.md T6): escape sequences (7-bit, 8-bit and UTF-8 encoded C1), other C0 and C1 controls,
// DEL, bidirectional controls and line or paragraph separators. Tabs and line breaks become spaces;
// invalid UTF-8 becomes U+FFFD.
func Clean(s string) string {
	// Invalid bytes, including raw 8-bit C1 introducers, become U+FFFD. UTF-8 encoded C1 controls
	// are rewritten as their 7-bit equivalents (U+009B becomes ESC [) so that the escape parser
	// removes whole sequences, not only their introducers.
	var b strings.Builder
	for _, r := range strings.ToValidUTF8(s, "�") {
		if r >= 0x80 && r <= 0x9f {
			b.WriteByte(0x1b)
			r -= 0x40
		}
		b.WriteRune(r)
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f' || r == '\u2028' || r == '\u2029':
			return ' '
		case unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r):
			return -1
		}
		return r
	}, ansi.Strip(b.String()))
}

// cleanAll applies Clean to every string of ss.
func cleanAll(ss []string) []string {
	r := make([]string, len(ss))
	for i, s := range ss {
		r[i] = Clean(s)
	}
	return r
}
