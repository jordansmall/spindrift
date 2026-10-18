package forge_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// callsForgetest reports whether the Go file at path calls forgetest.fn from
// inside a top-level Test function; mentions in comments or calls from other
// functions do not count.
func callsForgetest(path, fn string) (bool, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return false, err
	}
	found := false
	for _, decl := range file.Decls {
		f, ok := decl.(*ast.FuncDecl)
		if !ok || f.Recv != nil || f.Body == nil || !strings.HasPrefix(f.Name.Name, "Test") {
			continue
		}
		ast.Inspect(f.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return !found
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == fn {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "forgetest" {
					found = true
				}
			}
			return !found
		})
	}
	return found, nil
}

// contractProblems lists the contracts each backend's package under root owes
// but no _test.go file runs: the tracker contract for a backend valid as an
// Issue Tracker, the Code Forge contract for one valid as a Code Forge. It is
// a static scan, so it imports no backend package. A filesystem or parse error
// is reported as itself rather than as the missing call it hides.
func contractProblems(root string, descs []backend.Descriptor) []string {
	var problems []string
	for _, d := range descs {
		tests, err := filepath.Glob(filepath.Join(root, d.Name, "*_test.go"))
		if err != nil {
			problems = append(problems, d.Name+": "+err.Error())
			continue
		}
		owed := []struct {
			valid        bool
			role, called string
		}{
			{d.ValidAsTracker, "an Issue Tracker", "RunTrackerContract"},
			{d.ValidAsCodeForge, "a Code Forge", "RunCodeForgeContract"},
		}
		for _, o := range owed {
			if !o.valid {
				continue
			}
			ran, errored := false, false
			for _, f := range tests {
				ok, err := callsForgetest(f, o.called)
				if err != nil {
					problems = append(problems, d.Name+": "+err.Error())
					errored = true
				}
				ran = ran || ok
			}
			if !ran && !errored {
				problems = append(problems, d.Name+": valid as "+o.role+" but no _test.go file calls forgetest."+o.called)
			}
		}
	}
	return problems
}

// TestEveryBackendRunsItsContracts pins issue #5637: a backend marked valid as
// an Issue Tracker or a Code Forge must run the matching forgetest contract,
// so registering one without the contract's coverage fails.
func TestEveryBackendRunsItsContracts(t *testing.T) {
	if len(backend.Registry) == 0 {
		t.Fatal("backend.Registry is empty; nothing to guard")
	}
	for _, p := range contractProblems(".", backend.Registry) {
		t.Error(p)
	}
}

func TestContractProblemsReportsMissingCalls(t *testing.T) {
	root := t.TempDir()
	const (
		tracker = "package x\nfunc TestT(t *testing.T) { forgetest.RunTrackerContract(t, h) }"
		forge   = "package x\nfunc TestF(t *testing.T) { forgetest.RunCodeForgeContract(t, h) }"
	)
	writeTree(t, root, map[string]string{
		"both/a_test.go":      tracker,
		"both/b_test.go":      forge,
		"notracker/a_test.go": forge,
		"noforge/a_test.go":   tracker,
		// The names appear only in comments, so neither contract runs.
		"commented/a_test.go": "package x\n// forgetest.RunTrackerContract(t, h)\n/* forgetest.RunCodeForgeContract(t, h) */",
		// Called only from a helper, not from a Test function.
		"helperonly/a_test.go": "package x\nfunc helper(t *testing.T) { forgetest.RunTrackerContract(t, h) }",
		// Not valid as a tracker or a forge: owes nothing.
		"neither/a_test.go": "package x",
	})

	descs := []backend.Descriptor{
		{Name: "both", ValidAsTracker: true, ValidAsCodeForge: true},
		{Name: "notracker", ValidAsTracker: true, ValidAsCodeForge: true},
		{Name: "noforge", ValidAsTracker: true, ValidAsCodeForge: true},
		{Name: "commented", ValidAsTracker: true, ValidAsCodeForge: true},
		{Name: "helperonly", ValidAsTracker: true},
		{Name: "neither"},
	}
	got := contractProblems(root, descs)
	want := []string{
		"notracker: valid as an Issue Tracker but no _test.go file calls forgetest.RunTrackerContract",
		"noforge: valid as a Code Forge but no _test.go file calls forgetest.RunCodeForgeContract",
		"commented: valid as an Issue Tracker but no _test.go file calls forgetest.RunTrackerContract",
		"commented: valid as a Code Forge but no _test.go file calls forgetest.RunCodeForgeContract",
		"helperonly: valid as an Issue Tracker but no _test.go file calls forgetest.RunTrackerContract",
	}
	if !slices.Equal(got, want) {
		t.Errorf("contractProblems = %q, want %q", got, want)
	}
}

func TestContractProblemsSurfacesReadErrors(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"badparse/a_test.go": "func ("})

	got := contractProblems(root, []backend.Descriptor{{Name: "badparse", ValidAsTracker: true}})
	if len(got) != 1 || !strings.HasPrefix(got[0], "badparse: ") || !strings.Contains(got[0], "a_test.go") {
		t.Errorf("contractProblems = %q, want exactly one parse error naming a_test.go", got)
	}
}
