package ecosystem_test

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/bindregistry"
)

// Table order is the precedence that agent/entrypoint.sh's old if/elif chain
// had (#2930); no corpus case holds two lockfiles at once. The npm family sits
// ahead of go, and cargo ahead of both.
func TestClassify_TableOrderDecidesPrecedence(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  string
	}{
		{"cargo beats go", []string{"Cargo.lock", "go.sum"}, "cargo"},
		{"npm family beats go", []string{"package-lock.json", "go.sum"}, "npm/pnpm/yarn"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
			}

			if got := bindregistry.Classify(dir); got != tc.want {
				t.Errorf("Classify(%q) = %q, want %q", dir, got, tc.want)
			}
		})
	}
}

// Order is asserted, not just membership: the verb's apply/revert loop
// iterates InTreeBindings() in whatever order it returns, so npm/yarn/pnpm
// must match ecosystem.Table's own order (issue #3180, rows from issue
// #2933). cargo is absent by design (issue #3201), pinned by
// TestInTreeBindings_ExcludesCargo below.
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

// Cargo's ecosystem.Table row keeps a non-empty InTreeConfigPath for
// host-side discovery but carries a non-nil RepoAwareHomeConfig, so
// InTreeBindings' filter must exclude it (issue #3201). A regression would
// dual-write the in-tree rewrite and cargo's source-replacement home config,
// which its 1:1 URL to source-name rule cannot tolerate.
func TestInTreeBindings_ExcludesCargo(t *testing.T) {
	for _, row := range bindregistry.InTreeBindings() {
		if row.Name == "cargo" {
			t.Fatalf("InTreeBindings() = %+v, want it to never include the cargo row", bindregistry.InTreeBindings())
		}
	}
}
