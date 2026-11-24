// These tests pin the SDK's twins of driverkit code by source identity. They
// still read core, unlike the rest of the SDK's former parity tests: those
// pinned declared facts, which lib/connector-protocol now renders into the SDK
// and core's agreement tests hold core to. A helper's behaviour cannot be
// rendered from the description, and a Connector cannot import driverkit
// (issue #6870), so identical source is what makes a twin answer as core does.
package driver

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"strings"
	"testing"
)

// coreDir is the canonical vocabulary (ADR 0065). The nix check copies it
// beside sdk/ at the same relative layout so this path resolves there too.
const coreDir = "../../cmd/launcher/internal/driver/driverkit"

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
	return methodSource(t, dir, "", name)
}

// methodSource is funcSource for the method name on receiver type recv
// (pointer or value); an empty recv selects a plain func.
func methodSource(t *testing.T, dir, recv, name string) string {
	t.Helper()
	fset, files := parseDir(t, dir)
	for _, f := range files {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Name.Name != name || recvName(fn) != recv {
				continue
			}
			fn.Doc = nil
			return render(t, fset, fn)
		}
	}
	return ""
}

// recvName returns fn's receiver type name, "" for a plain func.
func recvName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	typ := fn.Recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	if id, ok := typ.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// TestHelperParity fails when an SDK helper's source drifts from its driverkit
// original. A Connector cannot import driverkit (issue #6870), so the SDK
// carries twins; identical source means identical output for every input. That
// holds for ErrorText and SessionUUID only because their callees CompactJSON
// and SessionKey are pinned here too. No twin for BaseTransientPatterns (host
// Classify appends them via driverkit.MatchTransient) or TrimNarration (only
// the host heartbeat writer). LineFramer's twin is pinned by
// TestLineFramerParity.
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

// typeSource returns the normalised source of the type name
// declared in dir's non-test files, "" when there is none.
func typeSource(t *testing.T, dir, name string) string {
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
				if ts.Name.Name == name {
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
	core := typeSource(t, coreDir, "StreamFilter")
	if core == "" {
		t.Fatal("core has no type StreamFilter")
	}
	if !strings.HasPrefix(core, "interface {") {
		t.Fatalf("core StreamFilter is not an interface: %s", core)
	}
	if sdk := typeSource(t, ".", "StreamFilter"); sdk != core {
		t.Errorf("SDK StreamFilter differs from core's:\nSDK:  %s\ncore: %s", sdk, core)
	}
}

// TestSessionFlagParity fails when the SDK's SessionFlag drifts from
// driverkit's: the host decodes a Connector's driver.sessionFlags answer
// straight into driverkit's type, so a renamed field or JSON tag drops the
// session pin silently.
func TestSessionFlagParity(t *testing.T) {
	core := typeSource(t, coreDir, "SessionFlag")
	if core == "" {
		t.Fatal("core has no type SessionFlag")
	}
	if sdk := typeSource(t, ".", "SessionFlag"); sdk != core {
		t.Errorf("SDK SessionFlag differs from core's:\nSDK:  %s\ncore: %s", sdk, core)
	}
}

// TestLineFramerParity fails when the SDK's LineFramer drifts from
// driverkit's: pi's stream filter crosses the Connector protocol, so its
// framing must match core's. The struct type and each method are compared on
// source, like the funcs in TestHelperParity.
func TestLineFramerParity(t *testing.T) {
	core := typeSource(t, coreDir, "LineFramer")
	if core == "" {
		t.Fatal("core has no type LineFramer")
	}
	if sdk := typeSource(t, ".", "LineFramer"); sdk != core {
		t.Errorf("SDK LineFramer differs from core's:\nSDK:  %s\ncore: %s", sdk, core)
	}
	for _, name := range []string{"Push", "Drain"} {
		t.Run(name, func(t *testing.T) {
			core := methodSource(t, coreDir, "LineFramer", name)
			if core == "" {
				t.Fatalf("core has no method LineFramer.%s", name)
			}
			if sdk := methodSource(t, ".", "LineFramer", name); sdk != core {
				t.Errorf("SDK LineFramer.%s differs from core's:\nSDK:  %s\ncore: %s", name, sdk, core)
			}
		})
	}
}
