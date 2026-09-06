package registryproxy

import (
	"bufio"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestImportGraphExcludesEcosystemDiscoveryAndPathSet pins ADR 0047's design
// boundary as a tripwire: registryproxy's own shipped code must never
// transitively import internal/ecosystem, internal/registrydiscover, or
// internal/registrypathset. Those three packages hold ecosystem discovery
// and path-set derivation; registryproxy's job is only to run whatever
// registryvocab.RewriteRow table its caller (the launcher) hands it (see
// New's rewriteRows parameter) -- a caller-supplied leaf/manifest split that
// an accidental import the other way would quietly erode.
//
// It walks only non-test *.go files. This package's own _test.go files
// (registryproxy_test.go, this file's siblings) legitimately import
// internal/ecosystem to drive cargo's real rewrite row through the
// round-trip tests -- that's a test-only exception the walk below is
// deliberately blind to, not a hole in the guard: it is the shipped
// package's import set that matters here, and go/parser's ImportsOnly mode
// on the non-test files is what pins that.
//
// This does not shell out to `go list`: it computes the closure itself with
// go/parser, following every spindrift.dev/launcher/... import to its
// directory under the module root and recursing, with a visited set so a
// cycle can't loop forever.
//
// This test rides the launcher-go-test check defined in nix/checks/go.nix,
// which nix/checks/default.nix already puts in sourceChecks and which is
// absent from imageOnlyCheckNames, so it reaches both `nix flake check` and
// `checks-inbox` with no new nix wiring (mirrors
// internal/ecosystem/containment_test.go's own reasoning for itself).
func TestImportGraphExcludesEcosystemDiscoveryAndPathSet(t *testing.T) {
	// The module root is "../.." from this package's own directory
	// (internal/registryproxy) -- go.mod lives there.
	const moduleRoot = "../.."
	moduleName := readModuleName(t, filepath.Join(moduleRoot, "go.mod"))
	rootImport := moduleName + "/internal/registryproxy"

	parent := map[string]string{}
	visited := map[string]bool{rootImport: true}
	closure := map[string]bool{}
	queue := []string{rootImport}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		for _, imp := range nonTestImports(t, importDir(moduleRoot, moduleName, cur)) {
			if !strings.HasPrefix(imp, moduleName+"/") {
				continue // stdlib or third-party: outside the internal closure this test walks
			}
			closure[imp] = true
			if !visited[imp] {
				visited[imp] = true
				parent[imp] = cur
				queue = append(queue, imp)
			}
		}
	}

	if len(closure) == 0 {
		t.Fatal("import walk reached zero internal packages -- a broken walker, not a genuinely empty import set")
	}

	forbidden := []string{
		moduleName + "/internal/ecosystem",
		moduleName + "/internal/registrydiscover",
		moduleName + "/internal/registrypathset",
	}
	for _, pkg := range forbidden {
		if closure[pkg] {
			t.Errorf("registryproxy's import closure reaches forbidden package %s via %s", pkg, importChain(parent, rootImport, pkg))
		}
	}
}

// readModuleName reads the "module " directive out of the go.mod at path.
func readModuleName(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if name, ok := strings.CutPrefix(strings.TrimSpace(scanner.Text()), "module "); ok {
			return strings.TrimSpace(name)
		}
	}
	t.Fatalf("%s: no module directive found", path)
	return ""
}

// importDir maps importPath to its directory on disk, given moduleRoot is
// where moduleName's go.mod lives.
func importDir(moduleRoot, moduleName, importPath string) string {
	rel := strings.TrimPrefix(importPath, moduleName+"/")
	if rel == importPath {
		return moduleRoot // importPath names the module root itself
	}
	return filepath.Join(moduleRoot, rel)
}

// nonTestImports returns every import path named by dir's non-test *.go
// files, parsed in ImportsOnly mode (no need to type-check or even parse
// function bodies -- only the import block matters here). Fails the test
// loudly rather than returning an error, so a directory this walk can't
// read never lets the walk pass vacuously.
func nonTestImports(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading directory %s: %v", dir, err)
	}

	var imports []string
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		for _, imp := range f.Imports {
			value, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("unquoting import %s in %s: %v", imp.Path.Value, path, err)
			}
			imports = append(imports, value)
		}
	}
	return imports
}

// importChain renders the root -> ... -> pkg path through parent, the
// cheapest-to-keep evidence for why pkg turned up in the closure at all.
func importChain(parent map[string]string, root, pkg string) string {
	chain := []string{pkg}
	for cur := pkg; ; {
		p, ok := parent[cur]
		if !ok {
			break
		}
		chain = append([]string{p}, chain...)
		cur = p
	}
	if chain[0] != root {
		chain = append([]string{root}, chain...)
	}
	return strings.Join(chain, " -> ")
}
