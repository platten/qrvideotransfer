package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// The workflow policy of SECURITY.md section 8: every action is pinned by a full commit SHA with a
// version comment, every workflow declares top-level permissions (and not a blanket read-all or
// write-all), and nothing runs on pull_request_target. YAML is checked line by line, which is
// enough for workflows written by hand in this repository's style.

// actionRef is a `uses:` reference pinned by commit, with the version its comment claims.
type actionRef struct{ repo, sha, version, where string }

var (
	usesLine    = regexp.MustCompile(`^\s*(?:-\s+)?uses:\s*(['"]?)([^'"\s#]+)['"]?\s*(?:#\s*(.*))?$`)
	fullSHA     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	versionNote = regexp.MustCompile(`^v\d+(\.\d+)*\S*$`)
	dockerPin   = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)
	prTarget    = regexp.MustCompile(`(^|[\s\[,'"])pull_request_target($|[\s\]:,'"])`)
)

// stripComment removes a trailing YAML comment; it is not quote-aware beyond requiring a space
// before the #, which is how comments are written here.
func stripComment(line string) string {
	if strings.HasPrefix(strings.TrimSpace(line), "#") {
		return ""
	}
	if i := strings.Index(line, " #"); i >= 0 {
		return line[:i]
	}
	return line
}

// lines splits a file into lines without carriage returns.
func lines(data []byte) []string {
	return strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
}

// workflowProblems returns the policy violations in one file. workflow is false for action
// metadata files, which need pinned uses but have no permissions.
func workflowProblems(name string, data []byte, workflow bool) []string {
	var probs []string
	add := func(n int, format string, a ...any) {
		where := name + ":"
		if n > 0 {
			where += fmt.Sprintf(" line %d:", n)
		}
		probs = append(probs, where+" "+fmt.Sprintf(format, a...))
	}
	perms := false
	for i, line := range lines(data) {
		n := i + 1
		if m := usesLine.FindStringSubmatch(line); m != nil {
			ref, note := m[2], strings.TrimSpace(m[3])
			switch {
			case strings.HasPrefix(ref, "./"):
			case strings.HasPrefix(ref, "docker://"):
				if !dockerPin.MatchString(ref) {
					add(n, "%s is not pinned by sha256 digest", ref)
				}
			default:
				at := strings.LastIndex(ref, "@")
				switch {
				case at < 0 || !fullSHA.MatchString(ref[at+1:]):
					add(n, "%s is not pinned by a full commit SHA", ref)
				case !versionNote.MatchString(note):
					add(n, "%s has no version comment (want # vX.Y.Z)", ref)
				}
			}
		}
		code := stripComment(line)
		if prTarget.MatchString(code) {
			add(n, "pull_request_target is not allowed")
		}
		if workflow && strings.HasPrefix(code, "permissions:") {
			perms = true
			if v := strings.TrimSpace(strings.TrimPrefix(code, "permissions:")); v == "read-all" || v == "write-all" {
				add(0, "top-level permissions: %s is not least privilege", v)
			}
		}
	}
	if workflow && !perms {
		add(0, "no top-level permissions")
	}
	return probs
}

// pinnedRefs returns the commit-pinned remote actions and reusable workflows of a file.
func pinnedRefs(name string, data []byte) []actionRef {
	var refs []actionRef
	for i, line := range lines(data) {
		m := usesLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		at := strings.LastIndex(m[2], "@")
		if at < 0 || !fullSHA.MatchString(m[2][at+1:]) || !versionNote.MatchString(strings.TrimSpace(m[3])) {
			continue
		}
		parts := strings.SplitN(m[2][:at], "/", 3)
		if len(parts) < 2 {
			continue
		}
		refs = append(refs, actionRef{parts[0] + "/" + parts[1], m[2][at+1:], strings.TrimSpace(m[3]), fmt.Sprintf("%s:%d", name, i+1)})
	}
	return refs
}

// verifyRefs checks that each reference's version tag names its commit. lsRemote returns the
// `git ls-remote --tags` output for a repository URL.
func verifyRefs(refs []actionRef, lsRemote func(url string) (string, error)) []string {
	var probs []string
	cache := map[string]map[string]string{}
	for _, r := range refs {
		tags, ok := cache[r.repo]
		if !ok {
			out, err := lsRemote("https://github.com/" + r.repo)
			if err != nil {
				probs = append(probs, fmt.Sprintf("%s: %s: %v", r.where, r.repo, err))
				continue
			}
			tags = map[string]string{}
			for _, l := range strings.Split(out, "\n") {
				f := strings.Fields(l)
				if len(f) != 2 || !strings.HasPrefix(f[1], "refs/tags/") {
					continue
				}
				tag := strings.TrimPrefix(f[1], "refs/tags/")
				if peeled, ok := strings.CutSuffix(tag, "^{}"); ok {
					tags[peeled] = f[0] // the commit an annotated tag points to wins
				} else if _, seen := tags[tag]; !seen {
					tags[tag] = f[0]
				}
			}
			cache[r.repo] = tags
		}
		switch got, ok := tags[r.version]; {
		case !ok:
			probs = append(probs, fmt.Sprintf("%s: %s has no tag %s", r.where, r.repo, r.version))
		case got != r.sha:
			probs = append(probs, fmt.Sprintf("%s: %s %s is %s, not %s", r.where, r.repo, r.version, got, r.sha))
		}
	}
	return probs
}

// workflowFiles lists the workflow and action metadata files under a .github directory.
func workflowFiles(dir string) (workflows, actions []string, err error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, nil, err
	}
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		ext := filepath.Ext(p)
		if ext != ".yml" && ext != ".yaml" {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		switch {
		case strings.HasPrefix(rel, "workflows/"):
			workflows = append(workflows, p)
		case strings.HasPrefix(rel, "actions/"):
			actions = append(actions, p)
		}
		return nil
	})
	return workflows, actions, err
}

// checkWorkflowDir applies the policy to every workflow and action under dir and returns the
// problems and the number of files checked.
func checkWorkflowDir(dir string) ([]string, int, error) {
	wfs, acts, err := workflowFiles(dir)
	if err != nil {
		return nil, 0, err
	}
	var probs []string
	for i, p := range append(wfs, acts...) {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, 0, err
		}
		probs = append(probs, workflowProblems(filepath.ToSlash(p), b, i < len(wfs))...)
	}
	return probs, len(wfs) + len(acts), nil
}

// checkWorkflows is the check-workflows task: the pinning policy, optionally each pin against
// its tag on GitHub, then actionlint.
func checkWorkflows(args []string) error {
	fs := newFlags("check-workflows")
	dir := fs.String("dir", ".github", "directory holding workflows/ and actions/")
	remote := fs.Bool("remote", false, "also check with git ls-remote that each version comment's tag names the pinned commit")
	lint := fs.Bool("actionlint", true, "also run the pinned actionlint")
	_ = fs.Parse(args) // ExitOnError: never returns an error
	probs, n, err := checkWorkflowDir(*dir)
	if err != nil {
		return err
	}
	if *remote {
		wfs, acts, _ := workflowFiles(*dir)
		var refs []actionRef
		for _, p := range append(wfs, acts...) {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			refs = append(refs, pinnedRefs(filepath.ToSlash(p), b)...)
		}
		probs = append(probs, verifyRefs(refs, func(url string) (string, error) {
			out, err := exec.Command("git", "ls-remote", "--tags", url).Output()
			return string(out), err
		})...)
	}
	if len(probs) > 0 {
		return errors.New("workflow policy violations:\n  " + strings.Join(probs, "\n  "))
	}
	fmt.Printf("workflow policy: %d files ok\n", n)
	if !*lint {
		return nil
	}
	p, err := find(toolNamed("actionlint"))
	if err != nil {
		return err
	}
	return run(nil, p)
}
