package bundle_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/testutil"
)

// The registered order is the lockfile first-hit precedence and the env-export
// file's line order, so a reshuffle in ecosystems.go must fail here. The
// expectation is built from the Strategies' own names rather than string
// literals because TestEcosystemNamesStayInTheTable bars ecosystem-name
// literals from every importer of internal/ecosystem, this package included.
func TestRegisteredEcosystemOrderIsPinned(t *testing.T) {
	want := []string{
		ecosystem.Cargo.Name, ecosystem.Npm.Name, ecosystem.Yarn.Name,
		ecosystem.Pnpm.Name, ecosystem.Go.Name, ecosystem.Gradle.Name,
	}
	var got []string
	for _, s := range ecosystem.Registered() {
		got = append(got, s.Name)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Registered() names = %v, want %v", got, want)
	}
}

func isStrategyType(expr ast.Expr) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == "Strategy"
}

// strategyFuncs names every package-level func across files returning exactly
// one Strategy, since `var X = newStrategy(...)` may call into another file.
func strategyFuncs(files []*ast.File) map[string]bool {
	funcs := make(map[string]bool)
	for _, file := range files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || fd.Type.Results == nil {
				continue
			}
			res := fd.Type.Results
			if res.NumFields() == 1 && len(res.List) == 1 && isStrategyType(res.List[0].Type) {
				funcs[fd.Name.Name] = true
			}
		}
	}
	return funcs
}

func isStrategyValue(expr ast.Expr, funcs map[string]bool) bool {
	switch v := expr.(type) {
	case *ast.CompositeLit:
		return isStrategyType(v.Type)
	case *ast.CallExpr:
		id, ok := v.Fun.(*ast.Ident)
		return ok && funcs[id.Name]
	}
	return false
}

// declaredStrategies returns every package-level Strategy var in dir's non-test
// files. It parses rather than imports, so a Strategy declared but never
// reachable from the Bundle is still seen.
func declaredStrategies(t *testing.T, dir string) []string {
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
	funcs := strategyFuncs(files)

	var vars []string
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
					if isStrategyType(vs.Type) {
						for _, n := range vs.Names {
							vars = append(vars, n.Name)
						}
					}
				case len(vs.Names) == len(vs.Values):
					for i, n := range vs.Names {
						if isStrategyValue(vs.Values[i], funcs) {
							vars = append(vars, n.Name)
						}
					}
				}
			}
		}
	}
	return vars
}

// registeredArgs returns the names X of every `ecosystem.X` argument to an
// `ecosystem.Register(...)` call in the file at path.
func registeredArgs(t *testing.T, path string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("ParseFile(%s): %v", path, err)
	}
	var args []string
	calls := 0
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); !ok || sel.Sel.Name != "Register" || !isIdent(sel.X, "ecosystem") {
			return true
		}
		calls++
		for _, a := range call.Args {
			if sel, ok := a.(*ast.SelectorExpr); ok && isIdent(sel.X, "ecosystem") {
				args = append(args, sel.Sel.Name)
			}
		}
		return true
	})
	if calls != 1 {
		t.Fatalf("%s holds %d ecosystem.Register calls, want exactly 1", path, calls)
	}
	return args
}

func isIdent(expr ast.Expr, name string) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == name
}

func unregisteredStrategies(t *testing.T, ecosystemDir, registerFile string) []string {
	t.Helper()
	registered := registeredArgs(t, registerFile)
	var missing []string
	for _, v := range declaredStrategies(t, ecosystemDir) {
		if !slices.Contains(registered, v) {
			missing = append(missing, v)
		}
	}
	sort.Strings(missing)
	return missing
}

// A Strategy declared in internal/ecosystem but absent from the Register call
// would silently never bind, so the Bundle must list every one.
func TestEveryDeclaredStrategyIsRegisteredByTheBundle(t *testing.T) {
	missing := unregisteredStrategies(t, "../../ecosystem", "ecosystems.go")
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
	reg := t.TempDir()
	testutil.WriteFixture(t, reg, "ecosystems.go", `package bundle

import "example.com/ecosystem"

func init() { ecosystem.Register(ecosystem.Listed, ecosystem.ListedA, ecosystem.ListedGrouped) }
`)

	got := unregisteredStrategies(t, eco, filepath.Join(reg, "ecosystems.go"))
	want := []string{"Forgotten", "ForgottenB", "ForgottenFromFunc", "ForgottenGrouped", "ForgottenTyped"}
	if !slices.Equal(got, want) {
		t.Fatalf("unregisteredStrategies = %v, want %v", got, want)
	}
}

// coreRegisteredArgs returns the identifiers passed to the bare Register(...)
// call in TestMain of the file at path, in order.
func coreRegisteredArgs(t *testing.T, path string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("ParseFile(%s): %v", path, err)
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
					t.Fatalf("%s: Register argument is not a bare identifier", path)
				}
				args = append(args, id.Name)
			}
			return true
		})
	}
	if calls != 1 {
		t.Fatalf("%s TestMain holds %d Register calls, want exactly 1", path, calls)
	}
	return args
}

// The core package's tests cannot link the Bundle, so TestMain there hand-lists
// the Strategies; that copy must not drift from the Bundle's own Register call.
func TestCoreTestMainRegistersTheBundlesStrategies(t *testing.T) {
	want := registeredArgs(t, "ecosystems.go")
	got := coreRegisteredArgs(t, "../../ecosystem/main_test.go")
	if !slices.Equal(got, want) {
		t.Fatalf("internal/ecosystem/main_test.go TestMain registers %v, want the Bundle's %v", got, want)
	}
}
