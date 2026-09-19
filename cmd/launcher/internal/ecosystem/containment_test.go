package ecosystem

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

type ecosystemLiteral struct {
	File  string
	Line  int
	Value string
}

// scanForEcosystemLiterals returns one finding per string literal in dir's
// non-test .go files whose entire unquoted value equals a Table row Name.
// Equality is whole-literal, so the scan skips "cargo config.json" and prose,
// and walking the AST skips comments. It reads dir's own files only;
// deriveEcosystemImporters lists every package directory separately.
func scanForEcosystemLiterals(t *testing.T, dir string) []ecosystemLiteral {
	t.Helper()

	// A missing dir fails the test loudly rather than scanning zero files: a
	// target renamed out from under the check must not quietly drop coverage.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("scanForEcosystemLiterals: reading directory %s: %v", dir, err)
	}

	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}

	// Names come from Table itself, never a hardcoded list, so a row added
	// to Table is covered by every existing caller with no edit here.
	names := make(map[string]bool, len(Table))
	for _, row := range Table {
		names[strings.ToLower(row.Name)] = true
	}

	var findings []ecosystemLiteral
	fset := token.NewFileSet()
	for _, f := range files {
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("ParseFile(%s): %v", f, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if !names[strings.ToLower(value)] {
				return true
			}
			pos := fset.Position(lit.Pos())
			findings = append(findings, ecosystemLiteral{File: pos.Filename, Line: pos.Line, Value: value})
			return true
		})
	}
	return findings
}

func writeFixture(t *testing.T, dir, name, contents string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", name, err)
	}
}

// lineOf reports the 1-based line of the first line of src containing needle.
// Fixture sources are generated, so deriving the expected line keeps the
// assertions from drifting when the templates change.
func lineOf(t *testing.T, src, needle string) int {
	t.Helper()
	for i, line := range strings.Split(src, "\n") {
		if strings.Contains(line, needle) {
			return i + 1
		}
	}
	t.Fatalf("lineOf: %q not found in fixture source", needle)
	return 0
}

// TestScanForEcosystemLiterals_FixtureDetectsViolation pins that exactly the
// top-level violator is reported. The compliant file proves whole-literal
// equality does not over-fire on containment or comments, the _test.go file
// proves test files are out of scope, and the subpackage and the unparseable
// testdata file prove the scan never descends into either.
func TestScanForEcosystemLiterals_FixtureDetectsViolation(t *testing.T) {
	// The fixture draws its names from Table, so deleting a row cannot fail
	// this test for a reason unrelated to the scanner.
	if len(Table) < 4 {
		t.Fatalf("fixture needs 4 distinct ecosystem names, Table has %d rows", len(Table))
	}
	violatingName := Table[0].Name
	containedName := Table[1].Name
	testFileName := Table[2].Name
	subpkgName := Table[3].Name

	dir := t.TempDir()

	violatingSrc := fmt.Sprintf(`package fixture

func classify(name string) bool {
	if name == %q {
		return true
	}
	return false
}
`, violatingName)
	writeFixture(t, dir, "violating.go", violatingSrc)

	writeFixture(t, dir, "compliant.go", fmt.Sprintf(`package fixture

// configFile names the %s config file this package writes; it is not
// itself a routing decision on the %s ecosystem name.
func configFile() string {
	return %q
}
`, containedName, containedName, containedName+" config.json"))

	writeFixture(t, dir, "fixture_test.go", fmt.Sprintf(`package fixture

import "testing"

func TestBareLiteralOutOfScope(t *testing.T) {
	name := %q
	_ = name
}
`, testFileName))

	subDir := filepath.Join(dir, "subpkg")
	nestedSrc := fmt.Sprintf(`package subpkg

var routed = %q
`, subpkgName)
	writeFixture(t, subDir, "nested.go", nestedSrc)

	writeFixture(t, filepath.Join(dir, "testdata"), "broken.go", "this is not go source {{")

	findings := scanForEcosystemLiterals(t, dir)
	want := ecosystemLiteral{File: filepath.Join(dir, "violating.go"), Line: lineOf(t, violatingSrc, violatingName), Value: violatingName}
	if len(findings) != 1 || findings[0] != want {
		t.Fatalf("scanForEcosystemLiterals(%s) = %+v, want exactly [%+v]", dir, findings, want)
	}
}

// readModuleName reads the "module " directive out of the go.mod at path.
// Duplicated from registryproxy/importgraph_test.go rather than exported
// across packages, matching that file's own test-local style for the same
// helper.
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

// nonTestImports returns every import path named by dir's non-test *.go files.
// Duplicated from registryproxy/importgraph_test.go for the same
// test-local-style reason as readModuleName above.
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

// packageDirs lists root and every subdirectory under it that can hold a Go
// package, pruning testdata (which may hold deliberately unparseable or
// fixture .go files, as internal/console/msgcensus/testdata does) and
// dot-prefixed directories, which hold tooling state rather than source.
func packageDirs(t *testing.T, root string) []string {
	t.Helper()
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if path != root && (name == "testdata" || strings.HasPrefix(name, ".")) {
			return fs.SkipDir
		}
		dirs = append(dirs, path)
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(%s): %v", root, err)
	}
	return dirs
}

// deriveEcosystemImporters returns every directory under moduleRoot whose
// non-test .go files directly import moduleName+"/internal/ecosystem", plus
// moduleRoot itself, so a refactor routing main's ecosystem access through an
// intermediary package still leaves main covered. Direct imports only: the
// criterion is reaching Table, not being handed a row's Name as string data.
func deriveEcosystemImporters(t *testing.T, moduleRoot, moduleName string) []string {
	t.Helper()
	ecosystemImport := moduleName + "/internal/ecosystem"

	var importers []string
	for _, dir := range packageDirs(t, moduleRoot) {
		if slices.Contains(nonTestImports(t, dir), ecosystemImport) {
			importers = append(importers, dir)
		}
	}

	// Checked before moduleRoot is added: a broken walker must not pass
	// vacuously by falling back to the one guaranteed entry.
	if len(importers) == 0 {
		t.Fatalf("deriveEcosystemImporters(%s, %s): zero directories directly import %s", moduleRoot, moduleName, ecosystemImport)
	}

	if !slices.Contains(importers, moduleRoot) {
		importers = append(importers, moduleRoot)
	}
	slices.Sort(importers)
	return importers
}

// buildSyntheticEcosystemModule writes a throwaway module tree: a go.mod, an
// empty stub internal/ecosystem package, a package importing the stub that
// holds plantedReader as a bare literal, a sibling importing nothing, and a
// root file holding plantedRoot without importing the stub. One tree serves
// both fixture tests below, whose assertions do not interfere.
func buildSyntheticEcosystemModule(t *testing.T, plantedReader, plantedRoot string) (root, moduleName string) {
	t.Helper()
	root = t.TempDir()
	moduleName = "example.com/fixturemodule"

	writeFixture(t, root, "go.mod", fmt.Sprintf("module %s\n\ngo 1.24\n", moduleName))
	writeFixture(t, root, "mainpkg.go", fmt.Sprintf("package mainpkg\n\nvar routed = %q\n", plantedRoot))
	writeFixture(t, filepath.Join(root, "internal", "ecosystem"), "ecosystem.go", "package ecosystem\n")
	writeFixture(t, filepath.Join(root, "internal", "newreader"), "newreader.go", fmt.Sprintf(
		"package newreader\n\nimport _ %q\n\nvar routed = %q\n", moduleName+"/internal/ecosystem", plantedReader))
	writeFixture(t, filepath.Join(root, "internal", "sibling"), "sibling.go", "package sibling\n\nvar notRouted = \"unrelated\"\n")

	return root, moduleName
}

// TestDeriveEcosystemImporters_NewImporterPickedUpAutomatically proves a new
// importer needs no edit here: newreader turns up in the derived list purely
// because it imports the stub ecosystem package, and scanning it then reports
// its planted literal. sibling, which imports nothing, proves the
// derivation does not just list every directory in the tree.
func TestDeriveEcosystemImporters_NewImporterPickedUpAutomatically(t *testing.T) {
	if len(Table) < 1 {
		t.Fatalf("fixture needs 1 ecosystem name, Table has %d rows", len(Table))
	}
	plantedName := Table[0].Name
	root, moduleName := buildSyntheticEcosystemModule(t, plantedName, plantedName)

	derived := deriveEcosystemImporters(t, root, moduleName)

	readerDir := filepath.Join(root, "internal", "newreader")
	if !slices.Contains(derived, readerDir) {
		t.Fatalf("deriveEcosystemImporters(%s) = %v, want it to include %s", root, derived, readerDir)
	}
	siblingDir := filepath.Join(root, "internal", "sibling")
	if slices.Contains(derived, siblingDir) {
		t.Fatalf("deriveEcosystemImporters(%s) = %v, want it to exclude %s (imports nothing)", root, derived, siblingDir)
	}

	findings := scanForEcosystemLiterals(t, readerDir)
	if len(findings) != 1 || findings[0].Value != plantedName {
		t.Fatalf("scanForEcosystemLiterals(%s) = %+v, want a single finding of %q", readerDir, findings, plantedName)
	}
}

// TestDeriveEcosystemImporters_MainPackageCoveredUnconditionally proves main
// is included even without importing the ecosystem package. The real launcher
// main package does import internal/ecosystem today, but the second assertion
// relies only on the unconditional inclusion, not on that import staying true.
func TestDeriveEcosystemImporters_MainPackageCoveredUnconditionally(t *testing.T) {
	if len(Table) < 1 {
		t.Fatalf("fixture needs 1 ecosystem name, Table has %d rows", len(Table))
	}
	plantedName := Table[0].Name
	root, moduleName := buildSyntheticEcosystemModule(t, plantedName, plantedName)

	derived := deriveEcosystemImporters(t, root, moduleName)
	if !slices.Contains(derived, root) {
		t.Fatalf("deriveEcosystemImporters(%s) = %v, want it to include the module root %s", root, derived, root)
	}

	findings := scanForEcosystemLiterals(t, root)
	if len(findings) != 1 || findings[0].Value != plantedName {
		t.Fatalf("scanForEcosystemLiterals(%s) = %+v, want a single finding of %q", root, findings, plantedName)
	}

	const realModuleRoot = "../.."
	realModuleName := readModuleName(t, filepath.Join(realModuleRoot, "go.mod"))
	realDerived := deriveEcosystemImporters(t, realModuleRoot, realModuleName)
	if !slices.Contains(realDerived, realModuleRoot) {
		t.Fatalf("deriveEcosystemImporters(%s) = %v, want it to include the real launcher main package %s", realModuleRoot, realDerived, realModuleRoot)
	}
}

// TestEcosystemNamesStayInTheTable enforces that every package importing this
// one reaches an ecosystem fact through its Table row, never by re-naming the
// ecosystem as a bare literal. Targets come from deriveEcosystemImporters, so
// the set tracks imports with no edit here; internal/registryproxy is absent
// because ADR 0047 forbids it importing this package at all.
func TestEcosystemNamesStayInTheTable(t *testing.T) {
	const moduleRoot = "../.."
	moduleName := readModuleName(t, filepath.Join(moduleRoot, "go.mod"))
	targets := deriveEcosystemImporters(t, moduleRoot, moduleName)

	var findings []ecosystemLiteral
	for _, target := range targets {
		findings = append(findings, scanForEcosystemLiterals(t, target)...)
	}

	if len(findings) > 0 {
		lines := make([]string, 0, len(findings))
		for _, f := range findings {
			lines = append(lines, fmt.Sprintf("%s:%d: literal %q matches an ecosystem.Table row name", f.File, f.Line, f.Value))
		}
		t.Errorf("found %d ecosystem-name literal(s) outside ecosystem.Table:\n%s", len(findings), strings.Join(lines, "\n"))
	}
}
