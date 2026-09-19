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

// TestImportGraphExcludesEcosystemDiscoveryAndPathSet pins ADR 0047's boundary:
// registryproxy runs only the registryvocab.RewriteRow table its caller hands
// New, so its shipped code must never transitively import internal/ecosystem,
// internal/registrydiscover, or internal/registrypathset. The walk skips
// _test.go files, which do import internal/ecosystem for the round-trip tests.
func TestImportGraphExcludesEcosystemDiscoveryAndPathSet(t *testing.T) {
	// go.mod lives two directories up, at the module root.
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

func importDir(moduleRoot, moduleName, importPath string) string {
	rel := strings.TrimPrefix(importPath, moduleName+"/")
	if rel == importPath {
		return moduleRoot // importPath names the module root itself
	}
	return filepath.Join(moduleRoot, rel)
}

// nonTestImports fails the test rather than returning an error, so a directory
// this walk cannot read never lets the walk pass vacuously.
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
