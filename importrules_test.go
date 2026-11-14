package qrvt

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"path/filepath"
	"regexp"
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
// value or a method expression, nor on a reflect.Value made elsewhere. The one form allowed is a
// package-qualified name (atomic.Pointer, a type; reflect.Pointer, a Kind), and that is decided by
// type-checking each package: the left side must resolve to an imported package, so a local
// variable or parameter named like one (reflect, atomic) does not pass. The check is a guard against
// mistakes, not a proof (section 9.6). tools/, spikes/ and testdata/ are not visited. Test files and
// files of every build tag are included. It returns one line per violation.
func checkImports(t *testing.T, root string) []string {
	t.Helper()
	paths, _ := filepath.Glob(filepath.Join(root, "*.go"))
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && (d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			if !d.IsDir() && strings.HasSuffix(path, ".go") {
				paths = append(paths, path)
			}
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
	}
	// Parse every file, grouped by package: its directory and package clause (an external test
	// package is its own).
	fset := token.NewFileSet()
	groups := map[string][]*ast.File{}
	rels := map[*ast.File]string{}
	for _, path := range paths {
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(root, path)
		rels[f] = filepath.ToSlash(rel)
		k := filepath.Dir(path) + "\x00" + f.Name.Name
		groups[k] = append(groups[k], f)
	}
	var bad []string
	for _, files := range groups {
		// Type-check only to resolve names: every import is an empty package, and type errors (from
		// those empty packages, or from files of different build tags declaring the same name) are
		// ignored. What matters is which object each identifier denotes.
		info := &types.Info{Uses: map[*ast.Ident]types.Object{}}
		conf := types.Config{Importer: emptyImporter{}, Error: func(error) {}}
		_, _ = conf.Check(files[0].Name.Name, fset, files, info)
		for _, f := range files {
			bad = append(bad, checkFile(rels[f], f, info)...)
		}
	}
	slices.Sort(bad)
	return bad
}

// checkFile applies the rule to one parsed and type-checked file.
func checkFile(rel string, f *ast.File, info *types.Info) []string {
	var bad []string
	for _, im := range f.Imports {
		p, _ := strconv.Unquote(im.Path.Value)
		if p == "unsafe" || (p == "os/exec" && !strings.HasPrefix(rel, "internal/video/")) {
			bad = append(bad, rel+" imports "+p)
		}
	}
	if rel == "internal/video/worker_windows.go" {
		return bad
	}
	ast.Inspect(f, func(n ast.Node) bool {
		s, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch s.Sel.Name {
		case "Pointer", "UnsafeAddr", "UnsafePointer":
			if id, ok := s.X.(*ast.Ident); ok {
				if _, pkg := info.Uses[id].(*types.PkgName); pkg {
					return true // a package-qualified name
				}
			}
			bad = append(bad, rel+" selects "+s.Sel.Name)
		}
		return true
	})
	return bad
}

// emptyImporter gives every import as an empty package, named after its path's last element (the
// one before a /vN suffix): enough for the checker to know which identifiers name packages.
type emptyImporter struct{}

var majorVersion = regexp.MustCompile(`^v[0-9]+$`)

func (emptyImporter) Import(path string) (*types.Package, error) {
	elems := strings.Split(path, "/")
	name := elems[len(elems)-1]
	if majorVersion.MatchString(name) && len(elems) > 1 {
		name = elems[len(elems)-2]
	}
	p := types.NewPackage(path, strings.TrimPrefix(name, "go-"))
	p.MarkComplete()
	return p, nil
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
		"internal/bad/exempt.go imports github.com/platten/qrvideotransfer/spikes/addr",
		"internal/bad/exempt.go imports github.com/platten/qrvideotransfer/tools/dev",
		"internal/bad/ifacedata.go selects InterfaceData",
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
