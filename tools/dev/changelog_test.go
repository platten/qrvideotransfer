package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEntryPath(t *testing.T) {
	day := time.Date(2026, 11, 15, 23, 59, 0, 0, time.UTC)
	got, err := entryPath("root", "DESIGN", day, "g-changelog-files")
	if want := filepath.Join("root", "docs", "changes", "DESIGN", "2026-11-15-g-changelog-files.md"); err != nil || got != want {
		t.Fatalf("entryPath = %q, %v; want %q", got, err, want)
	}
	for _, c := range []struct{ doc, topic string }{
		{"README", "x"},                     // not a document with a change log
		{"design", "x"},                     // case matters
		{"../DESIGN", "x"},                  // no path components
		{"DESIGN", ""},                      // a topic is required
		{"DESIGN", "Three Finder"},          // lower-case words joined by hyphens only
		{"DESIGN", "x/y"},                   // no path components
		{"DESIGN", "-x"},                    // no leading hyphen
		{"DESIGN", "x--y"},                  // no empty word
		{"DESIGN", "x.md"},                  // the extension is added
		{"DESIGN", strings.Repeat("a", 81)}, // short names
	} {
		if _, err := entryPath("root", c.doc, day, c.topic); err == nil {
			t.Errorf("entryPath(%q, %q) gave no error", c.doc, c.topic)
		}
	}
}

func TestAddEntryAndShow(t *testing.T) {
	root := t.TempDir()
	day := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	if _, err := addEntry(root, "FORMAT", day, "b-second", "Section 4 changes.\n"); err == nil {
		t.Fatal("adding to a document whose directory does not exist gave no error")
	}
	dir := filepath.Join(root, "docs", "changes", "FORMAT")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2026-11-13-table.md"), []byte("| Date | Change |\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := addEntry(root, "FORMAT", day, "b-second", "  Section 4 changes.  \n\n")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "Section 4 changes.\n" {
		t.Fatalf("entry holds %q", b)
	}
	if _, err := addEntry(root, "FORMAT", day, "b-second", "again"); err == nil {
		t.Fatal("overwriting an entry gave no error")
	}
	if _, err := addEntry(root, "FORMAT", day, "a-first", " \n"); err == nil {
		t.Fatal("an empty entry gave no error")
	}
	if _, err := addEntry(root, "FORMAT", day, "a-first", "Section 3 changes."); err != nil {
		t.Fatal(err)
	}
	// Other files in the directory are not entries.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := showLog(root, "FORMAT")
	if err != nil {
		t.Fatal(err)
	}
	want := "## 2026-11-13-table.md\n\n| Date | Change |\n\n" +
		"## 2026-11-15-a-first.md\n\nSection 3 changes.\n\n" +
		"## 2026-11-15-b-second.md\n\nSection 4 changes.\n\n"
	if got != want {
		t.Fatalf("showLog =\n%s\nwant\n%s", got, want)
	}
	if _, err := showLog(root, "NOPE"); err == nil {
		t.Fatal("an unknown document gave no error")
	}
}
