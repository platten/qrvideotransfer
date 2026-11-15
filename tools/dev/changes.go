package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// The documents-only CI skip (DESIGN.md section 17). A pull request whose changed files are all on
// docsOnlyAllowList skips the test matrix, the release snapshot, fuzzing and the setup scripts;
// the workflow and licence checks and CodeQL still run. Everything else runs everything.

// docsOnlyAllowList holds the only files whose change cannot affect a test or a build: no test,
// build, script, workflow or release configuration reads them, which TestDocsOnlyAllowListUnread
// checks. An entry ending in "/" covers a directory. docs/DESIGN.md is absent on purpose, because
// internal/channel's tests read its preset tables; so are LICENSE, NOTICE and README.md, which
// go into the release archives. A file that a test or build starts reading must leave this list.
var docsOnlyAllowList = []string{
	"docs/FORMAT.md",
	"docs/IMPLEMENTATION_PROMPT.md",
	"docs/SECURITY.md",
	"docs/spikes/",
}

// codeChanged reports whether CI must run everything: for every event that is not a pull request,
// for a pull request with no changed files listed, and for one that changes any file outside
// docsOnlyAllowList.
func codeChanged(event string, files []string) bool {
	if event != "pull_request" || len(files) == 0 {
		return true
	}
	for _, f := range files {
		if !allowListed(f) {
			return true
		}
	}
	return false
}

func allowListed(file string) bool {
	for _, e := range docsOnlyAllowList {
		if file == e || (strings.HasSuffix(e, "/") && strings.HasPrefix(file, e)) {
			return true
		}
	}
	return false
}

// parseDiffNames splits `git diff --name-only` output into file names.
func parseDiffNames(out string) []string {
	var files []string
	for _, l := range lines([]byte(out)) {
		if l = strings.TrimSpace(l); l != "" {
			files = append(files, l)
		}
	}
	return files
}

// changes is the changes task, the first job of ci.yml: it prints the files a pull request changes
// against the merge base of its base and head commits (renames listed as deletion plus addition)
// and writes code=true or code=false to $GITHUB_OUTPUT when that is set.
func changes(args []string) error {
	fs := newFlags("changes")
	event := fs.String("event", "", "the GitHub event name (github.event_name)")
	base := fs.String("base", "", "the pull request's base commit")
	head := fs.String("head", "", "the pull request's head commit")
	_ = fs.Parse(args) // ExitOnError: never returns an error
	var files []string
	if *event == "pull_request" {
		if *base == "" || *head == "" {
			return errors.New("a pull request needs -base and -head")
		}
		mb, err := exec.Command("git", "merge-base", *base, *head).Output()
		if err != nil {
			return fmt.Errorf("git merge-base %s %s: %w", *base, *head, err)
		}
		out, err := exec.Command("git", "diff", "--name-only", "--no-renames", strings.TrimSpace(string(mb)), *head).Output()
		if err != nil {
			return fmt.Errorf("git diff: %w", err)
		}
		files = parseDiffNames(string(out))
		for _, f := range files {
			fmt.Printf("changed: %s (allow-listed: %v)\n", f, allowListed(f))
		}
	}
	code := codeChanged(*event, files)
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
