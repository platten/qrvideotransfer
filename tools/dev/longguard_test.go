package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// longGuardProblems returns every place in one Go file that reads longTestsVar other than as
// os.Getenv(longTestsVar) == "1" or != "1": a guard such as != "" would run the long tests for
// QRVT_LONG_TESTS=0 too.
func longGuardProblems(fset *token.FileSet, f *ast.File) []string {
	var probs []string
	ok := map[ast.Node]bool{} // the os.Getenv calls inside an accepted comparison
	ast.Inspect(f, func(n ast.Node) bool {
		if b, is := n.(*ast.BinaryExpr); is && (b.Op == token.EQL || b.Op == token.NEQ) {
			for _, pair := range [][2]ast.Expr{{b.X, b.Y}, {b.Y, b.X}} {
				if isGetenvLong(pair[0]) && isString(pair[1], "1") {
					ok[pair[0]] = true
				}
			}
		}
		return true
	})
	ast.Inspect(f, func(n ast.Node) bool {
		switch e := n.(type) {
		case *ast.CallExpr:
			if isGetenvLong(e) && !ok[e] {
				probs = append(probs, fset.Position(e.Pos()).String()+": "+longTestsVar+" read other than == \"1\" or != \"1\"")
			}
			if isGetenvLong(e) {
				return false // its argument is the name, checked here
			}
		case *ast.BasicLit:
			if isString(e, longTestsVar) {
				probs = append(probs, fset.Position(e.Pos()).String()+": "+longTestsVar+" named outside os.Getenv(...) == \"1\"")
			}
		}
		return true
	})
	return probs
}

// isGetenvLong reports whether e is os.Getenv(longTestsVar).
func isGetenvLong(e ast.Expr) bool {
	c, ok := e.(*ast.CallExpr)
	if !ok || len(c.Args) != 1 {
		return false
	}
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Getenv" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "os" && isString(c.Args[0], longTestsVar)
}

// isString reports whether e is the string literal s.
func isString(e ast.Expr, s string) bool {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	v, err := strconv.Unquote(lit.Value)
	return err == nil && v == s
}

// TestLongTestGuards: every _test.go file of the module turns its long tests on only for
// QRVT_LONG_TESTS=1, exactly as tools/dev test -long sets it (DESIGN.md section 19: three guards
// had treated any non-empty value as on).
func TestLongTestGuards(t *testing.T) {
	fset := token.NewFileSet()
	var probs []string
	files := 0
	err := filepath.WalkDir(filepath.Join("..", ".."), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p != filepath.Join("..", "..") && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		files++
		probs = append(probs, longGuardProblems(fset, f)...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files < 100 {
		t.Fatalf("scanned %d test files; the walk missed the module", files)
	}
	for _, p := range probs {
		t.Error(p)
	}

	// The check itself, on the forms it must accept and refuse.
	for _, c := range []struct {
		src string
		bad int
	}{
		{`func f() bool { return os.Getenv("QRVT_LONG_TESTS") == "1" }`, 0},
		{`func f() bool { return "1" != os.Getenv("QRVT_LONG_TESTS") }`, 0},
		{`func f() bool { return os.Getenv("QRVT_LONG_TESTS") != "" }`, 1},
		{`func f() bool { return os.Getenv("QRVT_LONG_TESTS") == "" }`, 1},
		{`func f() bool { return os.Getenv("QRVT_LONG_TESTS") == "true" }`, 1},
		{`func f() bool { v := os.Getenv("QRVT_LONG_TESTS"); return v == "1" }`, 1},
		{`func f() bool { _, ok := os.LookupEnv("QRVT_LONG_TESTS"); return ok }`, 1},
		{`const name = "QRVT_LONG_TESTS"`, 1},
		{`func f() { t.Skip("set QRVT_LONG_TESTS=1") }`, 0},
	} {
		f, err := parser.ParseFile(fset, "x_test.go", "package x\n"+c.src, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := longGuardProblems(fset, f); len(got) != c.bad {
			t.Errorf("%s: %d problems %q, want %d", c.src, len(got), got, c.bad)
		}
	}
}
