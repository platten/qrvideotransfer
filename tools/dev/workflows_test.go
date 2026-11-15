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
		{"cancel-always.yml", []string{
			"line 9: cancel-in-progress must be false or ${{ github.event_name == 'pull_request' }}",
			"line 15: cancel-in-progress must be false or ${{ github.event_name == 'pull_request' }}",
		}},
		{"cancel-flow.yml", []string{
			"line 6: cancel-in-progress must be false or",
			"line 12: cancel-in-progress must be false or",
			"line 19: cancel-in-progress must be false or",
		}},
		{"cancel-group.yml", []string{
			"line 9: the concurrency group of a pull-request cancel must contain github.event.pull_request.number and github.sha",
			"line 14: the concurrency group of a pull-request cancel must contain github.event.pull_request.number and github.sha",
		}},
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

func TestWorkflowProblemsCancelAllowed(t *testing.T) {
	wf := `name: x
on: [push, pull_request]
permissions:
  contents: read
concurrency:
  group: ci-${{ github.event_name == 'pull_request' && github.event.pull_request.number || github.sha }}
  cancel-in-progress: ${{ github.event_name == 'pull_request' }}
jobs:
  a:
    concurrency:
      group: a
      cancel-in-progress: false # a comment
  b:
    concurrency: {group: "b-${{ github.event_name == 'pull_request' && github.event.pull_request.number || github.sha }}", cancel-in-progress: ${{ github.event_name == 'pull_request' }}}
  c:
    concurrency:
      group: c
      'cancel-in-progress': false
`
	if got := workflowProblems("x.yml", []byte(wf), true); len(got) != 0 {
		t.Fatalf("allowed cancel-in-progress forms gave problems: %v", got)
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

// TestCIResultNeedsEveryJob checks that the aggregate `ci result` job of ci.yml waits for every
// other job, so that no job can fail without failing the required check, and that every job the
// documents-only skip may skip is gated on the changes job.
func TestCIResultNeedsEveryJob(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	jobs, needs, gated := ciJobs(b)
	if !slices.Contains(jobs, "result") || !slices.Contains(jobs, "changes") {
		t.Fatalf("ci.yml jobs %v lack result or changes", jobs)
	}
	for _, j := range jobs {
		if j != "result" && !slices.Contains(needs["result"], j) {
			t.Errorf("job %s is not in the needs of ci result", j)
		}
		if j != "result" && j != "changes" && j != "repo-checks" && !gated[j] {
			t.Errorf("job %s is not gated on the changes job's code output", j)
		}
	}
}

// TestCIRetargetAndCodeOutput checks two properties of ci.yml from the review of PR #35: a pull
// request whose base branch changes (an edited event) is classified again (M2), and ci result
// fails unless the changes job's code output is exactly true or false (L3).
func TestCIRetargetAndCodeOutput(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"  pull_request:\n    types: [opened, synchronize, reopened, edited]\n",
		`if [ "$CODE" != true ] && [ "$CODE" != false ]; then`,
		`if [ "$CODE" != false ] && echo "$NEEDS" | jq -e '[.[].result] | any(. == "skipped")'`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("ci.yml lacks %q", want)
		}
	}
}

func TestCIJobsParse(t *testing.T) {
	wf := "on: push\njobs:\n  changes:\n    runs-on: x\n  check:\n    needs: changes\n    if: needs.changes.outputs.code == 'true'\n" +
		"  result:\n    needs: [changes, check]\n    if: always()\n"
	jobs, needs, gated := ciJobs([]byte(wf))
	if !slices.Equal(jobs, []string{"changes", "check", "result"}) || !slices.Equal(needs["result"], []string{"changes", "check"}) ||
		!slices.Equal(needs["check"], []string{"changes"}) || !gated["check"] || gated["result"] {
		t.Fatalf("ciJobs = %v, %v, %v", jobs, needs, gated)
	}
}
