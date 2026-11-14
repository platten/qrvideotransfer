package qrvt

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// checkImports applies the import rule of docs/SECURITY.md section 9.6 to the Go files under root:
// the root package itself, cmd/ and internal/. Only internal/video (and packages below it) may
// import os/exec, and nothing may import unsafe. Nor may any file but internal/video/worker_windows.go,
// the one reviewed exception, hold a selector named Pointer, UnsafeAddr or UnsafePointer, called or
// not, whatever it imports: so reflect cannot hand out an address through a method call, a method
// value or a method expression, nor on a reflect.Value made elsewhere. Package-qualified names
// (atomic.Pointer, a type; reflect.Pointer, a Kind) are not selectors on a value and are allowed. tools/, spikes/ and testdata/ are not visited. Test files and files of
// every build tag are included. It returns one line per violation.
func checkImports(t *testing.T, root string) []string {
	t.Helper()
	var bad []string
	check := func(path string) {
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		video := strings.HasPrefix(rel, "internal/video/")
		pkgs := map[string]bool{} // the names under which the file's imports are used
		for _, im := range f.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			if p == "unsafe" || (p == "os/exec" && !video) {
				bad = append(bad, rel+" imports "+p)
			}
			if im.Name != nil {
				pkgs[im.Name.Name] = true
			} else {
				pkgs[p[strings.LastIndex(p, "/")+1:]] = true
			}
		}
		if rel == "internal/video/worker_windows.go" {
			return
		}
		ast.Inspect(f, func(n ast.Node) bool {
			s, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch s.Sel.Name {
			case "Pointer", "UnsafeAddr", "UnsafePointer":
				if id, ok := s.X.(*ast.Ident); !ok || !pkgs[id.Name] { // a selector on a value or type, not a qualified name
					bad = append(bad, rel+" selects "+s.Sel.Name)
				}
			}
			return true
		})
	}
	files, _ := filepath.Glob(filepath.Join(root, "*.go"))
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && (d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			if !d.IsDir() && strings.HasSuffix(path, ".go") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
	}
	for _, f := range files {
		check(f)
	}
	slices.Sort(bad)
	return bad
}

// TestImportRules enforces the import rule on this repository.
func TestImportRules(t *testing.T) {
	if bad := checkImports(t, "."); len(bad) > 0 {
		t.Errorf("import rule of SECURITY.md section 9.6 broken:\n%s", strings.Join(bad, "\n"))
	}
}

// TestImportRulesDetects checks the checker on a fixture tree that breaks the rule on purpose.
func TestImportRulesDetects(t *testing.T) {
	got := checkImports(t, filepath.Join("testdata", "importrule"))
	want := []string{
		"cmd/tool/main_test.go imports unsafe",
		"internal/bad/bad.go imports os/exec",
		"internal/bad/bad.go imports unsafe",
		"internal/bad/lookalike.go selects Pointer",
		"internal/bad/methodexpr.go selects Pointer",
		"internal/bad/methodvalue.go selects Pointer",
		"internal/bad/noimport.go selects UnsafePointer",
		"internal/bad/refl.go selects Pointer",
		"internal/bad/refl.go selects UnsafeAddr",
		"internal/bad/shadowlocal.go selects Pointer",
		"internal/bad/shadowparam.go selects UnsafeAddr",
		"internal/video/sys/sys.go imports unsafe",
		"root.go imports os/exec",
	}
	if !slices.Equal(got, want) {
		t.Errorf("violations = %q, want %q", got, want)
	}
}
