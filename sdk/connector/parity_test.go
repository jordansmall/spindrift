package connector

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"go/types"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// hostSrc is the host side of the http_request wire contract. The nix check
// copies it beside sdk/ at the same relative layout so this path resolves there too.
const hostSrc = "../../cmd/launcher/internal/connectorhost/network.go"

func parseHost(t *testing.T) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, hostSrc, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	return fset, f
}

func TestMaxHTTPResponseMatchesHost(t *testing.T) {
	fset, f := parseHost(t)
	var expr ast.Expr
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, n := range vs.Names {
				if n.Name == "maxHTTPResponse" && i < len(vs.Values) {
					expr = vs.Values[i]
				}
			}
		}
	}
	if expr == nil {
		t.Fatal("host declares no const maxHTTPResponse: parser is vacuous")
	}
	var src strings.Builder
	if err := printer.Fprint(&src, fset, expr); err != nil {
		t.Fatal(err)
	}
	tv, err := types.Eval(token.NewFileSet(), nil, token.NoPos, src.String())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := tv.Value.ExactString(), strconv.Itoa(MaxHTTPResponse); got != want {
		t.Errorf("MaxHTTPResponse = %s, host maxHTTPResponse = %s", want, got)
	}
}

func TestRequestMatchesHost(t *testing.T) {
	fset, f := parseHost(t)
	var st *ast.StructType
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			if ts := spec.(*ast.TypeSpec); ts.Name.Name == "httpRequest" {
				st, _ = ts.Type.(*ast.StructType)
			}
		}
	}
	if st == nil {
		t.Fatal("host declares no struct httpRequest: parser is vacuous")
	}
	var host []string
	for _, fld := range st.Fields.List {
		var typ strings.Builder
		if err := printer.Fprint(&typ, fset, fld.Type); err != nil {
			t.Fatal(err)
		}
		tag := ""
		if fld.Tag != nil {
			var err error
			if tag, err = strconv.Unquote(fld.Tag.Value); err != nil {
				t.Fatal(err)
			}
		}
		for _, n := range fld.Names {
			host = append(host, fmt.Sprintf("%s %s %q", n.Name, typ.String(), tag))
		}
	}
	var sdk []string
	rt := reflect.TypeOf(Request{})
	for i := range rt.NumField() {
		fld := rt.Field(i)
		sdk = append(sdk, fmt.Sprintf("%s %s %q", fld.Name, fld.Type, string(fld.Tag)))
	}
	if !reflect.DeepEqual(sdk, host) {
		t.Errorf("Request drifted from host httpRequest\n sdk:  %q\n host: %q", sdk, host)
	}
}
