package driver

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// coreDir is the canonical vocabulary (ADR 0065). The nix check copies it
// beside sdk/ at the same relative layout so this path resolves there too.
const coreDir = "../../cmd/launcher/internal/driver/driverkit"

// reasonValues returns the string values of every const of type Reason
// declared in dir's non-test files.
func reasonValues(t *testing.T, dir string) []string {
	t.Helper()
	pkgs, err := parser.ParseDir(token.NewFileSet(), dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var vals []string
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, d := range file.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok || gd.Tok != token.CONST {
					continue
				}
				for _, spec := range gd.Specs {
					s := spec.(*ast.ValueSpec)
					if id, ok := s.Type.(*ast.Ident); !ok || id.Name != "Reason" {
						continue
					}
					for _, v := range s.Values {
						lit, ok := v.(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING {
							t.Fatalf("Reason const is not a string literal: %T", v)
						}
						u, err := strconv.Unquote(lit.Value)
						if err != nil {
							t.Fatal(err)
						}
						vals = append(vals, u)
					}
				}
			}
		}
	}
	sort.Strings(vals)
	return vals
}

// TestReasonParity fails when the SDK's Reason constants drift from core's
// driverkit.AllReasons: a Strategy's Reason crosses the Connector call
// protocol as a string, so a value only the SDK knows makes the host refuse
// the Driver at DriverStrategy, and a core-only one cannot be written by an
// SDK author.
func TestReasonParity(t *testing.T) {
	core := reasonValues(t, coreDir)
	if len(core) == 0 {
		t.Fatal("found no Reason constants in core")
	}
	sdk := reasonValues(t, ".")
	if !reflect.DeepEqual(sdk, core) {
		t.Errorf("SDK Reasons %v differ from core's %v", sdk, core)
	}
}
