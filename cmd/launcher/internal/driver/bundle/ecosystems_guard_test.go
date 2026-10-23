package bundle_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystem/cargo"
	"spindrift.dev/launcher/internal/ecosystem/gomod"
	"spindrift.dev/launcher/internal/ecosystem/gradle"
	"spindrift.dev/launcher/internal/testutil"
)

// The registered order is the lockfile first-hit precedence and the env-export
// file's line order, so a reshuffle in ecosystems.go must fail here. The
// expectation is built from the Strategies' own names rather than string
// literals because TestEcosystemNamesStayInTheirOwnPackage bars ecosystem-name
// literals from every importer of internal/ecosystem, this package included.
func TestRegisteredEcosystemOrderIsPinned(t *testing.T) {
	want := []string{
		cargo.Strategy.Name, ecosystem.Npm.Name, ecosystem.Yarn.Name,
		ecosystem.Pnpm.Name, gomod.Strategy.Name, gradle.Strategy.Name,
	}
	var got []string
	for _, s := range ecosystem.Registered() {
		got = append(got, s.Name)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Registered() names = %v, want %v", got, want)
	}
}

const (
	realModuleRoot = "../../.."
	// ecosystemKit is the one package under internal/ecosystem that is not an
	// ecosystem: it holds the helpers none of them owns.
	ecosystemKit = "ecosystemkit"
)

// strategyRef names a Strategy var by the package that declares it.
type strategyRef struct {
	ImportPath string
	Name       string
}

func (r strategyRef) String() string { return path.Base(r.ImportPath) + "." + r.Name }

// isStrategyType reports whether expr names the ecosystem Strategy type: the
// bare `Strategy` inside the core package (qualifier ""), or `<qualifier>.Strategy`
// from an ecosystem package.
func isStrategyType(expr ast.Expr, qualifier string) bool {
	if qualifier == "" {
		return isIdent(expr, "Strategy")
	}
	sel, ok := expr.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Strategy" && isIdent(sel.X, qualifier)
}

// strategyFuncs names every package-level func across files returning exactly
// one Strategy, since `var X = newStrategy(...)` may call into another file.
func strategyFuncs(files []*ast.File, qualifier string) map[string]bool {
	funcs := make(map[string]bool)
	for _, file := range files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || fd.Type.Results == nil {
				continue
			}
			res := fd.Type.Results
			if res.NumFields() == 1 && len(res.List) == 1 && isStrategyType(res.List[0].Type, qualifier) {
				funcs[fd.Name.Name] = true
			}
		}
	}
	return funcs
}

func isStrategyValue(expr ast.Expr, funcs map[string]bool, qualifier string) bool {
	switch v := expr.(type) {
	case *ast.CompositeLit:
		return isStrategyType(v.Type, qualifier)
	case *ast.CallExpr:
		id, ok := v.Fun.(*ast.Ident)
		return ok && funcs[id.Name]
	}
	return false
}

// declaredStrategies returns every package-level Strategy var in dir's non-test
// files, referenced by importPath. It parses rather than imports, so a Strategy
// declared but never reachable from the Bundle is still seen. qualifier is how
// dir's files name the Strategy type: "" for the core package itself,
// "ecosystem" for an ecosystem package that imports it.
func declaredStrategies(t *testing.T, dir, importPath, qualifier string) []strategyRef {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("ParseFile(%s): %v", name, err)
		}
		files = append(files, file)
	}
	funcs := strategyFuncs(files, qualifier)

	var vars []strategyRef
	add := func(n *ast.Ident) { vars = append(vars, strategyRef{ImportPath: importPath, Name: n.Name}) }
	for _, file := range files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				switch {
				case len(vs.Values) == 0:
					if vs.Type != nil && isStrategyType(vs.Type, qualifier) {
						for _, n := range vs.Names {
							add(n)
						}
					}
				case len(vs.Names) == len(vs.Values):
					for i, n := range vs.Names {
						if isStrategyValue(vs.Values[i], funcs, qualifier) {
							add(n)
						}
					}
				}
			}
		}
	}
	return vars
}

// importedPaths maps each import's local name to its path. A package whose
// name differs from its last path element must be imported under an explicit
// name for this to resolve it.
func importedPaths(file *ast.File) map[string]string {
	m := make(map[string]string)
	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		local := path.Base(p)
		if imp.Name != nil {
			local = imp.Name.Name
		}
		m[local] = p
	}
	return m
}

// readRegisterArgs returns every argument of the file's one Register call on
// the package at ecosystemImport, in order, each resolved to the package that
// declares it. An argument that is not a package-qualified identifier is an
// error rather than a silently dropped entry.
func readRegisterArgs(filePath, ecosystemImport string) ([]strategyRef, error) {
	file, err := parser.ParseFile(token.NewFileSet(), filePath, nil, 0)
	if err != nil {
		return nil, err
	}
	imports := importedPaths(file)
	var args []strategyRef
	var problems []string
	calls := 0
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		fun, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || fun.Sel.Name != "Register" {
			return true
		}
		if x, ok := fun.X.(*ast.Ident); !ok || imports[x.Name] != ecosystemImport {
			return true
		}
		calls++
		for _, a := range call.Args {
			sel, ok := a.(*ast.SelectorExpr)
			if !ok {
				problems = append(problems, "Register argument is not a <package>.<Strategy> selector")
				continue
			}
			x, ok := sel.X.(*ast.Ident)
			if !ok || imports[x.Name] == "" {
				problems = append(problems, fmt.Sprintf("Register argument %s names no imported package", sel.Sel.Name))
				continue
			}
			args = append(args, strategyRef{ImportPath: imports[x.Name], Name: sel.Sel.Name})
		}
		return true
	})
	if len(problems) > 0 {
		return nil, fmt.Errorf("%s: %s", filePath, strings.Join(problems, "; "))
	}
	if calls != 1 {
		return nil, fmt.Errorf("%s holds %d ecosystem.Register calls, want exactly 1", filePath, calls)
	}
	return args, nil
}

func registerArgs(t *testing.T, filePath, ecosystemImport string) []strategyRef {
	t.Helper()
	args, err := readRegisterArgs(filePath, ecosystemImport)
	if err != nil {
		t.Fatal(err)
	}
	return args
}

func isIdent(expr ast.Expr, name string) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == name
}

// coreQualifier is the local name dir's non-test files import the package at
// ecosystemImport under, resolved as readRegisterArgs does; "ecosystem" if none
// of them imports it.
func coreQualifier(t *testing.T, dir, ecosystemImport string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("ParseFile(%s): %v", name, err)
		}
		for local, p := range importedPaths(file) {
			if p == ecosystemImport {
				return local
			}
		}
	}
	return "ecosystem"
}

// allDeclaredStrategies scans the core package at ecosystemDir and every
// ecosystem package beneath it (ecosystemkit and testdata excluded), mapping
// each directory to its import path under ecosystemImport.
func allDeclaredStrategies(t *testing.T, ecosystemDir, ecosystemImport string) []strategyRef {
	t.Helper()
	vars := declaredStrategies(t, ecosystemDir, ecosystemImport, "")
	for _, dir := range packageDirs(t, ecosystemDir) {
		rel, err := filepath.Rel(ecosystemDir, dir)
		if err != nil {
			t.Fatal(err)
		}
		if rel == "." || rel == ecosystemKit || strings.HasPrefix(rel, ecosystemKit+string(filepath.Separator)) {
			continue
		}
		vars = append(vars, declaredStrategies(t, dir, ecosystemImport+"/"+filepath.ToSlash(rel), coreQualifier(t, dir, ecosystemImport))...)
	}
	return vars
}

func unregisteredStrategies(t *testing.T, ecosystemDir, ecosystemImport, registerFile string) []string {
	t.Helper()
	registered := registerArgs(t, registerFile, ecosystemImport)
	var missing []string
	for _, v := range allDeclaredStrategies(t, ecosystemDir, ecosystemImport) {
		if !slices.Contains(registered, v) {
			missing = append(missing, v.String())
		}
	}
	sort.Strings(missing)
	return missing
}

// A Strategy declared in internal/ecosystem or in a package beneath it but
// absent from the Register call would silently never bind, so the Bundle must
// list every one.
func TestEveryDeclaredStrategyIsRegisteredByTheBundle(t *testing.T) {
	moduleName := readModuleName(t, filepath.Join(realModuleRoot, "go.mod"))
	missing := unregisteredStrategies(t, filepath.Join(realModuleRoot, "internal", "ecosystem"), moduleName+"/internal/ecosystem", "ecosystems.go")
	if len(missing) > 0 {
		t.Errorf("Strategy var(s) declared but missing from the Register call in ecosystems.go: %s", strings.Join(missing, ", "))
	}
}

func TestUnregisteredStrategiesClassifiesPlantedInput(t *testing.T) {
	eco := t.TempDir()
	testutil.WriteFixture(t, eco, "rows.go", `package ecosystem

type Strategy struct{ Name string }
type Other struct{ Name string }

var Listed = Strategy{Name: "listed"}
var Forgotten = Strategy{Name: "forgotten"}
var ForgottenFromFunc = newStrategy()
var ForgottenTyped Strategy
var ListedA, ForgottenB = Strategy{}, Strategy{}
var NotAStrategy = Other{}
var NotAStrategyFromFunc = newOther()

var (
	ListedGrouped    = Strategy{}
	ForgottenGrouped = Strategy{}
)

func buildLocal() Strategy {
	local := Strategy{Name: "local, inside a function body"}
	return local
}
`)
	// The func a Strategy var calls may live in a different file than the var.
	testutil.WriteFixture(t, eco, "funcs.go", `package ecosystem

func newStrategy() Strategy { return Strategy{} }

func newOther() Other { return Other{} }
`)
	testutil.WriteFixture(t, eco, "rows_test.go", "package ecosystem\n\nvar testOnly = Strategy{}\n")
	// An ecosystem package qualifies the type, may build one through a func, and
	// may declare several.
	testutil.WriteFixture(t, filepath.Join(eco, "sub"), "sub.go", `package sub

import "example.com/ecosystem"

type Strategy struct{}

var Listed = ecosystem.Strategy{}
var SubForgotten = ecosystem.Strategy{}
var SubForgottenFromFunc = build()
var NotAStrategy = Strategy{}

func build() ecosystem.Strategy { return ecosystem.Strategy{} }
`)
	// ecosystemkit is not an ecosystem, and testdata is not compiled.
	testutil.WriteFixture(t, filepath.Join(eco, "ecosystemkit"), "kit.go", "package ecosystemkit\n\nimport \"example.com/ecosystem\"\n\nvar KitStrategy = ecosystem.Strategy{}\n")
	testutil.WriteFixture(t, filepath.Join(eco, "sub", "testdata"), "t.go", "package t\n\nimport \"example.com/ecosystem\"\n\nvar Planted = ecosystem.Strategy{}\n")
	reg := t.TempDir()
	testutil.WriteFixture(t, reg, "ecosystems.go", `package bundle

import (
	"example.com/ecosystem"
	"example.com/ecosystem/sub"
)

func init() {
	ecosystem.Register(ecosystem.Listed, ecosystem.ListedA, ecosystem.ListedGrouped, sub.Listed)
}
`)

	got := unregisteredStrategies(t, eco, "example.com/ecosystem", filepath.Join(reg, "ecosystems.go"))
	want := []string{
		"ecosystem.Forgotten", "ecosystem.ForgottenB", "ecosystem.ForgottenFromFunc",
		"ecosystem.ForgottenGrouped", "ecosystem.ForgottenTyped",
		"sub.SubForgotten", "sub.SubForgottenFromFunc",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("unregisteredStrategies = %v, want %v", got, want)
	}
}

// A Register argument that is not a <package>.<Strategy> selector would drop
// out of every comparison below, so reading one must fail.
func TestReadRegisterArgsRejectsAnUnreadableArgument(t *testing.T) {
	reg := t.TempDir()
	testutil.WriteFixture(t, reg, "ecosystems.go", `package bundle

import "example.com/ecosystem"

func init() { ecosystem.Register(ecosystem.Listed, local) }
`)
	if _, err := readRegisterArgs(filepath.Join(reg, "ecosystems.go"), "example.com/ecosystem"); err == nil {
		t.Fatal("readRegisterArgs accepted a bare-identifier argument")
	}
}

func TestReadRegisterArgsResolvesQualifiersThroughImports(t *testing.T) {
	reg := t.TempDir()
	testutil.WriteFixture(t, reg, "ecosystems.go", `package bundle

import (
	core "example.com/ecosystem"
	"example.com/ecosystem/sub"
)

func init() { core.Register(core.A, sub.S) }
`)
	got, err := readRegisterArgs(filepath.Join(reg, "ecosystems.go"), "example.com/ecosystem")
	want := []strategyRef{{"example.com/ecosystem", "A"}, {"example.com/ecosystem/sub", "S"}}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("readRegisterArgs = %v, %v; want %v", got, err, want)
	}
}

// coreRegisteredArgs returns the identifiers passed to the bare Register(...)
// call in TestMain of the file at path, in order.
func coreRegisteredArgs(t *testing.T, filePath string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filePath, nil, 0)
	if err != nil {
		t.Fatalf("ParseFile(%s): %v", filePath, err)
	}
	var args []string
	calls := 0
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Recv != nil || fd.Name.Name != "TestMain" {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isIdent(call.Fun, "Register") {
				return true
			}
			calls++
			for _, a := range call.Args {
				id, ok := a.(*ast.Ident)
				if !ok {
					t.Fatalf("%s: Register argument is not a bare identifier", filePath)
				}
				args = append(args, id.Name)
			}
			return true
		})
	}
	if calls != 1 {
		t.Fatalf("%s TestMain holds %d Register calls, want exactly 1", filePath, calls)
	}
	return args
}

// coreResidentArgs keeps the Bundle's Register arguments that the core package
// itself declares, in order. Every other argument must come from a package
// directly beneath internal/ecosystem (ecosystemkit aside), so one that does
// not is reported as a problem rather than silently dropped.
func coreResidentArgs(args []strategyRef, ecosystemImport string) (core, problems []string) {
	for _, a := range args {
		switch {
		case a.ImportPath == ecosystemImport:
			core = append(core, a.Name)
		case strings.HasPrefix(a.ImportPath, ecosystemImport+"/") && a.ImportPath != ecosystemImport+"/"+ecosystemKit:
		default:
			problems = append(problems, a.String())
		}
	}
	return core, problems
}

// The core package's tests cannot link the Bundle, so TestMain there hand-lists
// the Strategies; that copy must not drift from the Bundle's own Register call.
// A Strategy the Bundle takes from an ecosystem package of its own (cargo, gradle, gomod)
// is outside this copy, since core cannot import it.
func TestCoreTestMainRegistersTheBundlesStrategies(t *testing.T) {
	moduleName := readModuleName(t, filepath.Join(realModuleRoot, "go.mod"))
	want, problems := coreResidentArgs(registerArgs(t, "ecosystems.go", moduleName+"/internal/ecosystem"), moduleName+"/internal/ecosystem")
	if len(problems) > 0 {
		t.Fatalf("Register argument(s) declared outside internal/ecosystem and its ecosystem packages: %v", problems)
	}
	got := coreRegisteredArgs(t, "../../ecosystem/main_test.go")
	if !slices.Equal(got, want) {
		t.Fatalf("internal/ecosystem/main_test.go TestMain registers %v, want the Bundle's core-resident %v", got, want)
	}
}

func TestCoreResidentArgsClassifiesPlantedInput(t *testing.T) {
	const eco = "example.com/ecosystem"
	got, problems := coreResidentArgs([]strategyRef{
		{eco, "A"}, {eco + "/sub", "S"}, {eco, "B"}, {"example.com/elsewhere", "X"}, {eco + "/ecosystemkit", "K"},
	}, eco)
	if want := []string{"A", "B"}; !slices.Equal(got, want) {
		t.Fatalf("coreResidentArgs = %v, want %v", got, want)
	}
	if want := []string{"elsewhere.X", "ecosystemkit.K"}; !slices.Equal(problems, want) {
		t.Fatalf("problems = %v, want %v", problems, want)
	}
}
