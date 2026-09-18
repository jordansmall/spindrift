package forge

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
	"testing"
)

// scanOptionalInterfaceNames returns the name of every top-level interface
// declared in filename, excluding CodeForge and IssueTracker: those two are
// mandatory for every adapter, not optional seams Capabilities resolves.
func scanOptionalInterfaceNames(t *testing.T, filename string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, nil, 0)
	if err != nil {
		t.Fatalf("ParseFile(%s): %v", filename, err)
	}
	var names []string
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if _, ok := ts.Type.(*ast.InterfaceType); !ok {
				continue
			}
			if ts.Name.Name == "CodeForge" || ts.Name.Name == "IssueTracker" {
				continue
			}
			names = append(names, ts.Name.Name)
		}
	}
	return names
}

// packageGoFiles returns this package's own non-test *.go files. go test runs
// with the package directory as its working directory, so "." is this package.
// Scanning the directory rather than a hardcoded filename list picks up an
// optional interface that arrives in a new file, with no filename to remember.
func packageGoFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir(.): %v", err)
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, name)
	}
	return files
}

// scanResolvedFieldNames returns the Capabilities field names that
// ResolveCapabilities assigns, found as assignments whose left side is the
// selector c.X. A field with no such line passes the completeness test's
// "field exists" check but stays permanently nil at runtime, so that test
// needs this scan to catch it.
func scanResolvedFieldNames(t *testing.T, filename string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, nil, 0)
	if err != nil {
		t.Fatalf("ParseFile(%s): %v", filename, err)
	}
	assigned := make(map[string]bool)
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name.Name != "ResolveCapabilities" || fd.Body == nil {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for _, lhs := range as.Lhs {
				sel, ok := lhs.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				recv, ok := sel.X.(*ast.Ident)
				if !ok || recv.Name != "c" {
					continue
				}
				assigned[sel.Sel.Name] = true
			}
			return true
		})
	}
	return assigned
}

// This test checks scanResolvedFieldNames itself. The fields it lists span both
// the CodeForge-side and IssueTracker-side halves of ResolveCapabilities, so the
// completeness test below does not depend on an unverified AST walk.
func TestScanResolvedFieldNames_FindsKnownAssignments(t *testing.T) {
	assigned := scanResolvedFieldNames(t, "capabilities.go")
	for _, name := range []string{"BundleRelay", "PRForge", "BlockersLister", "FullyPaginated"} {
		if !assigned[name] {
			t.Errorf("scanResolvedFieldNames did not find an assignment for %s in ResolveCapabilities", name)
		}
	}
}

// Issue #2945 requires the optional seam interfaces declared in this package
// and Capabilities' fields to stay in correspondence. The test parses the
// source rather than hand-listing today's interfaces or files, and checks all
// three directions: every interface has a field, every interface-typed field
// names an interface the scan found, and ResolveCapabilities assigns each one.
func TestCapabilities_CoversEveryOptionalInterface(t *testing.T) {
	var interfaceNames []string
	for _, f := range packageGoFiles(t) {
		interfaceNames = append(interfaceNames, scanOptionalInterfaceNames(t, f)...)
	}
	if len(interfaceNames) == 0 {
		t.Fatal("scan found zero optional interfaces -- would pass vacuously")
	}

	capType := reflect.TypeOf(Capabilities{})
	resolvedFields := scanResolvedFieldNames(t, "capabilities.go")

	foundSet := make(map[string]bool, len(interfaceNames))
	for _, name := range interfaceNames {
		foundSet[name] = true
		field, ok := capType.FieldByName(name)
		if !ok {
			t.Errorf("interface %s has no matching Capabilities field", name)
			continue
		}
		if field.Type.Kind() != reflect.Interface {
			t.Errorf("Capabilities.%s has kind %s, want interface", name, field.Type.Kind())
		} else if field.Type.Name() != name {
			t.Errorf("Capabilities.%s has type %s, want interface %s", name, field.Type.Name(), name)
		}
		if !resolvedFields[name] {
			t.Errorf("ResolveCapabilities never assigns Capabilities.%s", name)
		}
	}

	for i := 0; i < capType.NumField(); i++ {
		field := capType.Field(i)
		if field.Type.Kind() != reflect.Interface {
			continue
		}
		if !foundSet[field.Name] {
			t.Errorf("Capabilities.%s is an interface-typed field but no interface named %s was found by the scan", field.Name, field.Name)
		}
	}
}
