// Package msgcensus AST-walks a directory of Go source and reports the
// sorted list of type names whose method set includes a marker method named
// "isConsoleMsg" (any receiver form — value or pointer). It has no
// dependency on the console package; it is a generic walker parametrized
// only by directory path and the hardcoded marker method name.
package msgcensus

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"sort"
	"strings"
)

const markerMethod = "isConsoleMsg"

// Collect parses every non-test Go file in dir and returns the sorted list
// of type names that declare a method named "isConsoleMsg" (value or
// pointer receiver).
func Collect(dir string) ([]string, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		return nil, fmt.Errorf("msgcensus: parsing %s: %w", dir, err)
	}

	found := make(map[string]struct{})
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Name.Name != markerMethod {
					continue
				}
				if fn.Recv == nil || len(fn.Recv.List) != 1 {
					continue
				}

				name := receiverTypeName(fn.Recv.List[0].Type)
				if name != "" {
					found[name] = struct{}{}
				}
			}
		}
	}

	names := make([]string, 0, len(found))
	for name := range found {
		names = append(names, name)
	}
	sort.Strings(names)

	return names, nil
}

// receiverTypeName resolves a receiver's type expression to the underlying
// *ast.Ident name, unwrapping a pointer receiver's *ast.StarExpr.
func receiverTypeName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}

	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}

	return ""
}
