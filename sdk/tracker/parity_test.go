package tracker

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"reflect"
	"strings"
	"testing"
)

// coreFile holds core's copies of the wire shapes. The nix check copies it
// beside sdk/ at the same relative layout so this path resolves there too.
const coreFile = "../../cmd/launcher/internal/connectortracker/connectortracker.go"

// pairs maps each sdk type to core's copy of it. The sdk's params and result
// envelopes are anonymous structs inside handlers, so only the named shapes
// can be compared.
var pairs = map[string]string{
	"Target":  "Target",
	"Issue":   "wireIssue",
	"Comment": "wireComment",
}

// structLines renders each field of every named struct in the file as
// "name type tag".
func structLines(t *testing.T, path string) map[string][]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0) // mode 0: comments dropped
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts := spec.(*ast.TypeSpec)
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			var lines []string
			for _, fld := range st.Fields.List {
				var b bytes.Buffer
				if err := printer.Fprint(&b, fset, fld.Type); err != nil {
					t.Fatal(err)
				}
				typ := strings.Join(strings.Fields(b.String()), " ")
				tag := ""
				if fld.Tag != nil {
					tag = fld.Tag.Value
				}
				for _, n := range fld.Names {
					lines = append(lines, fmt.Sprintf("%s %s %s", n.Name, typ, tag))
				}
			}
			out[ts.Name.Name] = lines
		}
	}
	return out
}

func TestWireShapesMatchCore(t *testing.T) {
	mine := structLines(t, "tracker.go")
	core := structLines(t, coreFile)
	for sdk, name := range pairs {
		want, ok := mine[sdk]
		if !ok || len(want) == 0 {
			t.Fatalf("sdk type %s not found: parser is vacuous", sdk)
		}
		got, ok := core[name]
		if !ok {
			t.Errorf("core lacks %s, the copy of %s", name, sdk)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("core %s drifted from sdk %s\n core: %q\n sdk:  %q", name, sdk, got, want)
		}
	}
}
