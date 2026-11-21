package pi_test

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// connectorDir is the pi Connector's source, which carries twins of decode.go,
// streamfilter.go and patterns.go. The nix checks copy it to this repo-relative path.
const connectorDir = "../../../../../connectors/pi"

// nativeOnlyDecls are the native declarations the Connector has no twin of.
var nativeOnlyDecls = map[string]bool{
	// The Connector's host never decodes a log: its decoder is built per scan.
	"func Decode": true,
}

// ignoredParams are the funcs whose parameter list differs by design: the SDK's
// Decoder constructor takes the Pass's start time, core's does not.
var ignoredParams = map[string]bool{"func NewDecoder": true}

// qualifiers are the package names core and the SDK each give the shared
// vocabulary; they are folded to one so only the declaration differs.
var qualifiers = map[string]bool{"driverkit": true, "driver": true, "event": true}

// decls maps each top-level non-import declaration in dir/file to its source,
// stripped of comments and whitespace and with qualifiers folded.
func decls(t *testing.T, dir, file string) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(dir, file), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	add := func(key string, n ast.Node) {
		if _, dup := out[key]; dup {
			t.Fatalf("%s: %s declared twice", file, key)
		}
		ast.Inspect(n, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && qualifiers[id.Name] {
					id.Name = "pkg"
				}
			}
			return true
		})
		out[key] = strings.Join(strings.Fields(render(t, fset, n)), " ")
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			key := "func " + d.Name.Name
			if d.Recv != nil {
				key = "func (" + render(t, fset, d.Recv.List[0].Type) + ") " + d.Name.Name
			}
			if ignoredParams[key] {
				d.Type.Params = nil
			}
			add(key, d)
		case *ast.GenDecl:
			if d.Tok == token.IMPORT {
				continue
			}
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					add("type "+s.Name.Name, s)
				case *ast.ValueSpec:
					names := make([]string, len(s.Names))
					for i, n := range s.Names {
						names[i] = n.Name
					}
					add(d.Tok.String()+" "+strings.Join(names, ","), s)
				}
			}
		}
	}
	return out
}

func render(t *testing.T, fset *token.FileSet, n ast.Node) string {
	t.Helper()
	var b strings.Builder
	if err := printer.Fprint(&b, fset, n); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestConnectorSourceParity pins the Connector's decode.go, streamfilter.go and
// patterns.go to the native Driver's: the two are copies, and a copy the goldens do not
// reach (the native-only fallback and property tests) would drift unseen.
// Imports, the event package qualifier, the NewDecoder signature and the
// native-only Decode are the known differences. A declaration present on one
// side only fails, as does any other source difference.
func TestConnectorSourceParity(t *testing.T) {
	for _, file := range []string{"decode.go", "streamfilter.go", "patterns.go"} {
		t.Run(file, func(t *testing.T) {
			native := decls(t, ".", file)
			connector := decls(t, connectorDir, file)
			for key := range nativeOnlyDecls {
				delete(native, key)
			}
			if len(native) == 0 {
				t.Fatal("found no native declarations")
			}
			for _, key := range slices.Sorted(maps.Keys(native)) {
				c, ok := connector[key]
				if !ok {
					t.Errorf("connector has no %s", key)
					continue
				}
				if c != native[key] {
					t.Errorf("%s differs:\nconnector: %s\nnative:    %s", key, c, native[key])
				}
			}
			for _, key := range slices.Sorted(maps.Keys(connector)) {
				if _, ok := native[key]; !ok {
					t.Errorf("native has no %s", key)
				}
			}
		})
	}
}
