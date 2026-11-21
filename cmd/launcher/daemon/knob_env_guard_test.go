package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/inputdoc"
)

// knobEnvGuardAllowlist maps a FuncDecl name to the selector (see
// selectorLabel) it may call with a non-literal key. warnStrippedChildEnv
// reads no knob, only replays the document's keys. childEnvValue's keys come
// from the registered Strategies, and clearKnobEnvT records the keys they
// read. backendRowSetting's key is the one a backend row names, with no
// registered set to record. Only the plain func's direct body is exempt, not
// a same-named method or a nested closure.
var knobEnvGuardAllowlist = map[string]string{
	"warnStrippedChildEnv": "os.Getenv",
	"childEnvValue":        "os.Getenv",
	"backendRowSetting":    ".Lookup",
}

// knobEnvGuardMethods are the inputdoc.Document knob readers matched on any
// receiver, alongside os.Getenv/os.LookupEnv. The guard is blind to
// os.Environ() scans (main.go feeds one into hostRunnerConfig.env),
// os.ExpandEnv, syscall.Getenv, and bare Getenv/LookupEnv calls through a
// dot-imported os.
var knobEnvGuardMethods = map[string]bool{
	"Resolve":         true,
	"ResolveOptional": true,
	"Lookup":          true,
}

// osImportNames returns the local identifiers bound to the "os" import in
// file, so an aliased import cannot hide a Getenv/LookupEnv read.
func osImportNames(file *ast.File) map[string]bool {
	names := make(map[string]bool)
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != "os" {
			continue
		}
		switch {
		case imp.Name == nil:
			names["os"] = true
		case imp.Name.Name != "_" && imp.Name.Name != ".":
			names[imp.Name.Name] = true
		}
	}
	return names
}

// selectorLabel returns sel's normalised knob-reader form, or "" if it is not
// a knob read. Methods match on name alone: without go/types the receiver
// cannot be confirmed as an inputdoc.Document. Callers skip type positions
// (markTypeSelectors), so a type named pkg.Lookup is not a read, but type
// spellings the parser puts in expression position stay flagged: a
// conversion pkg.Lookup(fn), a generic argument f[pkg.Lookup](x), a new or
// make argument.
func selectorLabel(sel *ast.SelectorExpr, osNames map[string]bool) string {
	if knobEnvGuardMethods[sel.Sel.Name] {
		return "." + sel.Sel.Name
	}
	if sel.Sel.Name == "Getenv" || sel.Sel.Name == "LookupEnv" {
		if x, ok := sel.X.(*ast.Ident); ok && osNames[x.Name] {
			return "os." + sel.Sel.Name
		}
	}
	return ""
}

// markTypeSelectors records in set every selector that n names as a type:
// a Field, ValueSpec, TypeSpec, CompositeLit or TypeAssertExpr type. The
// whole type subtree is marked: a type holds no evaluated expression (an
// array length must be constant), so no knob read can hide there.
func markTypeSelectors(n ast.Node, set map[*ast.SelectorExpr]bool) {
	var t ast.Expr
	switch n := n.(type) {
	case *ast.Field:
		t = n.Type
	case *ast.ValueSpec:
		t = n.Type
	case *ast.TypeSpec:
		t = n.Type
	case *ast.CompositeLit:
		t = n.Type
	case *ast.TypeAssertExpr:
		t = n.Type
	}
	if t == nil {
		return
	}
	ast.Inspect(t, func(m ast.Node) bool {
		if sel, ok := m.(*ast.SelectorExpr); ok {
			set[sel] = true
		}
		return true
	})
}

// scanKnobEnvSites maps each literal knob key read in dir's non-test files
// to its "file:line" sites, and returns a violation for each knob reader with
// a non-literal key outside the allow-list or not called directly (a method
// value), which the guard cannot trace to a key.
func scanKnobEnvSites(dir string) (sites map[string][]string, violations []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("ReadDir(%s): %w", dir, err)
	}
	sites = make(map[string][]string)
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil, nil, fmt.Errorf("ParseFile(%s): %w", path, perr)
		}
		osNames := osImportNames(file)

		for _, decl := range file.Decls {
			var enclosingFunc string
			var hasRecv bool
			if fd, ok := decl.(*ast.FuncDecl); ok {
				enclosingFunc = fd.Name.Name
				hasRecv = fd.Recv != nil
			}

			// Keyed by node identity: selectors that are a call's direct Fun
			// (vs a method value), and selectors inside a nested FuncLit,
			// which the allow-list does not exempt.
			callFun := make(map[*ast.SelectorExpr]*ast.CallExpr)
			inFuncLit := make(map[*ast.SelectorExpr]bool)
			typePos := make(map[*ast.SelectorExpr]bool)
			ast.Inspect(decl, func(n ast.Node) bool {
				markTypeSelectors(n, typePos)
				if call, ok := n.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
						callFun[sel] = call
					}
				}
				if lit, ok := n.(*ast.FuncLit); ok {
					ast.Inspect(lit.Body, func(m ast.Node) bool {
						if sel, ok := m.(*ast.SelectorExpr); ok {
							inFuncLit[sel] = true
						}
						return true
					})
				}
				return true
			})

			ast.Inspect(decl, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || typePos[sel] {
					return true
				}
				label := selectorLabel(sel, osNames)
				if label == "" {
					return true
				}
				pos := fset.Position(sel.Pos())
				loc := fmt.Sprintf("%s:%d", name, pos.Line)

				call, isCall := callFun[sel]
				if !isCall {
					violations = append(violations, fmt.Sprintf(
						"%s: %s used without being called directly (e.g. bound to a variable as a method value) — this guard cannot trace a key read at the call site",
						loc, label))
					return true
				}
				if len(call.Args) == 0 {
					violations = append(violations, fmt.Sprintf(
						"%s: %s called with no arguments — this guard cannot trace a key read at the call site",
						loc, label))
					return true
				}

				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					if !hasRecv && !inFuncLit[sel] && knobEnvGuardAllowlist[enclosingFunc] == label {
						return true
					}
					if enclosingFunc == "" {
						violations = append(violations, fmt.Sprintf(
							"%s: %s called with a non-literal key in a package-level initializer; make the key a literal, or move the read into a func",
							loc, label))
					} else {
						violations = append(violations, fmt.Sprintf(
							"%s: %s called with a non-literal key outside the allow-list; add %q to knobEnvGuardAllowlist with a reason, or make the key a literal",
							loc, label, enclosingFunc))
					}
					return true
				}

				key, uerr := strconv.Unquote(lit.Value)
				if uerr != nil {
					err = fmt.Errorf("%s: Unquote(%s): %w", loc, lit.Value, uerr)
					return false
				}
				sites[key] = append(sites[key], loc)
				return true
			})
			if err != nil {
				return nil, nil, err
			}
		}
	}
	return sites, violations, nil
}

// TestClearKnobEnvT_CoversEveryKnob guards clearKnobEnvT's hand-kept
// daemonKnobEnvVars against drift from the call sites it exists to shadow.
func TestClearKnobEnvT_CoversEveryKnob(t *testing.T) {
	sites, violations, err := scanKnobEnvSites(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range violations {
		t.Error(v)
	}
	if len(sites) == 0 {
		t.Fatal("scan found no knob-shaped call sites — a rename likely broke the walk")
	}

	known := make(map[string]bool, len(daemonKnobEnvVars))
	for _, k := range daemonKnobEnvVars {
		known[k] = true
	}

	var missing []string
	for key, locs := range sites {
		if !known[key] {
			missing = append(missing, fmt.Sprintf("%s (%s)", key, strings.Join(locs, ", ")))
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("daemonKnobEnvVars is missing a key read at %s", m)
	}

	var stale []string
	for _, k := range daemonKnobEnvVars {
		if _, ok := sites[k]; !ok {
			stale = append(stale, k)
		}
	}
	sort.Strings(stale)
	for _, s := range stale {
		t.Errorf("daemonKnobEnvVars has %q, but no call site in this package reads it", s)
	}
}

// TestDaemonOnlyKnobs_MatchesDocLookupSites pins the schema's launcherIgnores
// set (inputdoc.IsDaemonOnly) to the keys the daemon still reads through
// inputdoc.Document's ambient-wins Lookup path: a knob added via doc.Resolve
// without being in that set, or moved to childKnob but left in it, would make
// warnStrippedChildEnv's wording lie.
func TestDaemonOnlyKnobs_MatchesDocLookupSites(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]bool)
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !knobEnvGuardMethods[sel.Sel.Name] {
				return true
			}
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if key, err := strconv.Unquote(lit.Value); err == nil {
					got[key] = true
				}
			}
			return true
		})
	}
	if len(got) == 0 {
		t.Fatal("scan found no doc.Resolve/ResolveOptional/Lookup sites — a rename likely broke the walk")
	}
	for k := range got {
		if !inputdoc.IsDaemonOnly(k) {
			t.Errorf("%s is read via doc.Resolve/ResolveOptional/Lookup but not in the daemon-only set (inputdoc.IsDaemonOnly)", k)
		}
	}
	for _, k := range inputdoc.DaemonOnlyKnobs() {
		if !got[k] {
			t.Errorf("the daemon-only set (inputdoc.IsDaemonOnly) lists %s, but no doc.Resolve/ResolveOptional/Lookup site reads it", k)
		}
	}
}

// knobFixtureSource is a source file that never needs to compile, only
// parse (scanKnobEnvSites runs parser.ParseFile with mode 0, no type
// checking), covering every shape TestScanKnobEnvSites_HasTeeth pins.
const knobFixtureSource = `package fixture

import (
	"os"
	stdos "os"
)

func readsA(doc *Document, w Writer) {
	doc.Resolve("A", w)
}

func readsB(h *Holder, w Writer) {
	h.d.ResolveOptional("B", w)
}

func readsC() {
	os.Getenv("C")
}

func readsD() {
	stdos.LookupEnv("D")
}

type lookupHolder struct {
	f backendkit.Lookup
}

func typePositions(doc *Document, fn backendkit.Lookup, fns []backendkit.Lookup) backendkit.Lookup {
	var v backendkit.Lookup = fn
	_ = backendkit.Lookup{} // parse-only fixture: a CompositeLit type, though not valid Go for a func type
	_, _ = any(v).(backendkit.Lookup)
	doc.Lookup("E")
	return v
}

func methodValueLookup(doc *Document) {
	l := doc.Lookup
	_ = l
}

func nonLiteral(doc *Document, w Writer, key string) {
	doc.Resolve(key, w)
}

func methodValueDoc(doc *Document) {
	r := doc.Resolve
	_ = r
}

func methodValueOs() {
	f := os.Getenv
	_ = f
}

func zeroArgs() {
	os.Getenv()
}

type mutT struct{}

func (mutT) warnStrippedChildEnv(key string) string {
	return os.Getenv(key)
}

func warnStrippedChildEnv(key string) {
	os.Getenv(key)

	k := key
	inner := func() string {
		return os.Getenv(k)
	}
	_ = inner()
}
`

// knobFixtureTestSource lives in a _test.go file the scan must ignore
// entirely: its IGNORED key must never show up in sites or violations.
const knobFixtureTestSource = `package fixture

import "os"

func ignored() {
	os.Getenv("IGNORED")
}
`

// TestScanKnobEnvSites_HasTeeth proves scanKnobEnvSites can both find and
// flag, so TestClearKnobEnvT_CoversEveryKnob above cannot pass vacuously.
func TestScanKnobEnvSites_HasTeeth(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(knobFixtureSource), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fixture_test.go"), []byte(knobFixtureTestSource), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	sites, violations, err := scanKnobEnvSites(dir)
	if err != nil {
		t.Fatalf("scanKnobEnvSites: %v", err)
	}

	for _, key := range []string{"A", "B", "C", "D", "E"} {
		if len(sites[key]) == 0 {
			t.Errorf("sites is missing literal key %q", key)
		}
	}
	if _, ok := sites["IGNORED"]; ok {
		t.Error("scan should skip _test.go files, but found the IGNORED key from fixture_test.go")
	}

	// Pinned by line, not name: two fixture shapes are both named
	// warnStrippedChildEnv.
	lineOf := func(marker string) int {
		idx := strings.Index(knobFixtureSource, marker)
		if idx < 0 {
			t.Fatalf("marker %q not found in knobFixtureSource", marker)
		}
		return strings.Count(knobFixtureSource[:idx], "\n") + 1
	}
	hasLoc := func(line int) bool {
		want := fmt.Sprintf("fixture.go:%d: ", line)
		for _, v := range violations {
			if strings.HasPrefix(v, want) {
				return true
			}
		}
		return false
	}

	wantViolations := []struct {
		name   string
		marker string
	}{
		{"non-literal key outside the allow-list", "doc.Resolve(key, w)"},
		{"method value doc.Resolve, never called directly", "r := doc.Resolve"},
		{"method value doc.Lookup, never called directly", "l := doc.Lookup"},
		{"method value os.Getenv, never called directly", "f := os.Getenv"},
		{"zero-argument os.Getenv call", "\tos.Getenv()\n"},
		{"warnStrippedChildEnv on mutT has a receiver, so it does not qualify for the allow-list", "return os.Getenv(key)"},
		{"non-literal key inside a func literal nested in allow-listed warnStrippedChildEnv", "return os.Getenv(k)\n"},
	}
	for _, tc := range wantViolations {
		if line := lineOf(tc.marker); !hasLoc(line) {
			t.Errorf("%s: no violation at fixture.go:%d; got:\n%s", tc.name, line, strings.Join(violations, "\n"))
		}
	}

	if exempt := lineOf("\tos.Getenv(key)\n\n\tk := key\n"); hasLoc(exempt) {
		t.Errorf("allow-listed warnStrippedChildEnv direct body should not be flagged; fixture.go:%d got:\n%s", exempt, strings.Join(violations, "\n"))
	}

	if len(violations) != len(wantViolations) {
		t.Errorf("got %d violations, want %d (one per fixture shape above): %v", len(violations), len(wantViolations), violations)
	}
}
