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
	// CreateMeta is not paired: its Fields element type is a differently named
	// copy, so the rendered lines cannot match.
	"CreateField": "wireCreateField",

	"TransitionRequest": "transitionRequest",
	"TransitionResult":  "transitionResult",
	"StatusTarget":      "StatusTarget",
}

// fieldLines renders each field of st as "name type tag"; an embedded field
// renders as "=Type" so flatten can splice its fields in.
func fieldLines(t *testing.T, fset *token.FileSet, st *ast.StructType) []string {
	t.Helper()
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
		if len(fld.Names) == 0 {
			lines = append(lines, "="+typ)
		}
		for _, n := range fld.Names {
			lines = append(lines, fmt.Sprintf("%s %s %s", n.Name, typ, tag))
		}
	}
	return lines
}

// structLines renders each field of every named struct in the file.
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
			if st, ok := ts.Type.(*ast.StructType); ok {
				out[ts.Name.Name] = fieldLines(t, fset, st)
			}
		}
	}
	return out
}

// flatten splices each embedded "=Type" line for the fields of that struct in
// all, as encoding/json flattens an embedded struct.
func flatten(t *testing.T, lines []string, all map[string][]string) []string {
	t.Helper()
	var out []string
	for _, l := range lines {
		name, ok := strings.CutPrefix(l, "=")
		if !ok {
			out = append(out, l)
			continue
		}
		inner, ok := all[name]
		if !ok {
			t.Fatalf("embedded type %s not found", name)
		}
		out = append(out, flatten(t, inner, all)...)
	}
	return out
}

// methodConsts resolves the method consts the handler map is keyed by.
var methodConsts = map[string]string{
	"CapTransition": CapTransition, "CapEditLabels": CapEditLabels,
	"CapListLabels": CapListLabels, "CapCreateLabel": CapCreateLabel,
}

// handlerParams renders the anonymous `var p struct{...}` each method handler
// in the sdk's writes.go decodes its params into, keyed by method name.
func handlerParams(t *testing.T, path string) map[string][]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return true
		}
		ix, ok := as.Lhs[0].(*ast.IndexExpr)
		if !ok {
			return true
		}
		fn, isFn := as.Rhs[0].(*ast.FuncLit)
		var method string
		switch k := ix.Index.(type) {
		case *ast.BasicLit:
			method = strings.Trim(k.Value, `"`)
		case *ast.Ident: // the handlers key by the Cap* consts
			method = methodConsts[k.Name]
		}
		if method == "" || !isFn {
			return true
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			vs, ok := n.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "p" {
				return true
			}
			if st, ok := vs.Type.(*ast.StructType); ok {
				out[method] = fieldLines(t, fset, st)
			}
			return false
		})
		return true
	})
	return out
}

func TestWireShapesMatchCore(t *testing.T) {
	mine := structLines(t, "tracker.go")
	for name, lines := range structLines(t, "writes.go") {
		if _, dup := mine[name]; dup {
			t.Fatalf("type %s declared in both tracker.go and writes.go", name)
		}
		mine[name] = lines
	}
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

// TestWriteCapabilitiesMatchCore pins the write capability names against
// core's named consts, which gate the write layers on the handshake.
func TestWriteCapabilitiesMatchCore(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), coreFile, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	core := map[string]string{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, n := range vs.Names {
				if i >= len(vs.Values) {
					break // an iota or typed const with no literal
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					core[n.Name] = strings.Trim(lit.Value, `"`)
				}
			}
		}
	}
	for name, want := range map[string]string{
		"CapTransition": CapTransition, "CapEditLabels": CapEditLabels, "CapLabels": CapLabels,
		"CapListLabels": CapListLabels, "CapCreateLabel": CapCreateLabel,
		"transitionMoved": TransitionMoved, "transitionUnavailable": TransitionUnavailable,
		"transitionUnmapped": TransitionUnmapped,
	} {
		if got, ok := core[name]; !ok || got != want {
			t.Errorf("core %s = %q (declared: %v), sdk says %q", name, got, ok, want)
		}
	}
}

// TestWriteParamsMatchCore pins core's params envelopes for the write methods
// against the structs the sdk's handlers decode them into, which are anonymous
// and so cannot be paired by name.
func TestWriteParamsMatchCore(t *testing.T) {
	sdk := handlerParams(t, "writes.go")
	own := structLines(t, "writes.go")
	core := structLines(t, coreFile)
	for method, name := range map[string]string{
		"tracker.transition":  "transitionParams",
		"tracker.editLabels":  "editLabelsParams",
		"tracker.createLabel": "createLabelParams",
	} {
		want, ok := sdk[method]
		if !ok || len(want) == 0 {
			t.Fatalf("sdk handler for %s not found: parser is vacuous", method)
		}
		lines, ok := core[name]
		if !ok {
			t.Errorf("core lacks %s, the params of %s", name, method)
			continue
		}
		got := flatten(t, lines, core)
		want = flatten(t, want, own)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("core %s drifted from the sdk's %s params\n core: %q\n sdk:  %q", name, method, got, want)
		}
	}
}
