package connectorforge_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorprotocol"
)

// coreForgeNames returns the capability names (the string consts of this
// package) and the method names (every other exact "forge.<name>" literal) in
// the package's non-test sources. Fixtures live in connectorforgetest, which
// this walk does not enter.
func coreForgeNames(t *testing.T) (caps, methods []string) {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	name := regexp.MustCompile(`^forge(\.[A-Za-z]+)?$`)
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, file, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		declared := map[*ast.BasicLit]bool{}
		for _, d := range f.Decls {
			if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.CONST {
				for _, sp := range gd.Specs {
					for _, v := range sp.(*ast.ValueSpec).Values {
						if bl, ok := v.(*ast.BasicLit); ok && bl.Kind == token.STRING {
							declared[bl] = true
						}
					}
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			bl, ok := n.(*ast.BasicLit)
			if !ok || bl.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(bl.Value)
			if err != nil || !name.MatchString(s) {
				return true
			}
			into := &methods
			if declared[bl] {
				into = &caps
			}
			if !slices.Contains(*into, s) {
				*into = append(*into, s)
			}
			return true
		})
	}
	slices.Sort(caps)
	slices.Sort(methods)
	return caps, methods
}

func TestProtocolForgeAxisAgrees(t *testing.T) {
	coreCaps, coreMethods := coreForgeNames(t)
	var methods, caps []string
	for name, m := range connectorprotocol.Methods {
		if m.Axis == connectorprotocol.AxisForge {
			methods = append(methods, name)
		}
	}
	for name, axis := range connectorprotocol.Capabilities {
		if axis == connectorprotocol.AxisForge {
			caps = append(caps, name)
		}
	}
	slices.Sort(methods)
	slices.Sort(caps)
	// forge.remote stays declared for Connectors built against it, but core no
	// longer calls it. It leaves the description at protocol 0.4.0: drop this
	// filter and the CheckRemoteURLs body in checks.go then.
	methods = slices.DeleteFunc(methods, func(m string) bool { return m == "forge.remote" })
	if !slices.Equal(methods, coreMethods) {
		t.Errorf("the description declares forge methods %v, connectorforge calls %v", methods, coreMethods)
	}
	if !slices.Equal(caps, coreCaps) {
		t.Errorf("the description declares forge capabilities %v, connectorforge names %v", caps, coreCaps)
	}
	for _, name := range methods {
		m := connectorprotocol.Methods[name]
		if m.Capability == "" || connectorprotocol.Capabilities[m.Capability] != connectorprotocol.AxisForge {
			t.Errorf("method %s names capability %q, which is not a forge capability", name, m.Capability)
		}
	}
}
