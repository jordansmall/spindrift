package forge

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
const coreFile = "../../cmd/launcher/internal/connectorforge/connectorforge.go"

// corePushOnlyFile holds core's capability-name consts, copied by the same nix check.
const corePushOnlyFile = "../../cmd/launcher/internal/connectorforge/pushonly.go"

// pairs maps each sdk type to core's copy of it. The sdk's params and result
// envelopes are anonymous structs inside handlers, so only the named shapes
// can be compared.
var pairs = map[string]string{
	"Target": "Target",
	"Remote": "wireRemote",
	"PR":     "wirePR",
	"Check":  "wireCheck",
	"Rollup": "wireRollup",
	"Grant":  "wireGrant",
}

// structLines renders each field of every named struct in the file as
// "name type tag". rename maps type names appearing in field types, so core's
// wireCheck compares equal to the sdk's Check.
func structLines(t *testing.T, path string, rename map[string]string) map[string][]string {
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
				for from, to := range rename {
					typ = strings.ReplaceAll(typ, from, to)
				}
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
	// Core names its copy of a nested type differently (Rollup.Checks is
	// []Check here, []wireCheck there), so map core's names back to the sdk's.
	rename := map[string]string{}
	for sdk, name := range pairs {
		if sdk != name {
			rename[name] = sdk
		}
	}
	mine := structLines(t, "forge.go", nil)
	for _, file := range []string{"branches.go", "prs.go"} {
		for name, lines := range structLines(t, file, nil) {
			mine[name] = lines
		}
	}
	core := structLines(t, coreFile, rename)
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

// TestCapabilitiesMatchCore pins the capability names against core's named
// consts, which gate Compose on the handshake.
func TestCapabilitiesMatchCore(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), corePushOnlyFile, nil, 0)
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
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					core[n.Name] = strings.Trim(lit.Value, `"`)
				}
			}
		}
	}
	for name, want := range map[string]string{
		"capBranches": CapBranches, "capPushOnly": CapPushOnly, "capPRs": CapPRs,
		"capAutoMerge": CapAutoMerge, "capCIRun": CapCIRun,
		"capBranchProtection": CapBranchProtection, "capProbe": CapProbe,
		"capDraftPRs": CapDraftPRs, "CapTokenGrant": CapTokenGrant,
	} {
		if got, ok := core[name]; !ok || got != want {
			t.Errorf("core %s = %q (declared: %v), sdk says %q", name, got, ok, want)
		}
	}
}
