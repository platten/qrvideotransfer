package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"strings"
)

// The documents-only CI skip (DESIGN.md section 17). A pull request whose changed files are all on
// docsOnlyAllowList skips the test matrix, the release snapshot, fuzzing and the setup scripts;
// the workflow and licence checks and CodeQL still run. Everything else runs everything.

// docsOnlyAllowList holds the only files whose change cannot affect a test or a build: no test,
// build, script, workflow or release configuration reads them, which TestDocsOnlyAllowListUnread
// checks. Entries are path.Match patterns, so `docs/spikes/*.md` covers the Markdown files directly
// in that directory and nothing below it. docs/DESIGN.md is absent on purpose, because
// internal/channel's tests read its preset tables; so are LICENSE, NOTICE and README.md, which
// go into the release archives. A file that a test or build starts reading must leave this list.
var docsOnlyAllowList = []string{
	"docs/FORMAT.md",
	"docs/IMPLEMENTATION_PROMPT.md",
	"docs/SECURITY.md",
	"docs/spikes/*.md",
}

// fileChange is one line of `git diff --name-status`: a status letter and a path.
type fileChange struct{ status, path string }

// codeChanged reports whether CI must run everything: for every event that is not a pull request,
// for a pull request with no changed files listed, and for one with any change that is not the
// addition or modification of an allow-listed file. Deletions count as code, because a test may
// list the files it expects (TestDocsOnlyAllowListExists does).
func codeChanged(event string, changes []fileChange) bool {
	if event != "pull_request" || len(changes) == 0 {
		return true
	}
	for _, c := range changes {
		if (c.status != "A" && c.status != "M") || !allowListed(c.path) {
			return true
		}
	}
	return false
}

// codeFor is codeChanged for a pull request into baseRef. A stacked pull request, one whose base
// is not the repository's default branch, always runs everything: if it were later retargeted to
// the default branch, the green ci result from its old base would otherwise stand for code that
// was never tested. Only a pull request into the default branch can skip.
func codeFor(event, baseRef, defaultBranch string, changes []fileChange) bool {
	if event == "pull_request" && (baseRef == "" || baseRef != defaultBranch) {
		return true
	}
	return codeChanged(event, changes)
}

// allowListed reports whether a path is on docsOnlyAllowList and is not code by its type: Go
// source, module files, test data and .gitattributes are code wherever they sit.
func allowListed(file string) bool {
	base := path.Base(file)
	if strings.HasSuffix(base, ".go") || base == "go.mod" || base == "go.sum" || base == ".gitattributes" ||
		slices.Contains(strings.Split(file, "/"), "testdata") {
		return false
	}
	for _, e := range docsOnlyAllowList {
		if ok, _ := path.Match(e, file); ok {
			return true
		}
	}
	return false
}

// parseNameStatus parses `git diff --name-status --no-renames` output.
func parseNameStatus(out string) ([]fileChange, error) {
	var changes []fileChange
	for _, l := range lines([]byte(out)) {
		if strings.TrimSpace(l) == "" {
			continue
		}
		status, p, ok := strings.Cut(l, "\t")
		if !ok || status == "" || p == "" {
			return nil, fmt.Errorf("unexpected git diff line %q", l)
		}
		changes = append(changes, fileChange{status, p})
	}
	return changes, nil
}

// changes is the changes task, the first job of ci.yml: it prints the files a pull request changes
// against the merge base of its base and head commits, with their status (renames listed as a
// deletion plus an addition)
// and writes code=true or code=false to $GITHUB_OUTPUT when that is set.
func changes(args []string) error {
	fs := newFlags("changes")
	event := fs.String("event", "", "the GitHub event name (github.event_name)")
	base := fs.String("base", "", "the pull request's base commit")
	head := fs.String("head", "", "the pull request's head commit")
	baseRef := fs.String("base-ref", "", "the pull request's base branch")
	defaultBranch := fs.String("default-branch", "", "the repository's default branch")
	_ = fs.Parse(args) // ExitOnError: never returns an error
	var files []fileChange
	if *event == "pull_request" {
		if *base == "" || *head == "" || *baseRef == "" || *defaultBranch == "" {
			return errors.New("a pull request needs -base, -head, -base-ref and -default-branch")
		}
		fmt.Printf("base branch: %s (default branch: %s)\n", *baseRef, *defaultBranch)
		mb, err := exec.Command("git", "merge-base", *base, *head).Output()
		if err != nil {
			return fmt.Errorf("git merge-base %s %s: %w", *base, *head, err)
		}
		out, err := exec.Command("git", "diff", "--name-status", "--no-renames", strings.TrimSpace(string(mb)), *head).Output()
		if err != nil {
			return fmt.Errorf("git diff: %w", err)
		}
		files, err = parseNameStatus(string(out))
		if err != nil {
			return err
		}
		for _, f := range files {
			fmt.Printf("changed: %s %s (allow-listed: %v)\n", f.status, f.path, allowListed(f.path))
		}
	}
	code := codeFor(*event, *baseRef, *defaultBranch, files)
	fmt.Printf("code=%v\n", code)
	if p := os.Getenv("GITHUB_OUTPUT"); p != "" {
		f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(f, "code=%v\n", code)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return err
	}
	return nil
}

var (
	jobLine   = regexp.MustCompile(`^  ([A-Za-z0-9_-]+):\s*$`)
	needsLine = regexp.MustCompile(`^    needs:\s*(.+)$`)
)

// ciJobs reads the top-level jobs of a workflow written in this repository's style: their ids in
// order, each job's needs, and which jobs run only when the changes job reports code=true.
func ciJobs(b []byte) (jobs []string, needs map[string][]string, gated map[string]bool) {
	needs, gated = map[string][]string{}, map[string]bool{}
	inJobs, cur := false, ""
	for _, l := range lines(b) {
		code := stripComment(l)
		switch {
		case strings.HasPrefix(code, "jobs:"):
			inJobs = true
		case inJobs && code != "" && !strings.HasPrefix(code, " "):
			inJobs = false
		case !inJobs:
		case jobLine.MatchString(code):
			cur = jobLine.FindStringSubmatch(code)[1]
			jobs = append(jobs, cur)
		case cur != "" && needsLine.MatchString(code):
			v := strings.Trim(strings.TrimSpace(needsLine.FindStringSubmatch(code)[1]), "[]")
			for _, n := range strings.Split(v, ",") {
				if n = strings.TrimSpace(n); n != "" {
					needs[cur] = append(needs[cur], n)
				}
			}
		case cur != "" && strings.HasPrefix(code, "    if:") && strings.Contains(code, "needs.changes.outputs.code == 'true'"):
			gated[cur] = true
		}
	}
	return jobs, needs, gated
}
