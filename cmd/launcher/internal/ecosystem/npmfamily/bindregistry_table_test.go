package npmfamily_test

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/bindregistry"
	"spindrift.dev/launcher/internal/ecosystem"
)

// Order is asserted, not just membership: the verb's apply/revert loop
// iterates InTreeBindings() in whatever order it returns, so npm/yarn/pnpm
// must match ecosystem.Table's own order (issue #3180, Strategies from issue
// #2933). cargo is absent by design (issue #3201), pinned in its own package
// by TestInTreeBindings_ExcludesCargo.
func TestInTreeBindingTableHasExpectedRows(t *testing.T) {
	want := []struct {
		name       string
		configPath string
	}{
		{name: "npm", configPath: ".npmrc"},
		{name: "yarn", configPath: ".yarnrc.yml"},
		{name: "pnpm", configPath: "pnpm-workspace.yaml"},
	}

	got := bindregistry.InTreeBindings()
	if len(got) != len(want) {
		t.Fatalf("InTreeBindings() has %d rows, want exactly %d: %+v", len(got), len(want), got)
	}
	for i, tc := range want {
		if got[i].Name != tc.name || got[i].InTreeConfigPath != tc.configPath {
			t.Errorf("row %d = {Name: %q, InTreeConfigPath: %q}, want {Name: %q, InTreeConfigPath: %q}",
				i, got[i].Name, got[i].InTreeConfigPath, tc.name, tc.configPath)
		}
	}
}

// Every lockfile a Strategy declares classifies as that Strategy. The
// toolchain hint names this classification, and toolchain's own tests run on a
// stand-in, so this is where each real lockfile's spelling is held.
func TestClassify_EachLockfileClassifiesAsItsStrategy(t *testing.T) {
	registered := ecosystem.Registered()
	if len(registered) == 0 {
		t.Fatal("no Strategies registered")
	}
	for _, row := range registered {
		for _, lockfile := range row.LockfileNames {
			t.Run(row.Name+"/"+lockfile, func(t *testing.T) {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, lockfile), nil, 0o644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
				if got := bindregistry.Classify(dir); got != row.Classification {
					t.Errorf("Classify with %s = %q, want %q", lockfile, got, row.Classification)
				}
			})
		}
	}
}
