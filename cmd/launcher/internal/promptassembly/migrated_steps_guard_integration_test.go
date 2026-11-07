//go:build integration

package promptassembly

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoBackendNamedGateRowOutsideDeprecatedFills(t *testing.T) {
	_, reg := seamPrompts(t)
	if len(reg.Rows) == 0 {
		t.Fatal("registry has no rows, check is vacuous")
	}
	for _, r := range backendGatedRows(reg.Rows) {
		t.Errorf("row %s (var %s) gates on %s: move the step onto a slot, or deprecate the var in lib/removed-fragment-vars.nix", r.Fragment, r.Var, r.Gate)
	}
}

// Shipped top-level prompts, chores included, name only the slot vars: a
// deprecated fill is for Consumer overrides, and a removed var would ship its
// literal token to the model.
func TestShippedPromptsReferenceNoDeprecatedOrRemovedFragmentVar(t *testing.T) {
	prompts, _ := seamPrompts(t)
	seen := 0
	err := filepath.WalkDir(prompts, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "fragments" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".md") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		seen++
		for _, r := range removedFragmentVars {
			if strings.Contains(string(b), "${"+r.name+"}") {
				t.Errorf("%s references the %s var ${%s}", path, r.severity, r.name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen < 10 {
		t.Errorf("scanned %d prompts, want at least 10: the walk has gone stale", seen)
	}
}
