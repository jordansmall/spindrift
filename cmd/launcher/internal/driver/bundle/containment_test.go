package bundle_test

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

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/testutil"
)

type ecosystemLiteral struct {
	File  string
	Line  int
	Value string
}

// scanForEcosystemLiterals returns one finding per string literal in dir's
// .go files, test files included, whose entire unquoted value is in forbidden
// (lower-cased Strategy names), except in skipFiles (base names).
// Equality is whole-literal, so the scan skips "cargo config.json" and prose,
// and walking the AST skips comments. It reads dir's own files only;
// deriveEcosystemImporters lists every package directory separately.
func scanForEcosystemLiterals(t *testing.T, dir string, forbidden map[string]bool, skipFiles ...string) []ecosystemLiteral {
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
		if e.IsDir() || !strings.HasSuffix(name, ".go") || slices.Contains(skipFiles, name) {
			continue
		}
		files = append(files, filepath.Join(dir, name))
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
			if !forbidden[strings.ToLower(value)] {
				return true
			}
			pos := fset.Position(lit.Pos())
			findings = append(findings, ecosystemLiteral{File: pos.Filename, Line: pos.Line, Value: value})
			return true
		})
	}
	return findings
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
// top-level violator and the violating _test.go file are reported. The
// compliant file proves whole-literal equality does not over-fire on
// containment or comments, and the subpackage and the unparseable testdata
// file prove the scan never descends into either.
func TestScanForEcosystemLiterals_FixtureDetectsViolation(t *testing.T) {
	// The fixture draws its names from the registered Strategies, so deleting a
	// Strategy cannot fail this test for a reason unrelated to the scanner.
	names := registeredNames(t, 4)
	violatingName, containedName, testFileName, subpkgName := names[0], names[1], names[2], names[3]

	dir := t.TempDir()

	violatingSrc := fmt.Sprintf(`package fixture

func classify(name string) bool {
	if name == %q {
		return true
	}
	return false
}
`, violatingName)
	testutil.WriteFixture(t, dir, "violating.go", violatingSrc)

	testutil.WriteFixture(t, dir, "compliant.go", fmt.Sprintf(`package fixture

// configFile names the %s config file this package writes; it is not
// itself a routing decision on the %s ecosystem name.
func configFile() string {
	return %q
}
`, containedName, containedName, containedName+" config.json"))

	testSrc := fmt.Sprintf(`package fixture

import "testing"

func TestBareLiteralInTestFile(t *testing.T) {
	name := %q
	_ = name
}
`, testFileName)
	testutil.WriteFixture(t, dir, "fixture_test.go", testSrc)

	subDir := filepath.Join(dir, "subpkg")
	nestedSrc := fmt.Sprintf(`package subpkg

var routed = %q
`, subpkgName)
	testutil.WriteFixture(t, subDir, "nested.go", nestedSrc)

	testutil.WriteFixture(t, filepath.Join(dir, "testdata"), "broken.go", "this is not go source {{")

	findings := scanForEcosystemLiterals(t, dir, nameSet(names))
	want := []ecosystemLiteral{
		{File: filepath.Join(dir, "fixture_test.go"), Line: lineOf(t, testSrc, testFileName), Value: testFileName},
		{File: filepath.Join(dir, "violating.go"), Line: lineOf(t, violatingSrc, violatingName), Value: violatingName},
	}
	// Order-sensitive: os.ReadDir returns entries sorted by filename, so
	// fixture_test.go precedes violating.go.
	if !slices.Equal(findings, want) {
		t.Fatalf("scanForEcosystemLiterals(%s) = %+v, want exactly %+v", dir, findings, want)
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

// deriveEcosystemImporters returns the directories under moduleRoot that can
// handle an ecosystem name: direct importers of moduleName+"/internal/ecosystem"
// or moduleName+"/internal/registryvocab", plus registryvocab itself (the leaf
// declaring the string fields names travel in: Subtree.Ecosystem,
// RouteEcosystems),
// moduleRoot (so routing main's ecosystem access through an intermediary
// cannot uncover it) and internal/credresolver (which must never import this
// package yet must not re-name an ecosystem either).
//
// Direct imports only: a package that merely passes a registrymanifest along
// opaquely, as the runners do, never sees a name. The ecosystem tree (the core
// package and the ecosystem packages beneath it) is skipped by path, since it
// is where the names live; ecosystemTreeFindings scans it against each
// package's own names. Testdata corpora are pruned by packageDirs.
func deriveEcosystemImporters(t *testing.T, moduleRoot, moduleName string) []string {
	t.Helper()
	ecosystemImport := moduleName + "/internal/ecosystem"
	vocabImport := moduleName + "/internal/registryvocab"

	var importers []string
	ecosystemDir := filepath.Join(moduleRoot, "internal", "ecosystem")
	for _, dir := range packageDirs(t, moduleRoot) {
		if dir == ecosystemDir || strings.HasPrefix(dir, ecosystemDir+string(filepath.Separator)) {
			continue
		}
		imports := nonTestImports(t, dir)
		if slices.Contains(imports, ecosystemImport) || slices.Contains(imports, vocabImport) {
			importers = append(importers, dir)
		}
	}

	// Checked before the unconditional targets are added: a broken walker must
	// not pass vacuously by falling back to the guaranteed entries.
	if len(importers) == 0 {
		t.Fatalf("deriveEcosystemImporters(%s, %s): zero directories directly import %s or %s", moduleRoot, moduleName, ecosystemImport, vocabImport)
	}

	unconditional := []string{
		moduleRoot,
		filepath.Join(moduleRoot, "internal", "registryvocab"),
		filepath.Join(moduleRoot, "internal", "credresolver"),
	}
	for _, dir := range unconditional {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("unconditional scan target %s: %v", dir, err)
		}
		if !slices.Contains(importers, dir) {
			importers = append(importers, dir)
		}
	}
	slices.Sort(importers)
	return importers
}

// buildSyntheticEcosystemModule writes a throwaway module tree. Under
// internal/, with the planted literals as bare string values:
//   - ecosystem: stub importing the stub registryvocab and holding
//     plantedReader, so only the by-path skip keeps it out of the targets;
//   - registryvocab: stub leaf holding plantedReader;
//   - newreader: imports the stub ecosystem, holds plantedReader;
//   - vocabreader: imports only the stub registryvocab, holds plantedReader;
//   - sibling: imports nothing;
//   - credresolver: imports nothing, holds plantedCred.
//
// The module root holds plantedRoot and imports nothing. One tree serves the
// fixture tests below, whose assertions do not interfere.
func buildSyntheticEcosystemModule(t *testing.T, plantedReader, plantedRoot, plantedCred string) (root, moduleName string) {
	t.Helper()
	root = t.TempDir()
	moduleName = "example.com/fixturemodule"

	testutil.WriteFixture(t, root, "go.mod", fmt.Sprintf("module %s\n\ngo 1.24\n", moduleName))
	testutil.WriteFixture(t, root, "mainpkg.go", fmt.Sprintf("package mainpkg\n\nvar routed = %q\n", plantedRoot))
	testutil.WriteFixture(t, filepath.Join(root, "internal", "ecosystem"), "ecosystem.go", fmt.Sprintf(
		"package ecosystem\n\nimport _ %q\n\nvar routed = %q\n", moduleName+"/internal/registryvocab", plantedReader))
	testutil.WriteFixture(t, filepath.Join(root, "internal", "registryvocab"), "registryvocab.go", fmt.Sprintf(
		"package registryvocab\n\nvar routed = %q\n", plantedReader))
	testutil.WriteFixture(t, filepath.Join(root, "internal", "newreader"), "newreader.go", fmt.Sprintf(
		"package newreader\n\nimport _ %q\n\nvar routed = %q\n", moduleName+"/internal/ecosystem", plantedReader))
	testutil.WriteFixture(t, filepath.Join(root, "internal", "vocabreader"), "vocabreader.go", fmt.Sprintf(
		"package vocabreader\n\nimport _ %q\n\nvar routed = %q\n", moduleName+"/internal/registryvocab", plantedReader))
	testutil.WriteFixture(t, filepath.Join(root, "internal", "sibling"), "sibling.go", "package sibling\n\nvar notRouted = \"unrelated\"\n")
	testutil.WriteFixture(t, filepath.Join(root, "internal", "credresolver"), "credresolver.go", fmt.Sprintf(
		"package credresolver\n\nvar routed = %q\n", plantedCred))

	return root, moduleName
}

// TestDeriveEcosystemImporters_NewImporterPickedUpAutomatically proves a new
// importer needs no edit here: each of newreader (imports the stub ecosystem)
// and vocabreader (imports only the stub registryvocab, never ecosystem) turns
// up in the derived list, and scanning it reports its single planted literal.
//
// Two exclusions are asserted so the derivation cannot pass by over-selecting.
// sibling imports nothing, so listing every directory would include it. The
// stub ecosystem imports registryvocab and holds a planted literal, so it
// would be selected as a vocab importer were it not skipped by path; the
// skip is what keeps the ecosystem packages out of the importer scan, where an
// owning package's own names would be findings rather than allowed.
func TestDeriveEcosystemImporters_NewImporterPickedUpAutomatically(t *testing.T) {
	names := registeredNames(t, 1)
	plantedName := names[0]
	root, moduleName := buildSyntheticEcosystemModule(t, plantedName, plantedName, plantedName)

	derived := deriveEcosystemImporters(t, root, moduleName)

	for _, pkg := range []string{"newreader", "vocabreader"} {
		t.Run(pkg, func(t *testing.T) {
			dir := filepath.Join(root, "internal", pkg)
			if !slices.Contains(derived, dir) {
				t.Fatalf("deriveEcosystemImporters(%s) = %v, want it to include %s", root, derived, dir)
			}
			findings := scanForEcosystemLiterals(t, dir, nameSet(names))
			if len(findings) != 1 || findings[0].Value != plantedName {
				t.Fatalf("scanForEcosystemLiterals(%s) = %+v, want a single finding of %q", dir, findings, plantedName)
			}
		})
	}

	for _, excluded := range []struct{ pkg, why string }{
		{"sibling", "imports nothing"},
		{"ecosystem", "skipped by path though it imports registryvocab"},
	} {
		t.Run("excludes "+excluded.pkg, func(t *testing.T) {
			dir := filepath.Join(root, "internal", excluded.pkg)
			if slices.Contains(derived, dir) {
				t.Fatalf("deriveEcosystemImporters(%s) = %v, want it to exclude %s (%s)", root, derived, dir, excluded.why)
			}
		})
	}
}

// TestDeriveEcosystemImporters_MainPackageCoveredUnconditionally proves main
// is included even without importing the ecosystem package. The real launcher
// main package does import internal/ecosystem today, but the second assertion
// relies only on the unconditional inclusion, not on that import staying true.
func TestDeriveEcosystemImporters_MainPackageCoveredUnconditionally(t *testing.T) {
	names := registeredNames(t, 1)
	plantedName := names[0]
	root, moduleName := buildSyntheticEcosystemModule(t, plantedName, plantedName, plantedName)

	derived := deriveEcosystemImporters(t, root, moduleName)
	if !slices.Contains(derived, root) {
		t.Fatalf("deriveEcosystemImporters(%s) = %v, want it to include the module root %s", root, derived, root)
	}

	findings := scanForEcosystemLiterals(t, root, nameSet(names))
	if len(findings) != 1 || findings[0].Value != plantedName {
		t.Fatalf("scanForEcosystemLiterals(%s) = %+v, want a single finding of %q", root, findings, plantedName)
	}

	realModuleName := readModuleName(t, filepath.Join(realModuleRoot, "go.mod"))
	realDerived := deriveEcosystemImporters(t, realModuleRoot, realModuleName)
	if !slices.Contains(realDerived, realModuleRoot) {
		t.Fatalf("deriveEcosystemImporters(%s) = %v, want it to include the real launcher main package %s", realModuleRoot, realDerived, realModuleRoot)
	}
}

// TestDeriveEcosystemImporters_InternalLeavesCoveredUnconditionally proves
// internal/credresolver and internal/registryvocab are scan targets although
// neither imports the ecosystem package (credresolver must stay below the
// registry layer; registryvocab sits below ecosystem itself), and that a
// planted ecosystem name in either is found.
func TestDeriveEcosystemImporters_InternalLeavesCoveredUnconditionally(t *testing.T) {
	names := registeredNames(t, 1)
	plantedName := names[0]
	root, moduleName := buildSyntheticEcosystemModule(t, plantedName, plantedName, plantedName)
	derived := deriveEcosystemImporters(t, root, moduleName)

	realModuleName := readModuleName(t, filepath.Join(realModuleRoot, "go.mod"))
	realDerived := deriveEcosystemImporters(t, realModuleRoot, realModuleName)

	for _, pkg := range []string{"credresolver", "registryvocab"} {
		t.Run(pkg, func(t *testing.T) {
			dir := filepath.Join(root, "internal", pkg)
			if !slices.Contains(derived, dir) {
				t.Fatalf("deriveEcosystemImporters(%s) = %v, want it to include %s", root, derived, dir)
			}

			findings := scanForEcosystemLiterals(t, dir, nameSet(names))
			if len(findings) != 1 || findings[0].Value != plantedName {
				t.Fatalf("scanForEcosystemLiterals(%s) = %+v, want a single finding of %q", dir, findings, plantedName)
			}

			realDir := filepath.Join(realModuleRoot, "internal", pkg)
			if !slices.Contains(realDerived, realDir) {
				t.Fatalf("deriveEcosystemImporters(%s) = %v, want it to include %s", realModuleRoot, realDerived, realDir)
			}
		})
	}
}

func nameSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[strings.ToLower(n)] = true
	}
	return set
}

// registeredNames returns the registered Strategies' names in registration
// order, failing if there are fewer than atLeast so a fixture cannot run short.
func registeredNames(t *testing.T, atLeast int) []string {
	t.Helper()
	var names []string
	for _, s := range ecosystem.Registered() {
		names = append(names, s.Name)
	}
	if len(names) < atLeast {
		t.Fatalf("fixture needs %d ecosystem names, the Bundle registered %d", atLeast, len(names))
	}
	return names
}

// ecosystemOwners maps each registered Strategy name, lower-cased, to the
// directory of the package declaring it: the core ecosystem directory or one
// ecosystem package beneath it. names and the Bundle's Register arguments are
// both in registration order, which is how a name finds its declaring package.
func ecosystemOwners(t *testing.T, moduleRoot, moduleName, registerFile string, names []string) map[string]string {
	t.Helper()
	ecosystemImport := moduleName + "/internal/ecosystem"
	args := registerArgs(t, registerFile, ecosystemImport)
	if len(args) != len(names) {
		t.Fatalf("%s registers %d Strategies, the Bundle holds %d", registerFile, len(args), len(names))
	}
	owners := make(map[string]string, len(names))
	for i, a := range args {
		if a.ImportPath != ecosystemImport && !strings.HasPrefix(a.ImportPath, ecosystemImport+"/") {
			t.Fatalf("%s: Strategy %s is declared outside internal/ecosystem", registerFile, a)
		}
		owners[strings.ToLower(names[i])] = filepath.Join(moduleRoot, filepath.FromSlash(strings.TrimPrefix(a.ImportPath, moduleName+"/")))
	}
	return owners
}

// containmentFindings returns every ecosystem-name literal outside the places
// allowed to hold it. A name lives in the package that declares its Strategy
// and in registerFile (the Bundle's ecosystems file, which wires every one);
// anywhere else, another ecosystem's package and the core package included, it
// is a finding. The ecosystem tree is scanned in full, so a package added
// beneath it is covered with no edit here.
func containmentFindings(t *testing.T, moduleRoot, moduleName, registerFile string, names []string) []ecosystemLiteral {
	t.Helper()
	owners := ecosystemOwners(t, moduleRoot, moduleName, registerFile, names)
	all := make(map[string]bool, len(owners))
	for n := range owners {
		all[n] = true
	}
	registerDir, registerBase := filepath.Dir(registerFile), filepath.Base(registerFile)

	var findings []ecosystemLiteral
	for _, dir := range deriveEcosystemImporters(t, moduleRoot, moduleName) {
		var skip []string
		if dir == registerDir {
			skip = append(skip, registerBase)
		}
		findings = append(findings, scanForEcosystemLiterals(t, dir, all, skip...)...)
	}
	for _, dir := range packageDirs(t, filepath.Join(moduleRoot, "internal", "ecosystem")) {
		forbidden := make(map[string]bool, len(all))
		for n := range all {
			if owners[n] != dir {
				forbidden[n] = true
			}
		}
		findings = append(findings, scanForEcosystemLiterals(t, dir, forbidden)...)
	}
	return findings
}

// TestEcosystemNamesStayInTheirOwnPackage enforces that an ecosystem's name
// lives in its own Strategy's package: every direct importer of the core
// package or of the registryvocab leaf (whose string fields names travel in),
// and registryvocab itself, reaches an ecosystem fact through its Strategy and
// never re-names the ecosystem as a bare literal, and within internal/ecosystem
// each package holds only its own Strategies' names. Only the Bundle's
// ecosystems file may name them all. Targets come from
// deriveEcosystemImporters, so the set tracks imports with no edit here.
// internal/registryproxy, which ADR 0047 forbids importing the core package, is
// covered through its registryvocab import.
//
// This lives in the Bundle's tests because only here is every Strategy
// registered: core's own tests cannot link an ecosystem package.
func TestEcosystemNamesStayInTheirOwnPackage(t *testing.T) {
	moduleName := readModuleName(t, filepath.Join(realModuleRoot, "go.mod"))
	registerFile := filepath.Join(realModuleRoot, "internal", "driver", "bundle", "ecosystems.go")
	findings := containmentFindings(t, realModuleRoot, moduleName, registerFile, registeredNames(t, 1))

	if len(findings) > 0 {
		lines := make([]string, 0, len(findings))
		for _, f := range findings {
			lines = append(lines, fmt.Sprintf("%s:%d: literal %q names an ecosystem outside its own package", f.File, f.Line, f.Value))
		}
		t.Errorf("found %d ecosystem-name literal(s) outside their owning package:\n%s", len(findings), strings.Join(lines, "\n"))
	}
}

// containmentModule writes a module holding three Strategies, one in the core
// package (corename) and one in each of two ecosystem packages (subname,
// othername), wired by a Bundle ecosystems file that names all three. With
// extra overlaid (path relative to the module root), the findings are
// returned as "<relative file>:<value>".
func containmentModule(t *testing.T, extra map[string]string) []string {
	t.Helper()
	const mod = "example.com/fixturemodule"
	eco := mod + "/internal/ecosystem"
	files := map[string]string{
		"go.mod":                                 "module " + mod + "\n\ngo 1.24\n",
		"internal/ecosystem/ecosystem.go":        "package ecosystem\n\nvar Core = \"corename\"\n",
		"internal/ecosystem/sub/sub.go":          "package sub\n\nimport _ \"" + eco + "\"\n\nvar S = \"subname\"\n",
		"internal/ecosystem/other/other.go":      "package other\n\nimport _ \"" + eco + "\"\n\nvar O = \"othername\"\n",
		"internal/ecosystem/ecosystemkit/kit.go": "package ecosystemkit\n\nvar K = \"unrelated\"\n",
		"internal/registryvocab/vocab.go":        "package registryvocab\n\nvar V = \"unrelated\"\n",
		"internal/credresolver/cred.go":          "package credresolver\n\nvar C = \"unrelated\"\n",
		"internal/reader/reader.go":              "package reader\n\nimport _ \"" + eco + "\"\n\nvar R = \"unrelated\"\n",
		"internal/driver/bundle/bundle.go":       "package bundle\n\nimport _ \"" + eco + "\"\n",
		"internal/driver/bundle/ecosystems.go": "package bundle\n\nimport (\n\t\"" + eco + "\"\n\t\"" + eco + "/sub\"\n\t\"" + eco + "/other\"\n)\n\n" +
			"var wired = []string{\"corename\", \"subname\", \"othername\"}\n\nfunc init() { ecosystem.Register(ecosystem.Core, sub.S, other.O) }\n",
	}
	for p, body := range extra {
		files[p] = body
	}
	root := t.TempDir()
	for p, body := range files {
		testutil.WriteFixture(t, root, p, body)
	}
	found := containmentFindings(t, root, mod, filepath.Join(root, "internal", "driver", "bundle", "ecosystems.go"), []string{"corename", "subname", "othername"})
	var out []string
	for _, f := range found {
		rel, err := filepath.Rel(root, f.File)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, filepath.ToSlash(rel)+":"+f.Value)
	}
	slices.Sort(out)
	return out
}

func TestContainmentFindingsClassifiesPlantedLiterals(t *testing.T) {
	const eco = "example.com/fixturemodule/internal/ecosystem"
	plant := func(pkg, name string) string {
		return "package " + pkg + "\n\nimport _ \"" + eco + "\"\n\nvar Planted = \"" + name + "\"\n"
	}
	for _, tc := range []struct {
		name  string
		extra map[string]string
		want  []string
	}{
		{"each name in its own package and the Bundle's ecosystems file passes", nil, nil},
		{"own name in the owner's test file passes", map[string]string{"internal/ecosystem/sub/sub_test.go": plant("sub_test", "subname")}, nil},
		{"a name in an unrelated importer fails", map[string]string{"internal/reader/planted.go": plant("reader", "subname")},
			[]string{"internal/reader/planted.go:subname"}},
		{"a name in another ecosystem's package fails", map[string]string{"internal/ecosystem/other/planted.go": plant("other", "subname")},
			[]string{"internal/ecosystem/other/planted.go:subname"}},
		{"another ecosystem's name in a package's test file fails", map[string]string{"internal/ecosystem/sub/planted_test.go": plant("sub_test", "othername")},
			[]string{"internal/ecosystem/sub/planted_test.go:othername"}},
		{"a package's name in the core package fails", map[string]string{"internal/ecosystem/planted.go": plant("ecosystem", "subname")},
			[]string{"internal/ecosystem/planted.go:subname"}},
		{"the core package's own name in an ecosystem package fails", map[string]string{"internal/ecosystem/sub/planted.go": plant("sub", "corename")},
			[]string{"internal/ecosystem/sub/planted.go:corename"}},
		{"a name in ecosystemkit fails", map[string]string{"internal/ecosystem/ecosystemkit/planted.go": "package ecosystemkit\n\nvar Planted = \"subname\"\n"},
			[]string{"internal/ecosystem/ecosystemkit/planted.go:subname"}},
		{"a name in another Bundle file fails", map[string]string{"internal/driver/bundle/planted.go": plant("bundle", "subname")},
			[]string{"internal/driver/bundle/planted.go:subname"}},
		{"a name in the module root fails", map[string]string{"main.go": "package main\n\nvar Planted = \"subname\"\n"},
			[]string{"main.go:subname"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := containmentModule(t, tc.extra); !slices.Equal(got, tc.want) {
				t.Fatalf("findings = %v, want %v", got, tc.want)
			}
		})
	}
}
