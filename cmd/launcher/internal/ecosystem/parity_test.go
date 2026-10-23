package ecosystem

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

	"spindrift.dev/launcher/internal/testutil"
)

// isStrategyType reports whether expr is the bare identifier Strategy, the type a
// package-level Strategy var's composite literal must carry to be counted.
func isStrategyType(expr ast.Expr) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == "Strategy"
}

// isStrategySliceType reports whether expr is []Strategy, the type Table's own
// composite literal must carry.
func isStrategySliceType(expr ast.Expr) bool {
	at, ok := expr.(*ast.ArrayType)
	return ok && at.Len == nil && isStrategyType(at.Elt)
}

// isRowValueExpr reports whether expr is a value a Strategy var may hold: a
// Strategy{...} literal, or a call to a func named in rowFuncs. rowFuncs must
// come from a prior pass over the same files, since `var fooRow =
// newRow(...)` lets the func live in a different file than the var.
func isRowValueExpr(expr ast.Expr, rowFuncs map[string]bool) bool {
	switch v := expr.(type) {
	case *ast.CompositeLit:
		return isStrategyType(v.Type)
	case *ast.CallExpr:
		id, ok := v.Fun.(*ast.Ident)
		return ok && rowFuncs[id.Name]
	default:
		return false
	}
}

// collectRowFuncs returns the names of every package-level func across files
// that returns exactly one Strategy. It excludes methods: a Strategy var is never
// assigned from a method call without a receiver expression, which this
// syntactic scan cannot resolve anyway.
func collectRowFuncs(files []*ast.File) map[string]bool {
	rowFuncs := make(map[string]bool)
	for _, file := range files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || fd.Type.Results == nil {
				continue
			}
			results := fd.Type.Results
			if results.NumFields() == 1 && len(results.List) == 1 && isStrategyType(results.List[0].Type) {
				rowFuncs[fd.Name.Name] = true
			}
		}
	}
	return rowFuncs
}

// scanForUntabledRows returns, sorted, every package-level Strategy var in dir
// missing from `var Table = []Strategy{...}`. It does not recurse, because a Strategy
// var and its Table entry always share a package directory, and it parses
// rather than importing, so it still works mid-refactor when the package does
// not build. A missing Table is fatal, so a rename cannot drop coverage.
func scanForUntabledRows(t *testing.T, dir string) []string {
	t.Helper()

	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("scanForUntabledRows: dir %s does not exist: %v", dir, err)
	}

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
		path := filepath.Join(dir, name)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("ParseFile(%s): %v", path, err)
		}
		files = append(files, file)
	}

	// This pass runs before the var pass rather than interleaved with it,
	// because the func a Strategy var calls may live in a different file.
	rowFuncs := collectRowFuncs(files)

	rowVars := make(map[string]bool)
	tableIdents := make(map[string]bool)
	tableFound := false

	for _, file := range files {
		// Only top-level decls, so a Strategy literal built inside a function body
		// is never mistaken for a Strategy awaiting a Table entry.
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

				if len(vs.Names) == 1 && len(vs.Values) == 1 && vs.Names[0].Name == "Table" {
					if lit, ok := vs.Values[0].(*ast.CompositeLit); ok && isStrategySliceType(lit.Type) {
						tableFound = true
						for _, elt := range lit.Elts {
							if id, ok := elt.(*ast.Ident); ok {
								tableIdents[id.Name] = true
							}
						}
						continue
					}
				}

				if len(vs.Values) == 0 {
					// Typed, uninitialized: var fooRow Strategy / var aRow, bRow Strategy.
					if isStrategyType(vs.Type) {
						for _, n := range vs.Names {
							rowVars[n.Name] = true
						}
					}
					continue
				}

				if len(vs.Names) != len(vs.Values) {
					continue
				}
				for i, n := range vs.Names {
					if isRowValueExpr(vs.Values[i], rowFuncs) {
						rowVars[n.Name] = true
					}
				}
			}
		}
	}

	if !tableFound {
		t.Fatalf("scanForUntabledRows: no `var Table = []Strategy{...}` declaration found in %s", dir)
	}

	var missing []string
	for name := range rowVars {
		if !tableIdents[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

// TestScanForUntabledRows_FixtureDetectsViolation proves a Strategy var missing
// from Table trips the check in every shape the scan recognizes. Each shape
// pairs a tagged var (listed in Table) with an untabled one, so a shape that
// stopped being detected drops out of the result instead of passing silently.
// The fixture spans two files because a Strategy var's func may live in another.
func TestScanForUntabledRows_FixtureDetectsViolation(t *testing.T) {
	dir := t.TempDir()

	testutil.WriteFixture(t, dir, "rows.go", `package fixture

type Strategy struct {
	Name string
}

type OtherThing struct {
	Name string
}

// shape 1: var fooRow = Strategy{...}
var taggedSimpleRow = Strategy{Name: "tagged simple"}
var untabledSimpleRow = Strategy{Name: "untabled simple"}

// shape 2: var fooRow Strategy (assigned in init(), not at the declaration)
var taggedNoValueRow Strategy
var untabledNoValueRow Strategy

func init() {
	taggedNoValueRow = Strategy{Name: "tagged no value"}
	untabledNoValueRow = Strategy{Name: "untabled no value"}
}

// shape 3: var fooRow Strategy = Strategy{...}
var taggedTypedRow Strategy = Strategy{Name: "tagged typed"}
var untabledTypedRow Strategy = Strategy{Name: "untabled typed"}

// shape 4: var aRow, bRow = Strategy{...}, Strategy{...}
var taggedMultiA, taggedMultiB = Strategy{Name: "tagged multi a"}, Strategy{Name: "tagged multi b"}
var untabledMultiA, untabledMultiB = Strategy{Name: "untabled multi a"}, Strategy{Name: "untabled multi b"}

// shape 5: var aRow, bRow Strategy
var taggedMultiNoValueA, taggedMultiNoValueB Strategy
var untabledMultiNoValueA, untabledMultiNoValueB Strategy

// shape 6: var fooRow = newRow(...), newRow declared in funcs.go
var taggedFromFuncRow = newTaggedRow()
var untabledFromFuncRow = newUntabledRow()

// shapes 1-3, grouped inside var ( ... )
var (
	taggedGroupedRow          = Strategy{Name: "tagged grouped"}
	untabledGroupedRow        = Strategy{Name: "untabled grouped"}
	taggedGroupedNoValueRow   Strategy
	untabledGroupedNoValueRow Strategy
	taggedGroupedTypedRow     Strategy = Strategy{Name: "tagged grouped typed"}
	untabledGroupedTypedRow   Strategy = Strategy{Name: "untabled grouped typed"}
)

// must never be counted as a Strategy var
var otherThingNotARow = OtherThing{Name: "not a row"}
var otherThingFromFuncNotARow = newOtherThing()

func buildLocalRow() Strategy {
	localRow := Strategy{Name: "local, inside a function body"}
	return localRow
}

var Table = []Strategy{
	taggedSimpleRow,
	taggedNoValueRow,
	taggedTypedRow,
	taggedMultiA,
	taggedMultiB,
	taggedMultiNoValueA,
	taggedMultiNoValueB,
	taggedFromFuncRow,
	taggedGroupedRow,
	taggedGroupedNoValueRow,
	taggedGroupedTypedRow,
}
`)

	testutil.WriteFixture(t, dir, "funcs.go", `package fixture

func newTaggedRow() Strategy {
	return Strategy{Name: "tagged from func"}
}

func newUntabledRow() Strategy {
	return Strategy{Name: "untabled from func"}
}

func newOtherThing() OtherThing {
	return OtherThing{Name: "other thing from func"}
}
`)

	want := []string{
		"untabledFromFuncRow",
		"untabledGroupedNoValueRow",
		"untabledGroupedRow",
		"untabledGroupedTypedRow",
		"untabledMultiA",
		"untabledMultiB",
		"untabledMultiNoValueA",
		"untabledMultiNoValueB",
		"untabledNoValueRow",
		"untabledSimpleRow",
		"untabledTypedRow",
	}

	got := scanForUntabledRows(t, dir)
	if !slices.Equal(got, want) {
		t.Fatalf("scanForUntabledRows(%s) = %v, want %v", dir, got, want)
	}
}

// TestEveryDeclaredRowIsInTheTable is the parity half of
// containment_test.go's ecosystem-name check: a package-level `var fooRow =
// Strategy{...}` declaration must always be listed in Table.
func TestEveryDeclaredRowIsInTheTable(t *testing.T) {
	missing := scanForUntabledRows(t, ".")
	if len(missing) > 0 {
		t.Errorf("row var(s) declared but missing from Table: %s", strings.Join(missing, ", "))
	}
}
