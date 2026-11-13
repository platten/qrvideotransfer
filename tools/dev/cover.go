package main

import (
	"bufio"
	"fmt"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const module = "github.com/platten/qrvideotransfer"

// generated matches the Go convention for generated files.
var generated = regexp.MustCompile(`(?m)^// Code generated .* DO NOT EDIT\.$`)

// excluded reports whether a file (module-relative, slash-separated) is outside the coverage gate:
// cmd/qrvt/main.go, tools/, spikes/ and generated tables. Keep in step with .testcoverage.yml.
func excluded(rel string) bool {
	if rel == "cmd/qrvt/main.go" || strings.HasPrefix(rel, "tools/") || strings.HasPrefix(rel, "spikes/") ||
		strings.HasSuffix(rel, "_gen.go") {
		return true
	}
	b, err := os.ReadFile(rel)
	return err == nil && generated.Match(b)
}

// cover runs the tests with coverage and enforces a minimum statement coverage per package.
func cover(args []string) error {
	fs := newFlags("cover")
	minimum := fs.Float64("min", 95, "minimum statement coverage per package, percent")
	profile := fs.String("profile", "coverage.out", "coverage profile to write")
	_ = fs.Parse(args) // ExitOnError: never returns an error
	tags, err := releaseTags()
	if err != nil {
		return err
	}
	if err := run([]string{"CGO_ENABLED=0"}, "go", goTest(tags, "-coverprofile="+*profile, "./...")...); err != nil {
		return err
	}
	pkgs, err := parseProfile(*profile)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(pkgs))
	for p := range pkgs {
		names = append(names, p)
	}
	sort.Strings(names)
	var failed []string
	for _, p := range names {
		c := pkgs[p]
		pct := 100 * float64(c[1]) / float64(c[0])
		mark := "ok  "
		if pct < *minimum {
			mark = "FAIL"
			failed = append(failed, p)
		}
		fmt.Printf("%s %6.1f%%  %5d statements  %s\n", mark, pct, c[0], p)
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d packages below %.0f%% coverage: %s", len(failed), *minimum, strings.Join(failed, ", "))
	}
	fmt.Printf("coverage gate passed: every package at least %.0f%% (packages without statements are skipped)\n", *minimum)
	return nil
}

// parseProfile returns, per package directory, the total and covered statements of a coverage
// profile, merging duplicate blocks and skipping excluded files.
func parseProfile(name string) (map[string][2]int, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	type block struct{ stmts, count int }
	blocks := map[string]block{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		if strings.HasPrefix(line, "mode:") || line == "" {
			continue
		}
		// file:start.col,end.col statements count
		f := strings.Fields(line)
		if len(f) != 3 {
			return nil, fmt.Errorf("bad profile line %q", line)
		}
		stmts, err1 := strconv.Atoi(f[1])
		count, err2 := strconv.Atoi(f[2])
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("bad profile line %q", line)
		}
		b := blocks[f[0]]
		blocks[f[0]] = block{stmts, max(b.count, count)}
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	pkgs := map[string][2]int{}
	skip := map[string]bool{}
	for key, b := range blocks {
		file := key[:strings.LastIndex(key, ":")]
		rel := strings.TrimPrefix(strings.TrimPrefix(file, module), "/")
		ex, ok := skip[rel]
		if !ok {
			ex = excluded(rel)
			skip[rel] = ex
		}
		if ex {
			continue
		}
		p := path.Dir(file)
		c := pkgs[p]
		c[0] += b.stmts
		if b.count > 0 {
			c[1] += b.stmts
		}
		pkgs[p] = c
	}
	return pkgs, nil
}
