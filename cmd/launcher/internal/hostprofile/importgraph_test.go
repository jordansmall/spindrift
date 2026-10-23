package hostprofile

import (
	"bufio"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/testutil"
)

// The advisory package and file roots are the enforcement paths ADR 0071
// keeps host-profile-free: the proxy and route parsing as whole packages, and
// the launcher's route resolution as one file, because that lives in package
// main and walking all of main would flag every future legitimate
// host-profile consumer. The cost: a main-package helper the file calls,
// such as checkoutIsTargetRepo in registryroutesdrift_doctor_checks.go (a
// doctor file), is outside the guard.
var (
	advisoryPackageRoots = []string{"internal/registryproxy", "internal/registryroutes"}
	advisoryFileRoots    = []string{"registryroutesresolve.go"}
)

// The core go test run lacks package main's files (nix/checks/go.nix strips
// them), so an absent file root is skipped there. launcher-binaries-go-test
// copies the whole tree and sets SPINDRIFT_HOSTPROFILE_GUARD_FULL_TREE, where
// an absent file root is an error.
func TestAdvisoryBoundaryHoldsOnRealTree(t *testing.T) {
	// go.mod lives two directories up, at the module root.
	const moduleRoot = "../.."
	moduleName := readModuleName(t, filepath.Join(moduleRoot, "go.mod"))

	var fileRoots []string
	for _, rel := range advisoryFileRoots {
		if _, err := os.Stat(filepath.Join(moduleRoot, rel)); err == nil {
			fileRoots = append(fileRoots, rel)
		} else if os.Getenv("SPINDRIFT_HOSTPROFILE_GUARD_FULL_TREE") != "" {
			t.Errorf("%s: an advisory file root is absent from the tree", rel)
		}
	}

	if got := advisoryViolations(t, moduleRoot, moduleName, advisoryPackageRoots, fileRoots); len(got) > 0 {
		t.Errorf("advisory boundary broken (ADR 0071):\n%s", strings.Join(got, "\n"))
	}
}

// TestAdvisoryBoundaryCatchesPlantedImports proves the walker is not vacuous:
// each root reaches hostprofile in the fixture, one of them transitively.
func TestAdvisoryBoundaryCatchesPlantedImports(t *testing.T) {
	root := t.TempDir()
	const moduleName = "example.com/fixturemodule"
	hp := moduleName + "/internal/hostprofile"
	testutil.WriteFixture(t, root, "go.mod", "module "+moduleName+"\n\ngo 1.24\n")
	testutil.WriteFixture(t, filepath.Join(root, "internal", "hostprofile"), "h.go", "package hostprofile\n")
	testutil.WriteFixture(t, filepath.Join(root, "internal", "helper"), "h.go",
		"package helper\n\nimport _ \""+hp+"\"\n")
	testutil.WriteFixture(t, filepath.Join(root, "internal", "registryproxy"), "p.go",
		"package registryproxy\n\nimport _ \""+moduleName+"/internal/helper\"\n")
	testutil.WriteFixture(t, filepath.Join(root, "internal", "registryroutes"), "r.go",
		"package registryroutes\n\nimport _ \""+hp+"\"\n")
	testutil.WriteFixture(t, root, "registryroutesresolve.go",
		"package main\n\nimport _ \""+hp+"\"\n")
	// Test files may import hostprofile; only shipped code is the boundary.
	testutil.WriteFixture(t, filepath.Join(root, "internal", "registryroutes"), "r_test.go",
		"package registryroutes\n\nimport _ \""+hp+"\"\n")

	got := advisoryViolations(t, root, moduleName, advisoryPackageRoots, advisoryFileRoots)
	want := []string{
		moduleName + "/internal/registryproxy -> " + moduleName + "/internal/helper -> " + hp,
		moduleName + "/internal/registryroutes -> " + hp,
		"registryroutesresolve.go -> " + hp,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("violations = %q, want %q", got, want)
	}
}

// advisoryViolations returns one import chain per root that reaches
// <module>/internal/hostprofile, in packageRoots then fileRoots order.
func advisoryViolations(t *testing.T, moduleRoot, moduleName string, packageRoots, fileRoots []string) []string {
	t.Helper()
	forbidden := moduleName + "/internal/hostprofile"

	type seed struct {
		label   string
		imports []string
	}
	var seeds []seed
	for _, rel := range packageRoots {
		seeds = append(seeds, seed{moduleName + "/" + rel, nonTestImports(t, filepath.Join(moduleRoot, rel))})
	}
	for _, rel := range fileRoots {
		seeds = append(seeds, seed{rel, fileImports(t, token.NewFileSet(), filepath.Join(moduleRoot, rel))})
	}

	var violations []string
	for _, s := range seeds {
		parent := map[string]string{}
		visited := map[string]bool{}
		queue := []string{}
		visit := func(from string, imports []string) {
			for _, imp := range imports {
				if strings.HasPrefix(imp, moduleName+"/") && !visited[imp] {
					visited[imp] = true
					parent[imp] = from
					queue = append(queue, imp)
				}
			}
		}
		visit(s.label, s.imports)
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			visit(cur, nonTestImports(t, filepath.Join(moduleRoot, strings.TrimPrefix(cur, moduleName+"/"))))
		}
		if visited[forbidden] {
			chain := []string{forbidden}
			for cur := forbidden; parent[cur] != s.label; cur = parent[cur] {
				chain = append([]string{parent[cur]}, chain...)
			}
			violations = append(violations, s.label+" -> "+strings.Join(chain, " -> "))
		}
	}
	return violations
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
		imports = append(imports, fileImports(t, fset, filepath.Join(dir, name))...)
	}
	return imports
}

func fileImports(t *testing.T, fset *token.FileSet, path string) []string {
	t.Helper()
	f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	var imports []string
	for _, imp := range f.Imports {
		value, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			t.Fatalf("unquoting import %s in %s: %v", imp.Path.Value, path, err)
		}
		imports = append(imports, value)
	}
	return imports
}
