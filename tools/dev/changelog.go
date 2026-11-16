package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Change logs (DESIGN.md section 17). Each change to a specification document is one file,
// docs/changes/<document>/<YYYY-MM-DD>-<topic>.md, so that two branches never edit the same
// lines. The entries before 2026-11-15 are each document's old table, kept as it was in
// <YYYY-MM-DD>-table.md. Read in name order, the files are the log.

// changeLogDocs are the documents with a change log, by the name of their file in docs/.
var changeLogDocs = []string{"DESIGN", "FORMAT", "IMPLEMENTATION_PROMPT", "SECURITY"}

var topicName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// entryPath returns the file of a change-log entry for doc, dated day (UTC), about topic: a few
// lower-case words joined by hyphens, best starting with the stream (for example
// "m2-geometry-three-finder"), so that two branches do not pick the same name.
func entryPath(root, doc string, day time.Time, topic string) (string, error) {
	if !slices.Contains(changeLogDocs, doc) {
		return "", fmt.Errorf("%q has no change log; use one of %s", doc, strings.Join(changeLogDocs, ", "))
	}
	if len(topic) > 80 || !topicName.MatchString(topic) {
		return "", fmt.Errorf("topic %q: use up to 80 characters of lower-case words joined by hyphens", topic)
	}
	name := day.UTC().Format("2006-01-02") + "-" + topic + ".md"
	return filepath.Join(root, "docs", "changes", doc, name), nil
}

// addEntry writes a new change-log entry and returns its path. It refuses to overwrite one.
func addEntry(root, doc string, day time.Time, topic, text string) (string, error) {
	p, err := entryPath(root, doc, day, topic)
	if err != nil {
		return "", err
	}
	if text = strings.TrimSpace(text); text == "" {
		return "", errors.New("the entry is empty")
	}
	if _, err := os.Stat(filepath.Dir(p)); err != nil {
		return "", err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(text + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return p, err
}

// showLog returns a document's change log: every entry file in name order, each under its name.
func showLog(root, doc string) (string, error) {
	if !slices.Contains(changeLogDocs, doc) {
		return "", fmt.Errorf("%q has no change log", doc)
	}
	dir := filepath.Join(root, "docs", "changes", doc)
	names, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return "", err
	}
	slices.Sort(names)
	var b strings.Builder
	for _, n := range names {
		text, err := os.ReadFile(n)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "## %s\n\n%s\n", filepath.Base(n), strings.TrimRight(string(text), "\n")+"\n")
	}
	return b.String(), nil
}

// changelog is the changelog task:
//
//	go run ./tools/dev changelog add <DOCUMENT> <topic> <text>
//	go run ./tools/dev changelog show <DOCUMENT>
func changelog(args []string) error {
	fs := newFlags("changelog")
	_ = fs.Parse(args) // ExitOnError: never returns an error
	a := fs.Args()
	switch {
	case len(a) >= 4 && a[0] == "add":
		p, err := addEntry(".", a[1], time.Now(), a[2], strings.Join(a[3:], " "))
		if err != nil {
			return err
		}
		fmt.Println("wrote", filepath.ToSlash(p))
		return nil
	case len(a) == 2 && a[0] == "show":
		s, err := showLog(".", a[1])
		if err != nil {
			return err
		}
		fmt.Print(s)
		return nil
	}
	return errors.New("usage: changelog add <DOCUMENT> <topic> <text> | changelog show <DOCUMENT>")
}
