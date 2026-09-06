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
)

// isRowType reports whether expr is the bare identifier Row -- the type a
// package-level row var's composite literal must carry to be counted.
func isRowType(expr ast.Expr) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == "Row"
}

// isRowSliceType reports whether expr is []Row -- the type Table's own
// composite literal must carry.
func isRowSliceType(expr ast.Expr) bool {
	at, ok := expr.(*ast.ArrayType)
	return ok && at.Len == nil && isRowType(at.Elt)
}

// isRowValueExpr reports whether expr is a value a row var may hold: a
// Row{...} composite literal, or a call to a package-level func (found in
// rowFuncs) whose sole result type is Row. rowFuncs must come from a prior
// pass over the same files, since shape 6 (`var fooRow = newRow(...)`) lets
// the func live in a different file than the var.
func isRowValueExpr(expr ast.Expr, rowFuncs map[string]bool) bool {
	switch v := expr.(type) {
	case *ast.CompositeLit:
		return isRowType(v.Type)
	case *ast.CallExpr:
		id, ok := v.Fun.(*ast.Ident)
		return ok && rowFuncs[id.Name]
	default:
		return false
	}
}

// collectRowFuncs returns the names of every package-level func across
// files whose signature returns exactly one Row. Methods (non-nil Recv) are
// excluded: a row var is never assigned from a method call without a
// receiver expression, which this syntactic, no-type-checking scan has no
// way to resolve anyway.
func collectRowFuncs(files []*ast.File) map[string]bool {
	rowFuncs := make(map[string]bool)
	for _, file := range files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || fd.Type.Results == nil {
				continue
			}
			results := fd.Type.Results
			if results.NumFields() == 1 && len(results.List) == 1 && isRowType(results.List[0].Type) {
				rowFuncs[fd.Name.Name] = true
			}
		}
	}
	return rowFuncs
}

// scanForUntabledRows parses every non-_test.go .go file directly in dir --
// unlike scanForEcosystemLiterals, it does not recurse into subdirectories,
// because a package-level var and the Table literal that must list it
// always live in the same package directory; walking into a subpackage
// would only ever compare unrelated declarations -- and returns, sorted,
// the name of every package-level row var whose identifier does not appear
// as a top-level element of the package's `var Table = []Row{...}`.
//
// A var counts as a row var in any of these shapes, singly or grouped
// inside `var ( ... )`:
//
//  1. var fooRow = Row{...}
//  2. var fooRow Row                       (assigned later, e.g. in init())
//  3. var fooRow Row = Row{...}
//  4. var aRow, bRow = Row{...}, Row{...}  (paired by position)
//  5. var aRow, bRow Row
//  6. var fooRow = newRow(...)             (newRow's sole result is Row)
//
// The scan is purely syntactic: it never imports or evaluates this
// package's own Table value, so the check works even mid-refactor, when the
// package may not build.
//
// Only top-level file.Decls are inspected, so a Row-shaped composite
// literal declared inside a function body -- which never reaches
// file.Decls -- is never mistaken for a row awaiting a Table entry.
//
// If dir does not exist, or no `var Table = []Row{...}` declaration is
// found in any file, this fails the test loudly (t.Fatalf): a renamed
// Table must not silently drop coverage, matching scanForEcosystemLiterals's
// own posture.
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

	// rowFuncs needs every file read up front (shape 6's func may live in a
	// file other than the var), so this pass runs before the var pass below
	// rather than interleaved with it.
	rowFuncs := collectRowFuncs(files)

	rowVars := make(map[string]bool)
	tableIdents := make(map[string]bool)
	tableFound := false

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

				if len(vs.Names) == 1 && len(vs.Values) == 1 && vs.Names[0].Name == "Table" {
					if lit, ok := vs.Values[0].(*ast.CompositeLit); ok && isRowSliceType(lit.Type) {
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
					// Typed, uninitialized: var fooRow Row / var aRow, bRow Row.
					if isRowType(vs.Type) {
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
		t.Fatalf("scanForUntabledRows: no `var Table = []Row{...}` declaration found in %s", dir)
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

// TestScanForUntabledRows_FixtureDetectsViolation is the durable proof that
// a row var missing from Table trips the check, across every shape
// scanForUntabledRows recognizes. The fixture spreads across two files
// (rows.go, funcs.go) to prove shape 6's func-lookup pass sees a func
// declared in a file other than the var that calls it. For each shape, a
// "tagged" var (listed in Table) and an "untabled" var (the violation) are
// declared side by side, so a shape that stopped being detected would leave
// its untabled var out of the result rather than passing silently. The
// fixture also carries the shapes that must never be counted: an unrelated
// struct type, a call to a func whose result isn't Row, and a Row literal
// built inside a function body.
func TestScanForUntabledRows_FixtureDetectsViolation(t *testing.T) {
	dir := t.TempDir()

	writeFixture(t, dir, "rows.go", `package fixture

type Row struct {
	Name string
}

type OtherThing struct {
	Name string
}

// shape 1: var fooRow = Row{...}
var taggedSimpleRow = Row{Name: "tagged simple"}
var untabledSimpleRow = Row{Name: "untabled simple"}

// shape 2: var fooRow Row (assigned in init(), not at the declaration)
var taggedNoValueRow Row
var untabledNoValueRow Row

func init() {
	taggedNoValueRow = Row{Name: "tagged no value"}
	untabledNoValueRow = Row{Name: "untabled no value"}
}

// shape 3: var fooRow Row = Row{...}
var taggedTypedRow Row = Row{Name: "tagged typed"}
var untabledTypedRow Row = Row{Name: "untabled typed"}

// shape 4: var aRow, bRow = Row{...}, Row{...}
var taggedMultiA, taggedMultiB = Row{Name: "tagged multi a"}, Row{Name: "tagged multi b"}
var untabledMultiA, untabledMultiB = Row{Name: "untabled multi a"}, Row{Name: "untabled multi b"}

// shape 5: var aRow, bRow Row
var taggedMultiNoValueA, taggedMultiNoValueB Row
var untabledMultiNoValueA, untabledMultiNoValueB Row

// shape 6: var fooRow = newRow(...), newRow declared in funcs.go
var taggedFromFuncRow = newTaggedRow()
var untabledFromFuncRow = newUntabledRow()

// shapes 1-3, grouped inside var ( ... )
var (
	taggedGroupedRow          = Row{Name: "tagged grouped"}
	untabledGroupedRow        = Row{Name: "untabled grouped"}
	taggedGroupedNoValueRow   Row
	untabledGroupedNoValueRow Row
	taggedGroupedTypedRow     Row = Row{Name: "tagged grouped typed"}
	untabledGroupedTypedRow   Row = Row{Name: "untabled grouped typed"}
)

// must never be counted as a row var
var otherThingNotARow = OtherThing{Name: "not a row"}
var otherThingFromFuncNotARow = newOtherThing()

func buildLocalRow() Row {
	localRow := Row{Name: "local, inside a function body"}
	return localRow
}

var Table = []Row{
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

	writeFixture(t, dir, "funcs.go", `package fixture

func newTaggedRow() Row {
	return Row{Name: "tagged from func"}
}

func newUntabledRow() Row {
	return Row{Name: "untabled from func"}
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

// TestEveryDeclaredRowIsInTheTable enforces that a package-level `var
// fooRow = Row{...}` declaration is always listed in Table -- the parity
// half of containment_test.go's ecosystem-name check.
func TestEveryDeclaredRowIsInTheTable(t *testing.T) {
	missing := scanForUntabledRows(t, ".")
	if len(missing) > 0 {
		t.Errorf("row var(s) declared but missing from Table: %s", strings.Join(missing, ", "))
	}
}
