package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestWorkflowProblemsFixtures(t *testing.T) {
	cases := []struct {
		file string
		want []string // substrings, one per expected problem, in order
	}{
		{"good.yml", nil},
		{"unpinned.yml", []string{
			"line 10: actions/checkout@v7 is not pinned",
			"line 11: actions/setup-go@main is not pinned",
			"line 12: actions/cache@55cc834 is not pinned",
			"line 13: actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9 has no version comment",
			"line 14: actions/attest@1e69f48acb82d1966a394da916b4c1698aa569d6 has no version comment",
			"line 15: docker://alpine:3 is not pinned by sha256 digest",
		}},
		{"no-permissions.yml", []string{"no top-level permissions"}},
		{"write-all.yml", []string{"top-level permissions: write-all"}},
		{"pr-target.yml", []string{"line 4: pull_request_target"}},
		{"pr-target-list.yml", []string{"line 3: pull_request_target"}},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			got := workflowProblems(c.file, readFixture(t, c.file), true)
			if len(got) != len(c.want) {
				t.Fatalf("got %d problems, want %d:\n%s", len(got), len(c.want), strings.Join(got, "\n"))
			}
			for i, w := range c.want {
				if !strings.Contains(got[i], w) || !strings.HasPrefix(got[i], c.file+":") {
					t.Errorf("problem %d = %q, want it to start with %q and contain %q", i, got[i], c.file+":", w)
				}
			}
		})
	}
}

func TestWorkflowProblemsCRLF(t *testing.T) {
	b := strings.ReplaceAll(string(readFixture(t, "good.yml")), "\n", "\r\n")
	if got := workflowProblems("good.yml", []byte(b), true); len(got) != 0 {
		t.Fatalf("CRLF line endings gave problems: %v", got)
	}
}

func TestWorkflowProblemsActionFile(t *testing.T) {
	// A composite action needs pinned uses but no permissions block.
	action := "runs:\n  using: composite\n  steps:\n    - uses: actions/cache@v6\n"
	got := workflowProblems("action.yml", []byte(action), false)
	if len(got) != 1 || !strings.Contains(got[0], "not pinned") {
		t.Fatalf("got %v, want one pinning problem", got)
	}
}

func TestPinnedRefs(t *testing.T) {
	refs := pinnedRefs("good.yml", readFixture(t, "good.yml"))
	want := []actionRef{
		{"actions/checkout", "3d3c42e5aac5ba805825da76410c181273ba90b1", "v7.0.1", "good.yml:15"},
		{"actions/setup-go", "b7ad1dad31e06c5925ef5d2fc7ad053ef454303e", "v7.0.0", "good.yml:17"},
		{"github/codeql-action", "33d7b85da1a04171e211befe2c739303b9a4c066", "v4.39.3", "good.yml:18"},
		{"owner/repo", "3d3c42e5aac5ba805825da76410c181273ba90b1", "v1.2.3", "good.yml:23"},
	}
	if !slices.Equal(refs, want) {
		t.Fatalf("pinnedRefs =\n%v\nwant\n%v", refs, want)
	}
}

func TestVerifyRefs(t *testing.T) {
	const sha = "3d3c42e5aac5ba805825da76410c181273ba90b1"
	const other = "b7ad1dad31e06c5925ef5d2fc7ad053ef454303e"
	remote := map[string]string{
		// A lightweight tag.
		"https://github.com/a/light": sha + "\trefs/tags/v1.0.0\n",
		// An annotated tag: the peeled line names the commit.
		"https://github.com/a/annotated": other + "\trefs/tags/v2.0.0\n" + sha + "\trefs/tags/v2.0.0^{}\n",
		// The tag points elsewhere.
		"https://github.com/a/moved": other + "\trefs/tags/v3.0.0\n",
	}
	lsRemote := func(url string) (string, error) {
		if out, ok := remote[url]; ok {
			return out, nil
		}
		return "", errors.New("unreachable")
	}
	refs := []actionRef{
		{"a/light", sha, "v1.0.0", "w.yml:1"},
		{"a/annotated", sha, "v2.0.0", "w.yml:2"},
		{"a/moved", sha, "v3.0.0", "w.yml:3"},
		{"a/missing-tag", sha, "v4.0.0", "w.yml:4"},
		{"a/light", sha, "v9.9.9", "w.yml:5"},
	}
	got := verifyRefs(refs, lsRemote)
	want := []string{
		"w.yml:3: a/moved v3.0.0 is " + other + ", not " + sha,
		"w.yml:4: a/missing-tag: unreachable",
		"w.yml:5: a/light has no tag v9.9.9",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("verifyRefs =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestRepositoryWorkflows enforces the policy on the repository's own workflows and actions, so
// that every `go test ./...` run checks it (SECURITY.md section 8).
func TestRepositoryWorkflows(t *testing.T) {
	problems, n, err := checkWorkflowDir(filepath.Join("..", "..", ".github"))
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no workflows found under .github")
	}
	for _, p := range problems {
		t.Error(p)
	}
}

func TestCheckWorkflowDirFixtures(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "workflows")
	if err := os.MkdirAll(wf, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"good.yml", "write-all.yml"} {
		if err := os.WriteFile(filepath.Join(wf, f), readFixture(t, f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	act := filepath.Join(dir, "actions", "x")
	if err := os.MkdirAll(act, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(act, "action.yaml"), []byte("runs:\n  steps:\n    - uses: a/b@v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	problems, n, err := checkWorkflowDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 || len(problems) != 2 {
		t.Fatalf("checked %d files with problems %v; want 3 files and 2 problems", n, problems)
	}
	if _, _, err := checkWorkflowDir(filepath.Join(dir, "absent")); err == nil {
		t.Fatal("a missing directory gave no error")
	}
}
