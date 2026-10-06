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
)

// knobEnvGuardAllowlist maps a FuncDecl name to the normalised selector
// (see selectorLabel) it is allowed a non-literal first arg for. Only
// warnStrippedChildEnv qualifies: it replays the document's own settings
// keys to warn about them, it reads no knob. The exemption covers only the
// plain package-level func's own direct body — not a method of the same
// name (see fd.Recv below), and not a func literal nested inside it,
// which gets no exemption either.
var knobEnvGuardAllowlist = map[string]string{
	"warnStrippedChildEnv": "os.Getenv",
}

// knobEnvGuardMethods lists the inputdoc.Document knob readers this guard
// treats as a knob read on any receiver shape, alongside stdlib
// os.Getenv/os.LookupEnv. This guard is blind to a knob read any other way:
// scanning os.Environ() (main.go feeds it into hostRunnerConfig.env),
// os.ExpandEnv, syscall.Getenv, or a dot-imported "os" package's bare
// Getenv/LookupEnv call (no os. prefix for osImportNames to match).
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

// selectorLabel classifies sel as a knob-reader selector and returns its
// normalised form for messages and allow-list matching, or "" if sel is not
// a knob read. Resolve/ResolveOptional/Lookup match on method name alone,
// regardless of receiver shape (doc, d2.doc, h.d, ...): without go/types
// the receiver cannot be confirmed as an inputdoc.Document anyway.
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

// scanKnobEnvSites walks every non-test *.go file in dir and returns each
// knob-shaped call site's literal string key and every "file:line" location
// it was read from, plus one violation message for each knob-reader
// selector found with a non-literal key outside the allow-list, or found
// somewhere other than the direct Fun of a call (e.g. bound to a variable
// as a method value) — a shape this guard cannot trace to a key at all.
// ReadDir/ParseFile failures return a plain error rather than a violation.
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

			// First pass: every SelectorExpr that is the direct Fun of some
			// call, keyed by node identity so the second pass can tell a
			// direct call apart from a method value bound and used later.
			// Also record, by the same node identity, whether a selector
			// sits inside a *ast.FuncLit nested somewhere under decl — the
			// allow-list only exempts decl's own direct body, not a closure
			// declared inside it.
			callFun := make(map[*ast.SelectorExpr]*ast.CallExpr)
			inFuncLit := make(map[*ast.SelectorExpr]bool)
			ast.Inspect(decl, func(n ast.Node) bool {
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
				if !ok {
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

// TestDaemonOnlyKnobs_MatchesDocLookupSites pins daemonOnlyKnobs to the keys
// the daemon still reads through inputdoc.Document's ambient-wins Lookup
// path: a knob added via doc.Resolve without being listed, or moved to
// childKnob but left listed, would make warnStrippedChildEnv's wording lie.
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
		if !daemonOnlyKnobs[k] {
			t.Errorf("%s is read via doc.Resolve/ResolveOptional/Lookup but missing from daemonOnlyKnobs", k)
		}
	}
	for k := range daemonOnlyKnobs {
		if !got[k] {
			t.Errorf("daemonOnlyKnobs lists %s, but no doc.Resolve/ResolveOptional/Lookup site reads it", k)
		}
	}
}

// TestDaemonOnlyKnobs_AreLauncherIgnoresInFlagTable ties daemonOnlyKnobs to
// the generated flag table, whose launcherIgnores entries are the knobs "read
// by the daemon only": a key listed here that the table does not mark (or
// the reverse) means a child may read a knob the daemon resolves ambient-first.
// The parent package's source is in the nix go-test sandbox, which copies the
// whole cmd/launcher tree.
func TestDaemonOnlyKnobs_AreLauncherIgnoresInFlagTable(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "../flagtable_gen.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ignores := make(map[string]bool)
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		var env string
		var ignored bool
		for _, el := range lit.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				return true
			}
			switch v := kv.Value.(type) {
			case *ast.BasicLit:
				if key.Name == "env" && v.Kind == token.STRING {
					env, _ = strconv.Unquote(v.Value)
				}
			case *ast.Ident:
				ignored = ignored || (key.Name == "launcherIgnores" && v.Name == "true")
			}
		}
		if env != "" && ignored {
			ignores[env] = true
		}
		return true
	})
	if len(ignores) == 0 {
		t.Fatal("found no launcherIgnores entries in ../flagtable_gen.go — a rename likely broke the walk")
	}
	for k := range daemonOnlyKnobs {
		if !ignores[k] {
			t.Errorf("daemonOnlyKnobs lists %s, but flagtable_gen.go does not mark it launcherIgnores", k)
		}
	}
	for k := range ignores {
		if !daemonOnlyKnobs[k] {
			t.Errorf("flagtable_gen.go marks %s launcherIgnores, but daemonOnlyKnobs omits it", k)
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

	for _, key := range []string{"A", "B", "C", "D"} {
		if len(sites[key]) == 0 {
			t.Errorf("sites is missing literal key %q", key)
		}
	}
	if _, ok := sites["IGNORED"]; ok {
		t.Error("scan should skip _test.go files, but found the IGNORED key from fixture_test.go")
	}

	// lineOf finds marker's line number in the fixture source, so each
	// expected violation is pinned to the exact statement it came from
	// rather than a name substring that a later, unrelated fixture shape
	// could also produce (e.g. two shapes both named warnStrippedChildEnv).
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
