package console

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestNoBareGoStatementsOnConsolePaths pins issue #3675: every goroutine
// spindrift starts on a path the Console can reach must go through
// panicguard.Go, so a panic restores the terminal before the process dies. A
// bare `go` statement in console's transitive internal import closure
// bypasses that hook. panicguard itself is exempt: it owns the one real `go`.
func TestNoBareGoStatementsOnConsolePaths(t *testing.T) {
	const moduleRoot = "../.."
	moduleName := guardModuleName(t, filepath.Join(moduleRoot, "go.mod"))
	root := moduleName + "/internal/console"
	// cmdConsole installs the stopsignal relay before console.Run, so it is
	// live while the Console owns the terminal despite no import edge.
	relay := moduleName + "/internal/stopsignal"
	guard := moduleName + "/internal/panicguard"

	visited := map[string]bool{root: true, relay: true}
	queue := []string{root, relay}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, imp := range guardNonTestImports(t, guardImportDir(moduleRoot, moduleName, cur)) {
			if strings.HasPrefix(imp, moduleName+"/") && !visited[imp] {
				visited[imp] = true
				queue = append(queue, imp)
			}
		}
	}

	if len(visited) < 3 || !visited[guard] {
		t.Fatalf("import walk reached %d packages and panicguard=%v -- a broken walker", len(visited), visited[guard])
	}

	for pkg := range visited {
		if pkg == guard {
			continue
		}
		for _, pos := range bareGoStatements(t, guardImportDir(moduleRoot, moduleName, pkg)) {
			t.Errorf("%s: bare go statement; use panicguard.Go", pos)
		}
	}
}

func TestBareGoStatements_FlagsOnlyNonTestSites(t *testing.T) {
	dir := t.TempDir()
	planted := "package p\n\nfunc f() {\n\tgo func() {}()\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "planted.go"), []byte(planted), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "planted_test.go"), []byte(planted), 0o644); err != nil {
		t.Fatal(err)
	}

	got := bareGoStatements(t, dir)
	want := filepath.Join(dir, "planted.go") + ":4:2"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("bareGoStatements = %v, want [%s]", got, want)
	}
}

// bareGoStatements returns the file:line:col of every `go` statement in dir's
// non-test files.
func bareGoStatements(t *testing.T, dir string) []string {
	t.Helper()
	var found []string
	fset := token.NewFileSet()
	for _, path := range guardNonTestFiles(t, dir) {
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if g, ok := n.(*ast.GoStmt); ok {
				found = append(found, fset.Position(g.Go).String())
			}
			return true
		})
	}
	return found
}

func guardNonTestFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading directory %s: %v", dir, err)
	}
	var paths []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		paths = append(paths, filepath.Join(dir, name))
	}
	return paths
}

func guardModuleName(t *testing.T, path string) string {
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

func guardImportDir(moduleRoot, moduleName, importPath string) string {
	rel := strings.TrimPrefix(importPath, moduleName+"/")
	if rel == importPath {
		return moduleRoot
	}
	return filepath.Join(moduleRoot, rel)
}

func guardNonTestImports(t *testing.T, dir string) []string {
	t.Helper()
	var imports []string
	fset := token.NewFileSet()
	for _, path := range guardNonTestFiles(t, dir) {
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
