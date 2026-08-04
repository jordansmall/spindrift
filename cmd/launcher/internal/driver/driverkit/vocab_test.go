package driverkit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"testing"
)

func TestClassValues(t *testing.T) {
	if string(Transient) != "transient" {
		t.Errorf("Transient = %q, want %q", string(Transient), "transient")
	}
	if string(Terminal) != "terminal" {
		t.Errorf("Terminal = %q, want %q", string(Terminal), "terminal")
	}
}

func TestReasonValues(t *testing.T) {
	cases := []struct {
		got  Reason
		want string
	}{
		{RateLimit, "rateLimit"},
		{Overloaded, "overloaded"},
		{Network, "network"},
		{TaskFailed, "taskFailed"},
		{UnsupportedFlag, "unsupportedFlag"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("got %q, want %q", string(c.got), c.want)
		}
	}
}

// TestAllReasonsCoversDeclaredReasonConsts parses vocab.go's own source via
// go/ast and asserts every Reason-typed const declared there appears in the
// AllReasons composite literal — also parsed from source, not imported — so
// a future Reason const added without a matching AllReasons entry fails this
// test instead of silently vanishing from the completeness-pinned slice
// (issue #2301, pinning the exhaustiveness issue #2269 depends on).
func TestAllReasonsCoversDeclaredReasonConsts(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "vocab.go", nil, 0)
	if err != nil {
		t.Fatalf("parse vocab.go: %v", err)
	}

	var declaredReasons []string
	var allReasonsElems []string

	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		switch genDecl.Tok {
		case token.CONST:
			for _, spec := range genDecl.Specs {
				valueSpec, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				ident, ok := valueSpec.Type.(*ast.Ident)
				if !ok || ident.Name != "Reason" {
					continue
				}
				for _, name := range valueSpec.Names {
					declaredReasons = append(declaredReasons, name.Name)
				}
			}
		case token.VAR:
			for _, spec := range genDecl.Specs {
				valueSpec, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range valueSpec.Names {
					if name.Name != "AllReasons" {
						continue
					}
					if i >= len(valueSpec.Values) {
						continue
					}
					composite, ok := valueSpec.Values[i].(*ast.CompositeLit)
					if !ok {
						continue
					}
					for _, elt := range composite.Elts {
						eltIdent, ok := elt.(*ast.Ident)
						if !ok {
							continue
						}
						allReasonsElems = append(allReasonsElems, eltIdent.Name)
					}
				}
			}
		}
	}

	if len(declaredReasons) == 0 {
		t.Fatal("found no Reason-typed const declarations in vocab.go; test may be broken")
	}
	if len(allReasonsElems) == 0 {
		t.Fatal("found no elements in AllReasons composite literal in vocab.go; test may be broken")
	}

	allReasonsSet := make(map[string]bool, len(allReasonsElems))
	for _, name := range allReasonsElems {
		allReasonsSet[name] = true
	}

	var missing []string
	for _, name := range declaredReasons {
		if !allReasonsSet[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("Reason const(s) declared but missing from AllReasons: %v", missing)
	}
}
