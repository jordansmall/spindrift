package ecosystem

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
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

// scanForEcosystemLiterals parses target -- either a single .go file or a
// directory, which it walks recursively -- and returns one finding per
// string literal whose *entire* unquoted value equals a Table row's Name,
// case-insensitive.
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
// A directory is walked to its leaves so a subpackage added later under a
// scoped package is covered without an edit here. Two kinds of directory are
// pruned: testdata, which by Go convention may hold deliberately unparseable
// .go files, and dot-prefixed directories, which hold tooling state rather
// than package source.
//
// If target does not exist, this fails the test loudly (t.Fatalf) rather
// than silently scanning zero files: a target renamed out from under the
// check must not quietly drop coverage.
func scanForEcosystemLiterals(t *testing.T, target string) []ecosystemLiteral {
	t.Helper()

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("scanForEcosystemLiterals: target %s does not exist: %v", target, err)
	}

	var files []string
	if info.IsDir() {
		err := filepath.WalkDir(target, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := d.Name()
			if d.IsDir() {
				if path != target && (name == "testdata" || strings.HasPrefix(name, ".")) {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				return nil
			}
			files = append(files, path)
			return nil
		})
		if err != nil {
			t.Fatalf("WalkDir(%s): %v", target, err)
		}
	} else {
		files = append(files, target)
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
// that a deliberate violation trips the check. The fixture tree holds a
// production file that routes on a bare ecosystem literal, a production file
// that only holds a *containing* literal and a comment naming an ecosystem,
// a _test.go file that routes on a bare literal, a subpackage that routes on
// a bare literal, and a testdata directory holding an unparseable .go file.
// Exactly the top-level violator and the subpackage violator must be
// reported: the compliant file proves whole-literal equality doesn't
// over-fire on containment or comments, the _test.go file proves test files
// are out of scope, the subpackage proves the scan recurses, and testdata
// proves the scan does not try to parse it.
//
// Every ecosystem name in the fixture is drawn from Table, so deleting a row
// can never make this test fail for a reason unrelated to the scanner.
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
	if len(findings) != 2 {
		t.Fatalf("scanForEcosystemLiterals(%s) = %+v, want exactly 2 findings", dir, findings)
	}

	byFile := make(map[string]ecosystemLiteral, len(findings))
	for _, f := range findings {
		byFile[f.File] = f
	}

	want := []ecosystemLiteral{
		{File: filepath.Join(dir, "violating.go"), Line: lineOf(t, violatingSrc, violatingName), Value: violatingName},
		{File: filepath.Join(subDir, "nested.go"), Line: lineOf(t, nestedSrc, subpkgName), Value: subpkgName},
	}
	for _, w := range want {
		got, ok := byFile[w.File]
		if !ok {
			t.Errorf("no finding for %s; got %+v", w.File, findings)
			continue
		}
		if got != w {
			t.Errorf("finding for %s = %+v, want %+v", w.File, got, w)
		}
	}
}

// TestEcosystemNamesStayInTheTable enforces that once a fact about an
// ecosystem lives in one ecosystem.Table row, the registry proxy, the
// binding package, and the bind-registry verb reach it only through that
// row -- never by re-naming the ecosystem as a bare string literal. Names
// come from Table itself (see scanForEcosystemLiterals), so adding a row
// needs no edit here.
//
// The scope is deliberately narrow to these three routing surfaces and
// excludes two packages that legitimately name ecosystems for reasons the
// spec (#3137 / ADR 0045) does not want folded into the table:
// internal/credresolver, whose per-ecosystem literals name credential
// *store formats* (npmrc, cargo credentials, gradle.properties, netrc) --
// the ecosystem name is part of the file format, not a routing decision --
// and this package's own row files, whose per-row ConfigParser is a
// per-ecosystem lockfile *parser*. Covering either would be pure noise or
// would push format/parser knowledge into Table, which the spec rejects.
//
// bind-registry is scoped to its single file, not the driver-exec package,
// because driver-exec holds many unrelated verbs and only this one routes
// on ecosystem names.
//
// This test rides the launcher-go-test check defined in nix/checks/go.nix,
// which nix/checks/default.nix already puts in sourceChecks and which is
// absent from imageOnlyCheckNames, so it reaches both `nix flake check`
// and `checks-inbox` with no new nix wiring.
func TestEcosystemNamesStayInTheTable(t *testing.T) {
	targets := []string{
		"../registryproxy",
		"../bindregistry",
		"../../driver-exec/bindregistry_cmd.go",
	}

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
