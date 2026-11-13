package payload

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// Path limits of FORMAT.md section 12.
const (
	maxComponent = 255
	maxPath      = 4096
)

// devices are the Windows device names a component may not take, before its first dot.
var devices = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true}

func init() {
	for _, p := range []string{"COM", "LPT"} {
		for d := '1'; d <= '9'; d++ {
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
		if devices[strings.ToUpper(stem)] {
			return "Windows device name"
		}
	}
	return ""
}

// folder is the full Unicode case folding of golang.org/x/text/cases (CaseFolding.txt, statuses C and
// F), which is stateless and safe for concurrent use.
var folder = cases.Fold()

// foldKey returns the key under which two paths collide: NFC(full case folding(p)).
func foldKey(p string) string {
	return norm.NFC.String(folder.String(p))
}
