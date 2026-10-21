package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// generatedHeader is the standard Go marker for generated files
// (https://go.dev/s/generatedcode).
var generatedHeader = regexp.MustCompile(`(?m)^// Code generated .* DO NOT EDIT\.$`)

type backendNameHit struct {
	Pos   string
	Value string
}

// scanBackendNameComparisons reports each backend-name string literal that is
// an operand of == or !=, a case label, or an argument to slices.Contains or
// slices.Index in src. Walking the AST skips comments, and
// exec.Command("git", ...) arguments or assignments are not comparisons, so
// only a branch on a backend name is flagged. It is a tripwire, not a proof:
// map-key lookups, a backend name held in a named constant, an aliased slices
// import, slices.ContainsFunc, and strings.Contains-style helpers, for example,
// escape it.
func scanBackendNameComparisons(t *testing.T, filename string, src any, names map[string]bool) []backendNameHit {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		t.Fatalf("ParseFile(%s): %v", filename, err)
	}
	var hits []backendNameHit
	check := func(e ast.Expr) {
		lit, ok := e.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return
		}
		v, err := strconv.Unquote(lit.Value)
		if err != nil || !names[v] {
			return
		}
		hits = append(hits, backendNameHit{Pos: fset.Position(lit.Pos()).String(), Value: v})
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.BinaryExpr:
			if n.Op == token.EQL || n.Op == token.NEQ {
				check(n.X)
				check(n.Y)
			}
		case *ast.CaseClause:
			for _, e := range n.List {
				check(e)
			}
		case *ast.CallExpr:
			sel, ok := n.Fun.(*ast.SelectorExpr)
			if !ok {
				break
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "slices" && (sel.Sel.Name == "Contains" || sel.Sel.Name == "Index") {
				for _, a := range n.Args {
					check(a)
				}
			}
		}
		return true
	})
	return hits
}

// TestDashboardDoesNotCompareBackendNames fails when a dashboard file branches
// on a backend name (ADR 0068). It mirrors cmd/launcher's
// TestCoreDoesNotCompareBackendNames for this separate Go module, which cannot
// import backend.Registry: the names come from backendnames_gen_test.go,
// rendered from lib/backends, so a new row is covered with no edit here.
// _test.go and generated files are skipped.
func TestDashboardDoesNotCompareBackendNames(t *testing.T) {
	if len(backendNames) == 0 {
		t.Fatal("backendNames is empty: the guard has no names to look for")
	}
	names := make(map[string]bool, len(backendNames))
	for _, n := range backendNames {
		names[n] = true
	}

	scanned := map[string]bool{}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != "." && (d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if generatedHeader.Match(raw) {
			return nil
		}
		scanned[filepath.ToSlash(path)] = true
		for _, h := range scanBackendNameComparisons(t, path, raw, names) {
			t.Errorf("%s: dashboard code compares backend name %q; vary through a fact the Daemon writes to its status file instead", h.Pos, h.Value)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk .: %v", err)
	}
	// Known dashboard files: if the walk misses one it is matching the wrong tree.
	for _, f := range []string{"main.go", "server.go"} {
		if !scanned[f] {
			t.Errorf("%s was not scanned: the walk is matching the wrong tree", f)
		}
	}
}

// TestBackendNameScannerSensitivity is the negative control: without it a
// scanner that matched nothing would pass the real tree.
func TestBackendNameScannerSensitivity(t *testing.T) {
	names := map[string]bool{"github": true, "git": true}
	const src = `package x

import "os/exec"

// if tracker == "github" in a comment is not a comparison.
func f(tracker string) {
	_ = exec.Command("git", "status")
	name := "github"
	_ = name
	if tracker == "github" {
	}
	if "git" != tracker {
	}
	switch tracker {
	case "jira", "git":
	case "other":
	}
	_ = tracker == "jira"
	_ = slices.Contains(xs, "github")
	_ = slices.Index(xs, "git")
	_ = slices.Contains(xs, "other")
}
`
	got := scanBackendNameComparisons(t, "fixture.go", src, names)
	var values []string
	for _, h := range got {
		values = append(values, h.Value)
	}
	if want := "github,git,git,github,git"; strings.Join(values, ",") != want {
		t.Fatalf("flagged %v, want [%s]", values, want)
	}
}
