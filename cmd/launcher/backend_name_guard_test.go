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

	"spindrift.dev/launcher/internal/backend"
)

// generatedHeader is the standard Go marker for generated files
// (https://go.dev/s/generatedcode); detecting it keeps the exemption from
// naming individual *_gen.go files.
var generatedHeader = regexp.MustCompile(`(?m)^// Code generated .* DO NOT EDIT\.$`)

// bundleFile is the one core file that wires every backend (ADR 0068).
const bundleFile = "internal/driver/bundle/backends.go"

type backendNameHit struct {
	Pos   string
	Value string
}

// scanBackendNameComparisons reports each backend-name string literal that is
// an operand of == or !=, a case label, or an argument to slices.Contains or
// slices.Index in src. Walking the AST skips comments, and
// exec.Command("git", ...) arguments or assignments are not comparisons, so
// only a branch on a backend name is flagged. It is a tripwire, not a proof:
// map-key lookups and a backend name held in a named constant escape it.
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

func registeredBackendNames() map[string]bool {
	names := make(map[string]bool, len(backend.Registry))
	for _, d := range backend.Registry {
		names[d.Name] = true
	}
	return names
}

// TestCoreDoesNotCompareBackendNames fails when a core file branches on a
// registered backend name (ADR 0068): behavior varies through backendkit
// Strategies and descriptor facts, never a name test. Exempt: the backend
// packages internal/forge/<name>, the Bundle, and generated files. Names come
// from backend.Registry, so a new row is covered with no edit here.
//
// _test.go files are skipped: a test asserts per-backend outcomes
// (`if got != "local"` checks an expected value) rather than branching
// production behavior. The walk stops at cmd/launcher
// because dashboard/ is a separate Go module this test cannot see; the Nix side
// (nix/checks/backend-name-guard.nix) scans its non-test .go files.
func TestCoreDoesNotCompareBackendNames(t *testing.T) {
	names := registeredBackendNames()
	if len(names) == 0 {
		t.Fatal("backend.Registry is empty: the guard has no names to look for")
	}
	exemptDir := map[string]bool{}
	for n := range names {
		exemptDir["internal/forge/"+n] = true
	}

	scanned := map[string]bool{}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		slash := filepath.ToSlash(path)
		if d.IsDir() {
			if exemptDir[slash] || (path != "." && (d.Name() == "testdata" || strings.HasPrefix(d.Name(), "."))) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || slash == bundleFile {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if generatedHeader.Match(raw) {
			return nil
		}
		scanned[slash] = true
		for _, h := range scanBackendNameComparisons(t, path, raw, names) {
			t.Errorf("%s: core code compares backend name %q; vary through a backendkit Strategy or descriptor fact instead", h.Pos, h.Value)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk .: %v", err)
	}
	// Known core files: if the walk misses one it is matching the wrong tree.
	for _, f := range []string{"main.go", "internal/backend/registry.go", "internal/dispatchkind/dispatchkind.go"} {
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
