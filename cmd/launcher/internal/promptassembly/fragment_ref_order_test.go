package promptassembly

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/testutil/repopath"
)

// Rows render in order, so a fragment's ${VAR} resolves only when VAR's row is
// above it; an unresolved name is left in the prompt verbatim (issue #5300).
// This pins that rule on the registry itself, without a nix build.
func TestFragmentsReferenceOnlyRowsAbove(t *testing.T) {
	reg, err := LoadRegistryFile(repopath.RegistryJSON())
	if err != nil {
		t.Fatal(err)
	}
	rowOf := map[string]int{}
	for i, row := range reg.Rows {
		rowOf[row.Var] = i
	}
	for i, row := range reg.Rows {
		files := []string{filepath.Join(repopath.PromptsDir(), "fragments", row.Fragment)}
		if row.Axis != "" {
			files, err = filepath.Glob(filepath.Join(repopath.PromptsDir(), "fragments", "*", row.Fragment))
			if err != nil || len(files) == 0 {
				t.Fatalf("axis row %s: no per-backend files found (err=%v)", row.Fragment, err)
			}
		}
		for _, file := range files {
			body, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			for v, j := range rowOf {
				if j >= i && strings.Contains(string(body), "${"+v+"}") {
					t.Errorf("%s (row %d) references ${%s}, whose row %d does not render above it", file, i, v, j)
				}
			}
		}
	}
}

// A leaked ${VAR} in a rendered prompt means a parent fragment named a row var
// that was not yet set when it rendered. The goldens are the rendered prompts.
func TestGoldenPromptsLeakNoRegistryVar(t *testing.T) {
	reg, err := LoadRegistryFile(repopath.RegistryJSON())
	if err != nil {
		t.Fatal(err)
	}
	prompts, err := filepath.Glob(filepath.Join(repopath.PromptAssemblyGoldenDir(), "*prompt.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(prompts) == 0 {
		t.Fatal("no golden prompts found, check is vacuous")
	}
	for _, p := range prompts {
		body, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range reg.Rows {
			if strings.Contains(string(body), "${"+row.Var+"}") {
				t.Errorf("%s leaks unresolved ${%s}", filepath.Base(p), row.Var)
			}
		}
	}
}
