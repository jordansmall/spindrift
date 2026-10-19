package promptassembly

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
)

// backendGateTokens are the `_`-delimited gate-name tokens that mark a row as
// gated on one backend rather than on a slot's axis signal: every registered
// backend's name, so a new backend's gate is caught, plus the "GH" arm alias.
var backendGateTokens = func() map[string]bool {
	tokens := map[string]bool{"GH": true}
	for _, d := range backend.Registry {
		tokens[strings.ToUpper(d.Name)] = true
	}
	return tokens
}()

// backendGatedRows returns the rows whose gate names a backend and whose var
// has no deprecated row in removedFragmentVars: a migrated step must hang off
// a slot, so only deprecation-window fills may still gate on a backend.
func backendGatedRows(rows []FragmentRow) []FragmentRow {
	var bad []FragmentRow
	for _, r := range rows {
		named := false
		for _, tok := range strings.Split(r.Gate, "_") {
			named = named || backendGateTokens[tok]
		}
		if named && !isDeprecatedFragmentVar(r.Var) {
			bad = append(bad, r)
		}
	}
	return bad
}

func TestNoBackendNamedGateRowOutsideDeprecatedFills(t *testing.T) {
	for _, r := range backendGatedRows(loadTestRegistry(t).Rows) {
		t.Errorf("row %s (var %s) gates on %s: move the step onto a slot, or deprecate the var in lib/removed-fragment-vars.nix", r.Fragment, r.Var, r.Gate)
	}
}

func TestBackendGatedRowsFlagsUndeprecatedRow(t *testing.T) {
	rows := []FragmentRow{
		{Gate: "FIX_CI_READ_GH", Var: "FIX_CI_READ_GITHUB_STEP"},
		{Gate: "FIX_CI_READ_GH", Var: "FIX_CI_READ_NEW_STEP"},
		{Gate: "ISSUE_TRACKER_GITHUB_READONLY_LOG", Var: "BRAND_NEW_STEP"},
		{Gate: "FIX_CI_READ_ANY", Var: "FIX_CI_READ_STEP"},
		{Gate: "", Var: "UNGATED_STEP"},
	}
	got := backendGatedRows(rows)
	if len(got) != 2 || got[0].Var != "FIX_CI_READ_NEW_STEP" || got[1].Var != "BRAND_NEW_STEP" {
		t.Errorf("backendGatedRows = %+v, want the two undeprecated backend-gated rows", got)
	}
}

// Shipped top-level prompts, chores included, name only the slot vars: a
// deprecated fill is for Consumer overrides, and a removed var would ship its
// literal token to the model.
func TestShippedPromptsReferenceNoDeprecatedOrRemovedFragmentVar(t *testing.T) {
	seen := 0
	err := filepath.WalkDir(promptsDir, func(path string, d fs.DirEntry, err error) error {
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
