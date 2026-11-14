package payload

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// Path limits of FORMAT.md section 12.
const (
	maxComponent  = 255
	maxPath       = 4096
	maxComponents = 64 // components in a path
)

// devices are the Windows device names a component may not take, compared with its part from the first
// dot and any spaces before it removed.
var devices = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true, "CONIN$": true, "CONOUT$": true}

func init() {
	for _, p := range []string{"COM", "LPT"} {
		for _, d := range "0123456789\u00b9\u00b2\u00b3" {
			devices[p+string(d)] = true
		}
	}
}

// ValidatePath checks one manifest path against the path rules of FORMAT.md section 12 that concern a
// single path. It returns an [UnsafeArchiveError] naming the rule broken.
func ValidatePath(p string) error {
	if reason := pathRule(p); reason != "" {
		return &UnsafeArchiveError{p, reason}
	}
	return nil
}

func pathRule(p string) string {
	switch {
	case p == "":
		return "empty path"
	case len(p) > maxPath:
		return "path longer than 4096 bytes"
	case strings.Count(p, "/") >= maxComponents:
		return "more than 64 components"
	case !utf8.ValidString(p):
		return "not valid UTF-8"
	case !norm.NFC.IsNormalString(p):
		return "not in Unicode normalisation form C"
	}
	for _, r := range p {
		switch {
		case r < 0x20 || r == 0x7f:
			return "control character"
		case r == '\\':
			return "backslash"
		case r == ':':
			return "colon"
		case strings.ContainsRune(`<>"|?*`, r):
			return "character Windows cannot store in a name"
		}
	}
	for c := range strings.SplitSeq(p, "/") {
		switch {
		case c == "":
			return "empty component (absolute path or doubled or trailing slash)"
		case c == "." || c == "..":
			return "dot or dot-dot component"
		case len(c) > maxComponent:
			return "component longer than 255 bytes"
		case strings.HasSuffix(c, " ") || strings.HasSuffix(c, "."):
			return "component ends in a space or a dot"
		}
		stem, _, _ := strings.Cut(c, ".")
		if devices[strings.ToUpper(strings.TrimRight(stem, " "))] {
			return "Windows device name"
		}
	}
	return ""
}

// folder is the full Unicode case folding of golang.org/x/text/cases, which is stateless and safe for
// concurrent use. It agrees with CaseFolding.txt (statuses C and F) except for Cherokee, which
// CaseFolding.txt folds to the uppercase letters and cases.Fold to the lowercase ones; cherokee
// corrects that.
var folder = cases.Fold()

// cherokee maps the Cherokee lowercase letters to the uppercase letters CaseFolding.txt folds them to:
// U+AB70..U+ABBF to U+13A0..U+13EF and U+13F8..U+13FD to U+13F0..U+13F5.
func cherokee(r rune) rune {
	switch {
	case r >= 0xab70 && r <= 0xabbf:
		return r - 0xab70 + 0x13a0
	case r >= 0x13f8 && r <= 0x13fd:
		return r - 8
	}
	return r
}

// foldKey returns the key under which two paths collide: NFC of the full case folding of p
// (CaseFolding.txt statuses C and F).
func foldKey(p string) string {
	return norm.NFC.String(strings.Map(cherokee, folder.String(p)))
}
