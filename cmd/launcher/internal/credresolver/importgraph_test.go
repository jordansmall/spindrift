package credresolver

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

// TestImportGraphExcludesRegistryLayer enforces kinds.go's rule: credresolver's
// shipped code must never transitively import internal/ecosystem,
// internal/registrydiscover (a cycle: it imports credresolver), or
// internal/registryvocab. The walk skips _test.go files.
func TestImportGraphExcludesRegistryLayer(t *testing.T) {
	// go.mod lives two directories up, at the module root.
	const moduleRoot = "../.."
	moduleName := readModuleName(t, filepath.Join(moduleRoot, "go.mod"))

	closure, parent := importClosure(t, moduleRoot, moduleName, moduleName+"/internal/credresolver")
	for _, pkg := range forbiddenImports(moduleName) {
		if closure[pkg] {
			t.Errorf("credresolver's import closure reaches forbidden package %s via %s",
				pkg, importChain(parent, moduleName+"/internal/credresolver", pkg))
		}
	}
}

// TestImportClosureCatchesTransitiveForbiddenImport proves the walker is not
// vacuous: credresolver imports no internal package today, so the real test
// above would pass even if importClosure walked nothing.
func TestImportClosureCatchesTransitiveForbiddenImport(t *testing.T) {
	root := t.TempDir()
	const moduleName = "example.com/fixturemodule"
	writeFile(t, root, "go.mod", "module "+moduleName+"\n\ngo 1.24\n")
	writeFile(t, filepath.Join(root, "internal", "credresolver"), "c.go",
		"package credresolver\n\nimport _ \""+moduleName+"/internal/middle\"\n")
	writeFile(t, filepath.Join(root, "internal", "middle"), "m.go",
		"package middle\n\nimport _ \""+moduleName+"/internal/registryvocab\"\n")
	writeFile(t, filepath.Join(root, "internal", "registryvocab"), "v.go", "package registryvocab\n")

	rootImport := moduleName + "/internal/credresolver"
	closure, parent := importClosure(t, root, moduleName, rootImport)

	pkg := moduleName + "/internal/registryvocab"
	if !closure[pkg] {
		t.Fatalf("importClosure missed transitive import %s: got %v", pkg, closure)
	}
	want := rootImport + " -> " + moduleName + "/internal/middle -> " + pkg
	if got := importChain(parent, rootImport, pkg); got != want {
		t.Errorf("importChain = %q, want %q", got, want)
	}
}

func forbiddenImports(moduleName string) []string {
	return []string{
		moduleName + "/internal/ecosystem",
		moduleName + "/internal/registrydiscover",
		moduleName + "/internal/registryvocab",
	}
}

// importClosure BFS-walks the non-test imports of rootImport, returning every
// in-module package reached and each one's first-found importer.
func importClosure(t *testing.T, moduleRoot, moduleName, rootImport string) (closure map[string]bool, parent map[string]string) {
	t.Helper()
	parent = map[string]string{}
	closure = map[string]bool{}
	visited := map[string]bool{rootImport: true}
	queue := []string{rootImport}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		rel := strings.TrimPrefix(cur, moduleName+"/")
		for _, imp := range nonTestImports(t, filepath.Join(moduleRoot, rel)) {
			if !strings.HasPrefix(imp, moduleName+"/") {
				continue // stdlib or third-party
			}
			closure[imp] = true
			if !visited[imp] {
				visited[imp] = true
				parent[imp] = cur
				queue = append(queue, imp)
			}
		}
	}
	return closure, parent
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", name, err)
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
