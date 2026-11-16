package main

import (
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// mod returns modifications of the given paths.
func mod(paths ...string) []fileChange {
	var out []fileChange
	for _, p := range paths {
		out = append(out, fileChange{"M", p})
	}
	return out
}

func TestCodeChanged(t *testing.T) {
	docsOnly := mod("docs/FORMAT.md", "docs/spikes/S1.md", "docs/SECURITY.md", "docs/IMPLEMENTATION_PROMPT.md")
	cases := []struct {
		event   string
		changes []fileChange
		want    bool
	}{
		// Every event that is not a pull request runs everything.
		{"push", docsOnly, true},
		{"schedule", docsOnly, true},
		{"workflow_dispatch", docsOnly, true},
		{"merge_group", docsOnly, true},
		{"", docsOnly, true},
		{"pull_request", docsOnly, false},
		{"pull_request", mod("docs/spikes/S2-REQUEST.md"), false},
		{"pull_request", []fileChange{{"A", "docs/spikes/S4.md"}}, false},
		// No changed files cannot be shown safe.
		{"pull_request", nil, true},
		// Read by internal/channel's tests.
		{"pull_request", mod("docs/DESIGN.md"), true},
		{"pull_request", append(mod("docs/DESIGN.md"), docsOnly...), true},
		{"pull_request", mod("README.md"), true},
		{"pull_request", mod("LICENSE"), true},
		{"pull_request", mod("NOTICE"), true},
		{"pull_request", mod(".goreleaser.yaml"), true},
		{"pull_request", mod("go.mod"), true},
		{"pull_request", mod(".github/workflows/ci.yml"), true},
		{"pull_request", mod("testdata/golden/plan.json"), true},
		{"pull_request", mod("internal/frame/testdata/fuzz/FuzzDecodeHeader/abc"), true},
		{"pull_request", mod("scripts/setup.sh"), true},
		{"pull_request", mod("tools/dev/main.go"), true},
		// Prefix boundaries.
		{"pull_request", mod("docs/spikesX.md"), true},
		{"pull_request", mod("docs/FORMAT.md.orig"), true},
		{"pull_request", mod("x/docs/FORMAT.md"), true},
		// Review of PR #35, M1: Go source, module files, test data and attributes are code wherever
		// they sit, and the spike directory admits only Markdown files directly in it.
		{"pull_request", mod("docs/spikes/x.go"), true},
		{"pull_request", mod("docs/spikes/evil/x.go"), true},
		{"pull_request", mod("docs/spikes/a/b.md"), true},
		{"pull_request", mod("docs/spikes/go.mod"), true},
		{"pull_request", mod("docs/spikes/go.sum"), true},
		{"pull_request", mod("docs/spikes/testdata/x.md"), true},
		{"pull_request", mod("docs/spikes/.gitattributes"), true},
		{"pull_request", mod("docs/spikes/S1.png"), true},
		// Change-log entries, one file each (docs/changes/<document>/), are documents.
		{"pull_request", []fileChange{{"A", "docs/changes/FORMAT/2026-11-15-g-changelog-files.md"}}, false},
		{"pull_request", append(mod("docs/SECURITY.md"), fileChange{"A", "docs/changes/SECURITY/2026-11-15-x.md"}), false},
		{"pull_request", append(mod("docs/DESIGN.md"), fileChange{"A", "docs/changes/DESIGN/2026-11-15-x.md"}), true},
		{"pull_request", mod("docs/changes/README.md"), true},
		{"pull_request", mod("docs/changes/DESIGN/sub/x.md"), true},
		{"pull_request", mod("docs/changes/DESIGN/x.go"), true},
		{"pull_request", mod("docs/changes/DESIGN/x.txt"), true},
		{"pull_request", []fileChange{{"D", "docs/changes/DESIGN/2026-11-15-x.md"}}, true},
		{"pull_request", mod("docs/x_test.go"), true},
		// L1: a deletion, or any status other than an addition or modification, is code.
		{"pull_request", []fileChange{{"D", "docs/SECURITY.md"}}, true},
		{"pull_request", []fileChange{{"D", "docs/spikes/S1.md"}}, true},
		{"pull_request", []fileChange{{"T", "docs/FORMAT.md"}}, true},
		{"pull_request", []fileChange{{"", "docs/FORMAT.md"}}, true},
	}
	for _, c := range cases {
		if got := codeChanged(c.event, c.changes); got != c.want {
			t.Errorf("codeChanged(%q, %v) = %v, want %v", c.event, c.changes, got, c.want)
		}
	}
}

// TestCodeForStacked covers the fallback for M2 of the review of PR #35: a pull request whose base
// is not the default branch (a stacked pull request) always runs everything, because a later
// retarget to the default branch could otherwise carry untested code under a green ci result.
func TestCodeForStacked(t *testing.T) {
	docsOnly := mod("docs/FORMAT.md")
	cases := []struct {
		event, baseRef, defaultBranch string
		want                          bool
	}{
		{"pull_request", "main", "main", false},
		{"pull_request", "feat", "main", true},
		{"pull_request", "main-old", "main", true},
		{"pull_request", "", "main", true},
		{"pull_request", "main", "", true},
		{"pull_request", "trunk", "trunk", false},
		{"push", "main", "main", true},
	}
	for _, c := range cases {
		if got := codeFor(c.event, c.baseRef, c.defaultBranch, docsOnly); got != c.want {
			t.Errorf("codeFor(%q, base %q, default %q) = %v, want %v", c.event, c.baseRef, c.defaultBranch, got, c.want)
		}
	}
	if !codeFor("pull_request", "main", "main", mod("internal/a.go")) {
		t.Error("a code change against the default branch gave code=false")
	}
}

func TestParseNameStatus(t *testing.T) {
	got, err := parseNameStatus("M\tdocs/FORMAT.md\r\n\nD\tinternal/a.go\nA\tdocs/spikes/S4.md\n")
	want := []fileChange{{"M", "docs/FORMAT.md"}, {"D", "internal/a.go"}, {"A", "docs/spikes/S4.md"}}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("parseNameStatus = %v, %v; want %v", got, err, want)
	}
	if _, err := parseNameStatus("docs/FORMAT.md\n"); err == nil {
		t.Fatal("a line without a status gave no error")
	}
}

// allowListedFiles returns the repository files that the allow-list covers.
func allowListedFiles(t *testing.T) []string {
	t.Helper()
	root := filepath.Join("..", "..")
	var files []string
	for _, e := range docsOnlyAllowList {
		matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(e)))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) == 0 {
			t.Errorf("allow-list entry %s matches no file", e)
		}
		for _, m := range matches {
			rel, _ := filepath.Rel(root, m)
			files = append(files, filepath.ToSlash(rel))
		}
	}
	return files
}

// TestDocsOnlyAllowListExists keeps the allow-list from naming files that no longer exist.
func TestDocsOnlyAllowListExists(t *testing.T) {
	for _, f := range allowListedFiles(t) {
		if !allowListed(f) {
			t.Errorf("%s matched an entry but is not allow-listed", f)
		}
	}
}

// TestDocsOnlyAllowListUnread guards the allow-list. A test or build that reads an allow-listed
// file must run when it changes, so the test fails when:
//   - a Go string literal names an allow-listed file by repository path or base name, or names a
//     directory an entry covers (docs/spikes, docs/changes);
//   - a Go file holds the literal "docs" and the last element of such a directory ("spikes",
//     "changes"): a directory-level reader;
//   - a //go:embed pattern resolves to an allow-listed file or a directory that holds one;
//   - any Go file sits under docs/;
//   - a workflow, script or the GoReleaser configuration contains an allow-listed path outside a
//     comment.
//
// A new reader must take its file off docsOnlyAllowList.
func TestDocsOnlyAllowListUnread(t *testing.T) {
	root := filepath.Join("..", "..")
	files := allowListedFiles(t)
	var hits []string
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
		// The allow-list's own definition and tests name its entries, and the changelog task, which
		// only a person runs, writes and prints the entries.
		if rel == "tools/dev/changes.go" || rel == "tools/dev/changes_test.go" || rel == "tools/dev/changelog.go" || rel == "tools/dev/changelog_test.go" {
			return nil
		}
		switch {
		case strings.HasSuffix(rel, ".go"):
			src, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			hits = append(hits, goReaders(rel, src, files)...)
		case strings.HasPrefix(rel, ".github/") || strings.HasPrefix(rel, "scripts/") || rel == ".goreleaser.yaml":
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			// Here only a repository path counts: step names and descriptions cite documents by
			// base name ("SECURITY.md section 9.6") without reading them.
			for i, l := range lines(b) {
				code := stripComment(l)
				for _, f := range append(allowListDirs(), files...) {
					if strings.Contains(code, f) {
						hits = append(hits, rel+":"+strconv.Itoa(i+1)+" names "+f)
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
	files := []string{"docs/FORMAT.md", "docs/SECURITY.md", "docs/spikes/S1.md", "docs/changes/DESIGN/2026-11-15-x.md"}
	caught := []struct{ rel, src string }{
		{"tools/x/a.go", `package x; var p = "../../docs/FORMAT.md"`},
		{"tools/x/a.go", `package x; var p = "docs/spikes/S1.md"`},
		{"tools/x/a.go", `package x; var p = "FORMAT.md"`},
		{"tools/x/a.go", `package x; var p = "S1.md"`},
		{"tools/x/a.go", `package x; var p = "../docs/spikes"`},
		{"tools/x/a.go", `package x; var p = filepath.Join(root, "docs", "spikes")`},
		{"tools/x/a.go", `package x; var p = "../../docs/changes"`},
		{"tools/x/a.go", `package x; var p = filepath.Join(root, "docs", "changes", "DESIGN")`},
		{"internal/x/a.go", "package x\n\n//go:embed ../../docs/changes\nvar s embed.FS\n"},
		// Review of PR #35, L2: embedded files.
		{"internal/x/a.go", "package x\n\n//go:embed ../../docs/spikes/S1.md\nvar s string\n"},
		{"internal/x/a.go", "package x\n\n//go:embed ../../docs/spikes\nvar s embed.FS\n"},
		{"internal/x/a.go", "package x\n\n//go:embed ../../docs/spikes/*.md\nvar s embed.FS\n"},
		{"internal/x/a.go", "package x\n\n//go:embed ../../docs\nvar s embed.FS\n"},
		{"internal/x/a.go", "package x\n\n//go:embed \"../../docs/FORMAT.md\" other.txt\nvar s embed.FS\n"},
		// Any Go file under docs/.
		{"docs/emb.go", "package docs\n\n//go:embed spikes/S1.md\nvar s string\n"},
		{"docs/spikes/x_test.go", "package spikes\n"},
	}
	for _, c := range caught {
		if got := goReaders(c.rel, []byte(c.src), files); len(got) == 0 {
			t.Errorf("%s: %q would not be caught", c.rel, c.src)
		}
	}
	// Mentions in prose and comments, and unrelated embeds, are not reads.
	clean := []struct{ rel, src string }{
		{"a.go", `package a; var e = "import rule of SECURITY.md section 9.6 broken"`},
		{"a.go", `package a; var e = "docs"`},
		{"a.go", `package a; var e = "spikes"`},
		{"a.go", "package a\n\n// See docs/spikes/S1.md section 10.\nvar x = 1\n"},
		{"internal/x/a.go", "package x\n\n//go:embed testdata/*.json\nvar s embed.FS\n"},
	}
	for _, c := range clean {
		if got := goReaders(c.rel, []byte(c.src), files); len(got) != 0 {
			t.Errorf("%s: %q caught as %v", c.rel, c.src, got)
		}
	}
}

// goReaders returns how a Go file (module-relative path rel) may read an allow-listed file.
func goReaders(rel string, src []byte, files []string) []string {
	var hits []string
	if strings.HasPrefix(rel, "docs/") {
		hits = append(hits, rel+": a Go file under docs/")
	}
	var s scanner.Scanner
	fset := token.NewFileSet()
	s.Init(fset.AddFile(rel, -1, len(src)), src, nil, scanner.ScanComments)
	dirs := allowListDirs()
	seen := map[string]bool{}
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		where := fset.Position(pos).String()
		switch tok {
		case token.STRING:
			v, err := strconv.Unquote(lit)
			if err != nil {
				continue
			}
			seen[v] = true
			for _, d := range dirs {
				if strings.Contains(v, d) {
					hits = append(hits, where+" names "+d)
				}
			}
			for _, f := range files {
				if strings.Contains(v, f) || v == path.Base(f) {
					hits = append(hits, where+" names "+f)
				}
			}
		case token.COMMENT:
			pats, ok := strings.CutPrefix(lit, "//go:embed ")
			if !ok {
				continue
			}
			for _, p := range strings.Fields(pats) {
				p = strings.Trim(p, "\"`")
				r := path.Join(path.Dir(rel), p)
				for _, f := range files {
					if m, _ := path.Match(r, f); m || r == f || strings.HasPrefix(f, r+"/") {
						hits = append(hits, where+" embeds "+f)
					}
				}
			}
		}
	}
	for _, d := range dirs {
		if seen["docs"] && seen[path.Base(d)] {
			hits = append(hits, rel+": the literals \"docs\" and \""+path.Base(d)+"\" may join into "+d)
		}
	}
	return hits
}

// allowListDirs returns the directories that the allow-list's pattern entries cover, up to their
// first wildcard element: docs/spikes and docs/changes.
func allowListDirs() []string {
	var dirs []string
	for _, e := range docsOnlyAllowList {
		if i := strings.Index(e, "*"); i >= 0 {
			dirs = append(dirs, strings.TrimSuffix(e[:i], "/"))
		}
	}
	return dirs
}
