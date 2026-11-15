package main

import (
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestCodeChanged(t *testing.T) {
	docsOnly := []string{"docs/FORMAT.md", "docs/spikes/S1.md", "docs/SECURITY.md", "docs/IMPLEMENTATION_PROMPT.md"}
	cases := []struct {
		event string
		files []string
		want  bool
	}{
		// Every event that is not a pull request runs everything.
		{"push", docsOnly, true},
		{"schedule", docsOnly, true},
		{"workflow_dispatch", docsOnly, true},
		{"merge_group", docsOnly, true},
		{"", docsOnly, true},
		{"pull_request", docsOnly, false},
		{"pull_request", []string{"docs/spikes/S2-REQUEST.md"}, false},
		// No changed files cannot be shown safe.
		{"pull_request", nil, true},
		// Read by internal/channel's tests.
		{"pull_request", []string{"docs/DESIGN.md"}, true},
		{"pull_request", append([]string{"docs/DESIGN.md"}, docsOnly...), true},
		{"pull_request", []string{"README.md"}, true},
		{"pull_request", []string{"LICENSE"}, true},
		{"pull_request", []string{"NOTICE"}, true},
		{"pull_request", []string{".goreleaser.yaml"}, true},
		{"pull_request", []string{"go.mod"}, true},
		{"pull_request", []string{".github/workflows/ci.yml"}, true},
		{"pull_request", []string{"testdata/golden/plan.json"}, true},
		{"pull_request", []string{"internal/frame/testdata/fuzz/FuzzDecodeHeader/abc"}, true},
		{"pull_request", []string{"scripts/setup.sh"}, true},
		{"pull_request", []string{"tools/dev/main.go"}, true},
		// Prefix boundaries.
		{"pull_request", []string{"docs/spikesX.md"}, true},
		{"pull_request", []string{"docs/FORMAT.md.orig"}, true},
		{"pull_request", []string{"x/docs/FORMAT.md"}, true},
	}
	for _, c := range cases {
		if got := codeChanged(c.event, c.files); got != c.want {
			t.Errorf("codeChanged(%q, %v) = %v, want %v", c.event, c.files, got, c.want)
		}
	}
}

func TestParseDiffNames(t *testing.T) {
	got := parseDiffNames("docs/FORMAT.md\r\n\ninternal/a.go\n")
	if strings.Join(got, ",") != "docs/FORMAT.md,internal/a.go" {
		t.Fatalf("parseDiffNames = %q", got)
	}
}

// TestDocsOnlyAllowListExists keeps the allow-list from naming files that no longer exist.
func TestDocsOnlyAllowListExists(t *testing.T) {
	for _, e := range docsOnlyAllowList {
		if _, err := os.Stat(filepath.Join("..", "..", filepath.FromSlash(strings.TrimSuffix(e, "/")))); err != nil {
			t.Errorf("allow-list entry %s: %v", e, err)
		}
	}
}

// TestDocsOnlyAllowListUnread guards the allow-list: no Go string literal anywhere in the module,
// and no non-comment text of a workflow, script or the GoReleaser configuration, may name an
// allow-listed file (by its repository path, or for a file by its base name alone), because a
// test or build that reads such a file must run when it changes. A new reader must remove the
// file from docsOnlyAllowList.
func TestDocsOnlyAllowListUnread(t *testing.T) {
	root := filepath.Join("..", "..")
	var hits []string
	check := func(where, text string) {
		for _, e := range allowListedIn(text) {
			hits = append(hits, where+" names "+e)
		}
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == ".git" || rel == "dist" || rel == "recordings" || rel == ".claude" {
				return filepath.SkipDir
			}
			return nil
		}
		// The allow-list's own definition and tests name its entries.
		if rel == "tools/dev/changes.go" || rel == "tools/dev/changes_test.go" {
			return nil
		}
		switch {
		case strings.HasSuffix(rel, ".go"):
			src, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			var s scanner.Scanner
			fset := token.NewFileSet()
			s.Init(fset.AddFile(rel, -1, len(src)), src, nil, 0)
			for {
				pos, tok, lit := s.Scan()
				if tok == token.EOF {
					break
				}
				if tok == token.STRING {
					if v, err := strconv.Unquote(lit); err == nil {
						check(fset.Position(pos).String(), v)
					}
				}
			}
		case strings.HasPrefix(rel, ".github/") || strings.HasPrefix(rel, "scripts/") || rel == ".goreleaser.yaml":
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			for i, l := range lines(b) {
				if code := strings.TrimSpace(stripComment(l)); code != "" {
					for _, f := range strings.FieldsFunc(code, func(r rune) bool { return strings.ContainsRune(" \t'\"=,()[]{}", r) }) {
						check(rel+":"+strconv.Itoa(i+1), f)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		t.Error(h)
	}
}

func TestDocsOnlyGuardCatchesReaders(t *testing.T) {
	// The guard's matching rule, on the forms a reader would write.
	for _, text := range []string{"../../docs/FORMAT.md", "docs/spikes/S1.md", "FORMAT.md"} {
		if len(allowListedIn(text)) == 0 {
			t.Errorf("%q would not be caught", text)
		}
	}
	// Mentions in prose are not reads.
	for _, text := range []string{"import rule of SECURITY.md section 9.6 broken", "docs", "spikes"} {
		if got := allowListedIn(text); len(got) != 0 {
			t.Errorf("%q caught as %v", text, got)
		}
	}
}

// allowListedIn returns the allow-list entries that text names as a path: by repository path, or
// for a file by exactly its base name (as in filepath.Join("..", "docs", "FORMAT.md")).
func allowListedIn(text string) []string {
	var out []string
	for _, e := range docsOnlyAllowList {
		full := strings.TrimSuffix(e, "/")
		if strings.Contains(text, full) || (!strings.HasSuffix(e, "/") && text == path.Base(full)) {
			out = append(out, e)
		}
	}
	return out
}
