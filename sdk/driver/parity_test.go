package driver

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
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

const (
	dispatchkindDir = "../../cmd/launcher/internal/dispatchkind"
	hostopDir       = "../../cmd/launcher/internal/hostop"
)

// parseDir parses dir's non-test files, returning them with their FileSet.
func parseDir(t *testing.T, dir string) (*token.FileSet, []*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			files = append(files, f)
		}
	}
	return fset, files
}

// parseNonTest parses dir's non-test files.
func parseNonTest(t *testing.T, dir string) []*ast.File {
	t.Helper()
	_, files := parseDir(t, dir)
	return files
}

// stringConsts returns every untyped-or-typed string const in files that is
// initialised with a string literal, by name.
func stringConsts(t *testing.T, files []*ast.File) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, f := range files {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				s := spec.(*ast.ValueSpec)
				for i, v := range s.Values {
					if lit, ok := v.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						u, err := strconv.Unquote(lit.Value)
						if err != nil {
							t.Fatal(err)
						}
						out[s.Names[i].Name] = u
					}
				}
			}
		}
	}
	return out
}

// passRolesTable reads the map literal returned by core's passRoles func,
// resolving each value identifier through roles.
func passRolesTable(t *testing.T, files []*ast.File, roles map[string]string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, f := range files {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "passRoles" {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				cl, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				for _, e := range cl.Elts {
					kv := e.(*ast.KeyValueExpr)
					k, err := strconv.Unquote(kv.Key.(*ast.BasicLit).Value)
					if err != nil {
						t.Fatal(err)
					}
					out[k] = roles[kv.Value.(*ast.Ident).Name]
				}
				return false
			})
		}
	}
	return out
}

// TestHostLineParity fails when the SDK's host-line grammar, role constants or
// pass-role table drift from core's: HostLine mirrors them because the SDK
// cannot import core-internal packages.
func TestHostLineParity(t *testing.T) {
	kit := stringConsts(t, parseNonTest(t, coreDir))
	dk := stringConsts(t, parseNonTest(t, dispatchkindDir))
	ho := stringConsts(t, parseNonTest(t, hostopDir))
	for name, pair := range map[string][2]string{
		"hostop.TypeSpindriftOp":       {ho["TypeSpindriftOp"], "spindrift_op"},
		"hostop.OpPassStart":           {ho["OpPassStart"], "pass_start"},
		"dispatchkind.ImplementorRole": {dk["ImplementorRole"], ImplementorRole},
		"dispatchkind.ReviewerRole":    {dk["ReviewerRole"], ReviewerRole},
		"driverkit.DefaultRole":        {kit["DefaultRole"], DefaultRole},
	} {
		if pair[0] == "" || pair[0] != pair[1] {
			t.Errorf("%s = %q, SDK has %q", name, pair[0], pair[1])
		}
	}
	core := passRolesTable(t, parseNonTest(t, dispatchkindDir), dk)
	if len(core) == 0 {
		t.Fatal("found no pass-role table in core")
	}
	if !reflect.DeepEqual(passRoles, core) {
		t.Errorf("SDK pass roles %v differ from core's %v", passRoles, core)
	}
}

func render(t *testing.T, fset *token.FileSet, n any) string {
	t.Helper()
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, n); err != nil {
		t.Fatal(err)
	}
	return strings.Join(strings.Fields(buf.String()), " ")
}

// funcSource returns the normalised source of the top-level func name (no
// receiver) declared in dir's non-test files, "" when there is none.
func funcSource(t *testing.T, dir, name string) string {
	t.Helper()
	fset, files := parseDir(t, dir)
	for _, f := range files {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name.Name != name {
				continue
			}
			fn.Doc = nil
			return render(t, fset, fn)
		}
	}
	return ""
}

// TestHelperParity fails when an SDK helper's source drifts from its driverkit
// original. A Connector cannot import driverkit (issue #6870), so the SDK
// carries twins; identical source means identical output for every input. That
// holds for ErrorText and SessionUUID only because their callees CompactJSON
// and SessionKey are pinned here too. No twin for BaseTransientPatterns (host
// Classify appends them via driverkit.MatchTransient), LineFramer (only pi's
// stream filter, which does not cross the Connector protocol) or TrimNarration
// (only the host heartbeat writer).
func TestHelperParity(t *testing.T) {
	for _, name := range []string{"ToolTarget", "ErrorText", "CompactJSON", "SessionKey", "SessionUUID"} {
		t.Run(name, func(t *testing.T) {
			core := funcSource(t, coreDir, name)
			if core == "" {
				t.Fatalf("core has no func %s", name)
			}
			if sdk := funcSource(t, ".", name); sdk != core {
				t.Errorf("SDK %s differs from core's:\nSDK:  %s\ncore: %s", name, sdk, core)
			}
		})
	}
}

// ifaceSource returns the normalised source of the interface type name
// declared in dir's non-test files, "" when there is none.
func ifaceSource(t *testing.T, dir, name string) string {
	t.Helper()
	fset, files := parseDir(t, dir)
	for _, f := range files {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts := spec.(*ast.TypeSpec)
				if _, ok := ts.Type.(*ast.InterfaceType); ok && ts.Name.Name == name {
					return render(t, fset, ts.Type)
				}
			}
		}
	}
	return ""
}

// TestStreamFilterParity fails when the SDK's StreamFilter drifts from
// driverkit's: the host wraps a Connector-backed filter in the driverkit
// interface, so a method one side lacks breaks that adapter.
func TestStreamFilterParity(t *testing.T) {
	core := ifaceSource(t, coreDir, "StreamFilter")
	if core == "" {
		t.Fatal("core has no interface StreamFilter")
	}
	if sdk := ifaceSource(t, ".", "StreamFilter"); sdk != core {
		t.Errorf("SDK StreamFilter differs from core's:\nSDK:  %s\ncore: %s", sdk, core)
	}
}
