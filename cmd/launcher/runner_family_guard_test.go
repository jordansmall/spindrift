package main

import (
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

	"spindrift.dev/launcher/internal/runner"
)

const (
	runnerDir   = "internal/runner"
	bundleDir   = "internal/driver/bundle"
	kitDir      = runnerDir + "/runnertest"
	familiesGen = runnerDir + "/families_gen.go"

	// familyGuardMarker opens an exemption comment; the text after it is the
	// reason, and a marker without one exempts nothing.
	familyGuardMarker = "runner-family-guard:"

	runtimeNameMsg = "runtime-name comparison outside the OCI/bwrap family packages: move it into the family's dialect"
	familyConstMsg = "runner family constant outside the family packages: read the Descriptor row or the Strategy instead"
	emptyReasonMsg = "runner-family-guard marker without a reason exempts nothing: say why the exemption is needed"
)

// familyNames returns every runner family name, runtime alias and runtime
// binary, derived from the generated rows so a new family is guarded without
// editing this test.
func familyNames() map[string]bool {
	names := map[string]bool{}
	for _, f := range runner.Families {
		names[f.Name] = true
		for _, rt := range f.Runtimes {
			names[rt] = true
			names[runner.BinaryFor(rt)] = true
		}
	}
	return names
}

// familyIdents parses families_gen.go (path is relative to the launcher
// module root) for its top-level constants and its Descriptor-valued vars.
// Families itself is not one: ranging over the rows is the sanctioned form.
func familyIdents(t testing.TB, path string) map[string]bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	idents := map[string]bool{}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, id := range vs.Names {
				switch {
				case gd.Tok == token.CONST:
					idents[id.Name] = true
				case gd.Tok == token.VAR && i < len(vs.Values) && isDescriptorLiteral(vs.Values[i]):
					idents[id.Name] = true
				}
			}
		}
	}
	if len(idents) == 0 {
		t.Fatalf("%s declares no family constants: the guard is reading the wrong file", path)
	}
	return idents
}

func isDescriptorLiteral(e ast.Expr) bool {
	cl, ok := e.(*ast.CompositeLit)
	if !ok {
		return false
	}
	id, ok := cl.Type.(*ast.Ident)
	return ok && id.Name == "Descriptor"
}

// familyDirs lists the per-family packages, internal/runner/<name>, which own
// the runtime spellings and so are outside the guard.
func familyDirs() []string {
	var dirs []string
	for _, f := range runner.Families {
		dirs = append(dirs, runnerDir+"/"+f.Name)
	}
	return dirs
}

// outOfScopeDir reports whether dir (slash-separated, relative to the module
// root) is exempt by structure: not walked at all.
func outOfScopeDir(dir string, famDirs []string) bool {
	base := filepath.Base(dir)
	switch {
	case dir == ".":
		return false
	case base == "testdata", strings.HasPrefix(base, "."):
		return true // fixtures and tooling dirs are not product code
	case slices.Contains(famDirs, dir):
		return true // the family packages own the runtime spellings
	case dir == kitDir || strings.HasPrefix(dir, kitDir+"/"):
		return true // the test-only contract kit names a family on purpose to scope a contract row (ADR 0069)
	case dir == bundleDir:
		return true // the Bundle links every family package into the binaries by name
	}
	return false
}

// runnerFamilyViolations parses src as the file at path (slash-separated) and
// returns one "path:line: why\n\t<source line>" per comparison that spells a
// runtime/family name or reference to a family constant. A marker with a
// reason exempts its own line, and the following line only when the marker
// comment stands alone on its line. Generated files yield nothing.
//
// Known limits: only ==, != and case against a string literal are seen, so
// strings.EqualFold(rt, "podman"), slices.Contains(xs, "docker") and a named
// constant compared to rt pass; family constants are matched through the
// literal identifier runner, so an aliased or dot import passes.
func runnerFamilyViolations(t testing.TB, path string, src []byte, names, idents map[string]bool) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if ast.IsGenerated(file) {
		return nil
	}
	lines := strings.Split(string(src), "\n")
	var out []string
	report := func(pos token.Pos, why string) {
		line := fset.Position(pos).Line
		out = append(out, path+":"+strconv.Itoa(line)+": "+why+"\n\t"+strings.TrimSpace(lines[line-1]))
	}

	exempt := map[int]bool{}
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			_, reason, found := strings.Cut(c.Text, familyGuardMarker)
			if !found {
				continue
			}
			line := fset.Position(c.Slash).Line
			if strings.TrimSpace(strings.TrimSuffix(reason, "*/")) == "" {
				report(c.Slash, emptyReasonMsg)
				continue
			}
			exempt[line] = true
			// A marker sharing its line with code, before or after it, covers
			// only its own line.
			end := fset.Position(c.End())
			if strings.TrimSpace(lines[line-1][:fset.Position(c.Slash).Column-1]) == "" &&
				strings.TrimSpace(lines[end.Line-1][end.Column-1:]) == "" {
				exempt[line+1] = true
			}
		}
	}
	flag := func(pos token.Pos, why string) {
		if !exempt[fset.Position(pos).Line] {
			report(pos, why)
		}
	}

	isName := func(e ast.Expr) bool {
		bl, ok := e.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return false
		}
		s, err := strconv.Unquote(bl.Value)
		return err == nil && names[s]
	}

	// Selector fields (cfg.OCI) are not references to the constant.
	sels := map[*ast.Ident]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		if se, ok := n.(*ast.SelectorExpr); ok {
			sels[se.Sel] = true
		}
		return true
	})
	inRunner := file.Name.Name == "runner"

	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.BinaryExpr:
			if (n.Op == token.EQL || n.Op == token.NEQ) && (isName(n.X) || isName(n.Y)) {
				flag(n.OpPos, runtimeNameMsg)
			}
		case *ast.CaseClause:
			for _, e := range n.List {
				if isName(e) {
					flag(e.Pos(), runtimeNameMsg)
				}
			}
		case *ast.SelectorExpr:
			if x, ok := n.X.(*ast.Ident); ok && x.Name == "runner" && idents[n.Sel.Name] {
				flag(n.Pos(), familyConstMsg)
			}
		case *ast.Ident:
			if inRunner && idents[n.Name] && !sels[n] {
				flag(n.Pos(), familyConstMsg)
			}
		}
		return true
	})
	return out
}

// TestRunnerFamilyGuard walks the launcher module and fails on a runtime or
// family name compared, or a family constant referenced, outside the family
// packages and the Bundle: core reads the Descriptor row or the Strategy
// instead of re-deriving a family fact from its name.
func TestRunnerFamilyGuard(t *testing.T) {
	names := familyNames()
	idents := familyIdents(t, familiesGen)
	famDirs := familyDirs()
	for _, d := range famDirs {
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			t.Fatalf("family package %s does not exist: a rename would widen the guard's scope", d)
		}
	}
	scanned := 0
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		slash := filepath.ToSlash(path)
		if d.IsDir() {
			if outOfScopeDir(slash, famDirs) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil // tests pin runtime spellings as fixtures
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		scanned++
		for _, v := range runnerFamilyViolations(t, slash, src, names, idents) {
			t.Error(v)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk .: %v", err)
	}
	if scanned == 0 {
		t.Fatal("no .go files scanned: the guard is walking the wrong tree")
	}
}

// TestRunnerFamilyGuardPlanted pins what the guard flags and spares, so a
// loosened check shows up here rather than as a silently passing walk.
func TestRunnerFamilyGuardPlanted(t *testing.T) {
	names := map[string]bool{"podman": true, "docker": true, "rancher": true, "oci": true, "bwrap": true}
	idents := map[string]bool{"KindOCI": true, "KindBwrap": true, "OCI": true, "Bwrap": true}
	check := func(path, src string) int {
		return len(runnerFamilyViolations(t, path, []byte(src), names, idents))
	}
	const hdr = "package x\n\nfunc f() {\n"
	wrap := func(body string) string { return hdr + body + "\n}\n" }

	flagged := map[string]string{
		"eq literal":        `if cfg.Runtime == "podman" {}`,
		"neq literal left":  `if "docker" != rt {}`,
		"case clause":       "switch rt {\ncase \"rancher\":\n}",
		"family const":      `if kind == runner.KindBwrap {}`,
		"family descriptor": `x := runner.OCI; _ = x`,
		"empty reason":      "// runner-family-guard:\nx := 1; _ = x",
	}
	for name, body := range flagged {
		if check("internal/x/x.go", wrap(body)) == 0 {
			t.Errorf("%s: %q not flagged", name, body)
		}
	}

	spared := map[string]string{
		"similar literal":     `if rt == "podmanx" {}`,
		"name not compared":   `m := map[string]int{"podman": 1}; _ = m`,
		"exempt line above":   "// runner-family-guard: the one family core picks\nif rt == \"podman\" {}",
		"exempt same line":    `if rt == "podman" {} // runner-family-guard: pinned to podman`,
		"unrelated selector":  `_ = cfg.OCI`,
		"other package const": `_ = other.KindOCI`,
	}
	for name, body := range spared {
		if n := check("internal/x/x.go", wrap(body)); n != 0 {
			t.Errorf("%s: %q flagged %d time(s)", name, body, n)
		}
	}

	for name, body := range map[string]string{
		"trailing marker":          "if rt == \"docker\" {} // runner-family-guard: why\nif rt == \"podman\" {}",
		"block marker before code": "/* runner-family-guard: why */ if rt == \"docker\" {}\nif rt == \"podman\" {}",
	} {
		if n := check("internal/x/x.go", wrap(body)); n != 1 {
			t.Errorf("%s: %q reported %d time(s), want 1 (the unmarked line only)", name, body, n)
		}
	}
	if n := check("internal/x/x.go", wrap(`if rt == "podman" {}`)+"// runner-family-guard:   \n"); n != 2 {
		t.Errorf("empty-reason marker: got %d reports, want 2 (the comparison and the marker)", n)
	}
	if n := check("internal/x/x.go", wrap("// runner-family-guard: far above\n\nif rt == \"podman\" {}")); n == 0 {
		t.Error("marker two lines above exempted a comparison")
	}

	bare := "package runner\n\nconst defaultKind = KindOCI\n"
	if check("internal/runner/strategy.go", bare) == 0 {
		t.Error("bare family constant in package runner not flagged")
	}
	if check("internal/x/x.go", strings.Replace(bare, "package runner", "package x", 1)) != 0 {
		t.Error("bare family constant outside package runner flagged")
	}
	if check("internal/runner/families_gen.go", "// Code generated by x. DO NOT EDIT.\n\n"+bare) != 0 {
		t.Error("generated file flagged")
	}
}

// TestRunnerFamilyGuardScope pins the directories the walk skips.
func TestRunnerFamilyGuardScope(t *testing.T) {
	fam := []string{"internal/runner/oci", "internal/runner/bwrap"}
	for dir, want := range map[string]bool{
		".":                                   false,
		"internal/runner":                     false,
		"internal/runner/oci":                 true,
		"internal/runner/bwrap":               true,
		"internal/runner/fake":                false,
		"internal/runner/runnertest":          true,
		"internal/runner/runnertest/contract": true,
		"internal/driver/bundle":              true,
		"internal/driver":                     false,
		"internal/x/testdata":                 true,
		"internal/.hidden":                    true,
		"internal/runner/ocipath":             false,
	} {
		if got := outOfScopeDir(dir, fam); got != want {
			t.Errorf("outOfScopeDir(%q) = %v, want %v", dir, got, want)
		}
	}
}

// TestRunnerFamilyGuardDerivation pins that the derived sets cover today's
// families, so an empty derivation can't make the walk vacuous.
func TestRunnerFamilyGuardDerivation(t *testing.T) {
	names := familyNames()
	for _, n := range []string{"oci", "bwrap", "podman", "docker", "rancher", "nerdctl"} {
		if !names[n] {
			t.Errorf("familyNames missing %q", n)
		}
	}
	idents := familyIdents(t, familiesGen)
	for _, c := range []string{"KindOCI", "KindBwrap", "OCI", "Bwrap"} {
		if !idents[c] {
			t.Errorf("familyIdents missing %q", c)
		}
	}
	if idents["Families"] {
		t.Error("Families must not be a guarded family constant")
	}
}
