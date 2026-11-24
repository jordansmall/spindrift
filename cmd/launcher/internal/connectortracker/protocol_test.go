package connectortracker_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"go/ast"

	"spindrift.dev/launcher/internal/connectorprotocol"
	"spindrift.dev/launcher/internal/connectortracker"
)

// coreTrackerNames returns every exact "tracker[.suffix]" string literal in the
// package's non-test sources: the capability names and the method names core
// sends, which share one namespace (a method is sometimes its own capability,
// sometimes rides on one).
func coreTrackerNames(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	name := regexp.MustCompile(`^tracker(\.[A-Za-z]+)*$`)
	fset := token.NewFileSet()
	var names []string
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
		ast.Inspect(f, func(n ast.Node) bool {
			bl, ok := n.(*ast.BasicLit)
			if !ok || bl.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(bl.Value)
			if err == nil && name.MatchString(s) && !slices.Contains(names, s) {
				names = append(names, s)
			}
			return true
		})
	}
	slices.Sort(names)
	return names
}

func TestProtocolTrackerAxisAgrees(t *testing.T) {
	var methods, caps []string
	for name, m := range connectorprotocol.Methods {
		if m.Axis == connectorprotocol.AxisTracker {
			methods = append(methods, name)
		}
	}
	for name, axis := range connectorprotocol.Capabilities {
		if axis == connectorprotocol.AxisTracker {
			caps = append(caps, name)
		}
	}
	described := slices.Sorted(slices.Values(slices.Concat(methods, caps)))
	described = slices.Compact(described)
	if core := coreTrackerNames(t); !slices.Equal(described, core) {
		t.Errorf("the description names tracker methods and capabilities %v, connectortracker names %v", described, core)
	}
	served := map[string]bool{}
	for _, name := range methods {
		m := connectorprotocol.Methods[name]
		if m.Capability == "" || connectorprotocol.Capabilities[m.Capability] != connectorprotocol.AxisTracker {
			t.Errorf("method %s names capability %q, which is not a tracker capability", name, m.Capability)
		}
		served[m.Capability] = true
	}
	for _, c := range caps {
		// The one capability that serves no method: it changes the id grammar.
		if !served[c] && c != connectortracker.CapProjectKeyIDs {
			t.Errorf("capability %s serves no method", c)
		}
	}
}
