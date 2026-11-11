package seamtest

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

const driverkitPath = "spindrift.dev/launcher/internal/driver/driverkit"

// nativeDecoderSites returns file:line for every native Strategy decoder in
// src: a driverkit.Strategy literal setting NewDecoder (under the file's own
// import name for driverkit, or bare when dot-imported; also as a type-elided
// element of a slice, array or map of Strategy), or an assignment to
// <expr>.NewDecoder. A positional literal is flagged too: it must set every
// field, NewDecoder included.
func nativeDecoderSites(t *testing.T, filename string, src any) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	pkgName, dot := "", false
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p != driverkitPath {
			continue
		}
		switch {
		case imp.Name == nil:
			pkgName = "driverkit"
		case imp.Name.Name == ".":
			dot = true
		default:
			pkgName = imp.Name.Name
		}
	}
	// isStrategy reports whether e names driverkit.Strategy, through pointers.
	var isStrategy func(e ast.Expr) bool
	isStrategy = func(e ast.Expr) bool {
		switch e := e.(type) {
		case *ast.StarExpr:
			return isStrategy(e.X)
		case *ast.Ident:
			return dot && e.Name == "Strategy"
		case *ast.SelectorExpr:
			x, ok := e.X.(*ast.Ident)
			return ok && pkgName != "" && x.Name == pkgName && e.Sel.Name == "Strategy"
		}
		return false
	}
	var sites []string
	hit := func(p token.Pos) { sites = append(sites, fset.Position(p).String()) }
	flagLit := func(lit *ast.CompositeLit) {
		for _, e := range lit.Elts {
			kv, ok := e.(*ast.KeyValueExpr)
			if !ok {
				hit(lit.Pos())
				return
			}
			if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "NewDecoder" {
				hit(kv.Pos())
			}
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CompositeLit:
			var elem ast.Expr
			switch typ := n.Type.(type) {
			case *ast.ArrayType:
				elem = typ.Elt
			case *ast.MapType:
				elem = typ.Value
			}
			switch {
			case n.Type != nil && isStrategy(n.Type):
				flagLit(n)
			case elem != nil && isStrategy(elem):
				for _, e := range n.Elts {
					if kv, ok := e.(*ast.KeyValueExpr); ok {
						e = kv.Value
					}
					if lit, ok := e.(*ast.CompositeLit); ok && lit.Type == nil {
						flagLit(lit)
					}
				}
			}
		case *ast.AssignStmt:
			for _, l := range n.Lhs {
				if sel, ok := l.(*ast.SelectorExpr); ok && sel.Sel.Name == "NewDecoder" {
					hit(sel.Pos())
				}
			}
		}
		return true
	})
	return sites
}

// TestNoNativeStrategyOutsideDrivers fails when core builds a native
// driverkit.Strategy decoder. ADR 0067 is all-or-none: core's fake Driver is
// the compiled Connector (seamtest.Strategy), never a native Strategy (issue
// #6794). The bundled Drivers (internal/driver) and the Connector adapter
// (internal/connectorhost) are the only legitimate builders.
func TestNoNativeStrategyOutsideDrivers(t *testing.T) {
	root := filepath.Join("..", "..")
	allowed := []string{"internal/driver", "internal/connectorhost"}
	checked := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch d.Name() {
			case "vendor", "testdata":
				return filepath.SkipDir
			}
			for _, a := range allowed {
				if rel == a {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		checked++
		for _, s := range nativeDecoderSites(t, path, nil) {
			t.Errorf("%s: native driverkit.Strategy decoder outside internal/driver and internal/connectorhost; use the Connector (seamtest.Strategy)", s)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatalf("no .go files found under %s; the walk is inert", root)
	}
}

func TestNativeDecoderDetector(t *testing.T) {
	const path = driverkitPath
	const pre = "package p\nimport \"" + path + "\"\n"
	for name, tc := range map[string]struct {
		src  string
		want int
	}{
		"literal":    {pre + "var s = driverkit.Strategy{Name: \"x\", NewDecoder: f}\n", 1},
		"assignment": {pre + "func g() { var s driverkit.Strategy; s.NewDecoder = f }\n", 1},
		"clean": {pre + "var a = driverkit.PassOptions{NewDecoder: f}\n" +
			"var b = driverkit.Strategy{Name: \"x\"}\n", 0},
		"alias": {"package p\nimport dk \"" + path + "\"\n" +
			"var s = dk.Strategy{NewDecoder: f}\n", 1},
		"alias-passoptions": {"package p\nimport dk \"" + path + "\"\n" +
			"var s = dk.PassOptions{NewDecoder: f}\n", 0},
		"dot-import": {"package p\nimport . \"" + path + "\"\n" +
			"var s = Strategy{NewDecoder: f}\nvar o = PassOptions{NewDecoder: f}\n", 1},
		"elided-slice":      {pre + "var s = []driverkit.Strategy{{Name: \"x\"}, {NewDecoder: f}}\n", 1},
		"elided-ptr":        {pre + "var s = []*driverkit.Strategy{{NewDecoder: f}}\n", 1},
		"elided-map":        {pre + "var s = map[string]driverkit.Strategy{\"a\": {NewDecoder: f}}\n", 1},
		"positional":        {pre + "var s = driverkit.Strategy{\"x\", f}\n", 1},
		"elided-positional": {pre + "var s = []driverkit.Strategy{{\"x\", f}}\n", 1},
		"empty":             {pre + "var s = driverkit.Strategy{}\n", 0},
		"elided-passoptions": {pre +
			"var s = []driverkit.PassOptions{{NewDecoder: f}}\n", 0},
		"other-package-strategy": {"package p\nimport \"other\"\n" +
			"var s = other.Strategy{NewDecoder: f}\n", 0},
	} {
		if got := nativeDecoderSites(t, name+".go", tc.src); len(got) != tc.want {
			t.Errorf("%s: got %v, want %d site(s)", name, got, tc.want)
		}
	}
}
