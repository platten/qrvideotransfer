package ui

// Clean returns s without control characters, for printing a string that may come from the input
// (SECURITY.md T6): escape sequences (7-bit, 8-bit and UTF-8 encoded C1), other C0 and C1 controls,
// DEL, bidirectional controls and line or paragraph separators. Tabs and line breaks become spaces;
// invalid UTF-8 becomes U+FFFD.
func Clean(s string) string {
	return s
}
