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

// scanOptionalInterfaceNames parses filename (a source file in this
// package's own directory) and returns the name of every top-level `type X
// interface { ... }` declaration found there, excluding CodeForge and
// IssueTracker — the two mandatory interfaces every adapter must implement
// directly, as opposed to the optional seams Capabilities resolves.
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

// packageGoFiles returns every top-level *.go file in this package's own
// directory, excluding _test.go files. go test runs with the package
// directory as its working directory, so "." is this package. Scanning the
// directory rather than a hardcoded filename list means a future optional
// interface arriving in a new file (this package's own precedent: pagelimit.go
// was a new file introduced for one new optional seam) is picked up
// automatically, with no filename to remember to add.
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

// scanResolvedFieldNames parses filename looking for the declaration of
// func ResolveCapabilities and returns the set of Capabilities field names
// it assigns -- every X such that the function body contains an assignment
// whose LHS is the selector c.X. This is what lets the completeness test
// below catch a field added to Capabilities with no matching assignment
// line in ResolveCapabilities: without this check, such a field passes the
// "field exists" check and stays permanently nil at runtime, silently
// unresolvable.
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

// TestScanResolvedFieldNames_FindsKnownAssignments is a positive-coverage
// check on scanResolvedFieldNames itself: applied to the real
// capabilities.go, it must find the assignment for a handful of fields
// spanning both the CodeForge-side and IssueTracker-side halves of
// ResolveCapabilities. Guards the scanner's own AST-walking logic before
// TestCapabilities_CoversEveryOptionalInterface relies on it below.
func TestScanResolvedFieldNames_FindsKnownAssignments(t *testing.T) {
	assigned := scanResolvedFieldNames(t, "capabilities.go")
	for _, name := range []string{"BundleRelay", "PRForge", "BlockersLister", "FullyPaginated"} {
		if !assigned[name] {
			t.Errorf("scanResolvedFieldNames did not find an assignment for %s in ResolveCapabilities", name)
		}
	}
}

// TestCapabilities_CoversEveryOptionalInterface enforces the correspondence
// issue #2945 requires between the optional forge/tracker seam interfaces
// declared anywhere in this package's own directory and Capabilities' own
// fields: parsing the source (rather than hand-listing today's interfaces
// or today's files) means a future interface added to any file in this
// package without a matching Capabilities field fails this test, instead of
// silently becoming unresolvable through ResolveCapabilities. It also
// checks the reverse direction: every interface-typed field on Capabilities
// must name an interface the scan actually found, so a field can't silently
// outlive the interface it once resolved. And it checks a third direction:
// every found interface must have a corresponding assignment line inside
// ResolveCapabilities's body, so a field can't exist on Capabilities while
// staying permanently nil because nobody wrote the c.X, _ = cf.(X) line.
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
