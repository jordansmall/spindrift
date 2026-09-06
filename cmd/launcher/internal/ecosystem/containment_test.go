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

// ecosystemLiteral is one scanner finding: where the offending literal sits
// and what it said. Findings stay structured so callers assert on fields and
// only the code rendering a failure message formats them.
type ecosystemLiteral struct {
	File  string
	Line  int
	Value string
}

// scanForEcosystemLiterals parses the package directory dir and returns one
// finding per string literal whose *entire* unquoted value equals a Table
// row's Name, case-insensitive.
//
// A bare "npm" outside the table is the shape this check exists to catch: a
// second, driftable home for a fact the row already owns.
//
// Equality is whole-literal, not substring: production code legitimately
// holds literals that *contain* an ecosystem name without routing on one,
// e.g. "cargo config.json" (a filename) or a flag-usage string mentioning
// cargo in prose. Flagging those would bury the true positives in noise.
//
// Walking *ast.BasicLit of Kind == token.STRING rather than grepping means
// comments naming an ecosystem are excluded for free -- a doc comment
// mentioning "gradle" is not a routing decision and must not be flagged.
//
// A directory contributes only its own non-test .go files -- it does not
// recurse. A subpackage is a package of its own and is a target on its own
// merits: it imports the ecosystem package or it doesn't.
// deriveEcosystemImporters enumerates every package directory in the tree
// already, so recursing here would only double-cover a child the derived
// target list already carries.
//
// If dir does not exist, this fails the test loudly (t.Fatalf) rather than
// silently scanning zero files: a target renamed out from under the check
// must not quietly drop coverage.
func scanForEcosystemLiterals(t *testing.T, dir string) []ecosystemLiteral {
	t.Helper()

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

// writeFixture writes contents to name inside dir, creating dir first so
// callers can drop a file into a not-yet-existing subdirectory in one line.
// Kept separate from the scanner under test so a write failure and a scan
// failure are never confused with each other in a test log.
func writeFixture(t *testing.T, dir, name, contents string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", name, err)
	}
}

// lineOf reports the 1-based line of the first line of src containing
// needle. Fixture sources are generated, so deriving the expected line this
// way keeps the assertions from drifting when the templates change.
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

// TestScanForEcosystemLiterals_FixtureDetectsViolation is the durable proof
// that a deliberate violation trips the check, and that scanning a
// directory never reaches into a subdirectory to find one. The fixture
// tree holds a production file that routes on a bare ecosystem literal, a
// production file that only holds a *containing* literal and a comment
// naming an ecosystem, a _test.go file that routes on a bare literal, a
// subpackage that routes on a bare literal, and a testdata directory
// holding an unparseable .go file. Exactly the top-level violator is
// reported: the compliant file proves whole-literal equality doesn't
// over-fire on containment or comments, the _test.go file proves test
// files are out of scope, and the subpackage and testdata cases both prove
// the same thing from opposite ends -- neither a parseable subpackage nor
// an unparseable testdata one is ever descended into, because scanning a
// directory covers only its own files. The subpackage is a target of its
// own on the derived list (see TestEcosystemNamesStayInTheTable), never
// covered by a parent's recursion.
//
// Every ecosystem name in the fixture is drawn from Table, so deleting a
// row can never make this test fail for a reason unrelated to the scanner.
func TestScanForEcosystemLiterals_FixtureDetectsViolation(t *testing.T) {
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
//
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

// nonTestImports returns every import path named by dir's non-test *.go
// files, parsed in ImportsOnly mode. Duplicated from
// registryproxy/importgraph_test.go for the same test-local-style reason as
// readModuleName above.
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

// packageDirs lists every directory at or under root that can hold a Go
// package: root itself, plus every subdirectory, pruning testdata (which
// may hold deliberately unparseable or fixture .go files -- see
// internal/console/msgcensus/testdata for a real one with its own nested
// packages) and dot-prefixed directories (tooling state, not package
// source).
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
// non-test .go files directly import moduleName+"/internal/ecosystem",
// plus moduleRoot itself unconditionally. moduleRoot is where the launcher
// main package lives (its go.mod's directory); it is included even when it
// doesn't import internal/ecosystem so that a future refactor routing
// main's ecosystem access through an intermediary package still leaves
// main covered.
//
// Direct imports only, not the transitive closure importgraph_test.go
// computes for a different purpose: the criterion is being able to reach
// Table. A package handed a row's Name as plain string data -- as
// registrypathset is, via registrydiscover -- could still compare against
// it, but is deliberately out of scope: it passes a fact through rather
// than being a second home for one.
//
// Fails loudly (t.Fatalf) if zero directories directly import
// internal/ecosystem, before moduleRoot is added -- a broken walker must
// not pass vacuously by falling back to the one guaranteed entry.
func deriveEcosystemImporters(t *testing.T, moduleRoot, moduleName string) []string {
	t.Helper()
	ecosystemImport := moduleName + "/internal/ecosystem"

	var importers []string
	for _, dir := range packageDirs(t, moduleRoot) {
		if slices.Contains(nonTestImports(t, dir), ecosystemImport) {
			importers = append(importers, dir)
		}
	}

	if len(importers) == 0 {
		t.Fatalf("deriveEcosystemImporters(%s, %s): zero directories directly import %s", moduleRoot, moduleName, ecosystemImport)
	}

	if !slices.Contains(importers, moduleRoot) {
		importers = append(importers, moduleRoot)
	}
	slices.Sort(importers)
	return importers
}

// buildSyntheticEcosystemModule writes a throwaway module tree under
// t.TempDir(): a go.mod, a stub internal/ecosystem package (empty --
// deriveEcosystemImporters only cares that its import path is named, not
// its contents), a package that imports the stub and holds plantedReader as
// a bare literal, a sibling package that imports nothing, and a root-level
// file holding plantedRoot as a bare literal without importing the stub.
// One tree serves both the "new importer picked up automatically" and the
// "main package covered unconditionally" fixture tests below, since
// neither test's assertions interfere with the other's fixture files.
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

// TestDeriveEcosystemImporters_NewImporterPickedUpAutomatically proves a
// new importer needs no edit here: newreader is never named anywhere but
// the fixture-building call below, yet it must turn up in the derived
// list purely because it imports the stub ecosystem package -- and its
// planted literal must then be reported by scanning it. sibling, which
// imports nothing, proves the derivation doesn't just list every directory
// in the tree.
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

// TestDeriveEcosystemImporters_MainPackageCoveredUnconditionally proves
// main is included even without importing the ecosystem package:
// mainpkg.go plants a bare literal at the fixture module's root without
// importing the stub ecosystem package at all, and the derivation
// must still include the root and the scan must still flag it. The second
// assertion checks the real module root joins up the same way: the actual
// launcher main package does import internal/ecosystem today, but this
// assertion only relies on the unconditional inclusion, not on that import
// staying true.
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

// TestEcosystemNamesStayInTheTable enforces that once a fact about an
// ecosystem lives in one ecosystem.Table row, every package that imports
// this one reaches that fact through the row -- never by re-naming the
// ecosystem as a bare string literal of its own. The target list comes
// from deriveEcosystemImporters, not a hand-listed set: a package that
// starts importing internal/ecosystem is covered from its next test run
// with no edit here, and one that stops importing it drops off the same
// way.
//
// internal/registryproxy is not scanned: its own importgraph_test.go
// tripwire (ADR 0047) forbids its shipped code from importing
// internal/ecosystem, so it never reads a row.
//
// internal/credresolver is out of scope because its per-ecosystem literals
// name credential *store formats* (npmrc, cargo credentials,
// gradle.properties, netrc), which needs no import of internal/ecosystem
// to write. This package's own row files (cargo.go, npm.go, ...) are out
// of scope because deriveEcosystemImporters reports only importERS of
// internal/ecosystem, never the package itself.
//
// This test rides the launcher-go-test check defined in nix/checks/go.nix,
// which nix/checks/default.nix already puts in sourceChecks and which is
// absent from imageOnlyCheckNames, so it reaches both `nix flake check`
// and `checks-inbox` with no new nix wiring.
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
