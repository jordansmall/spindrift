package ecosystem

import "testing"

// TestTable_NamesUnique verifies no two rows share a name, so a lookup by
// name can never be ambiguous about which row it found.
func TestTable_NamesUnique(t *testing.T) {
	seen := make(map[string]bool, len(Table))
	for _, row := range Table {
		if seen[row.Name] {
			t.Fatalf("duplicate row name %q", row.Name)
		}
		seen[row.Name] = true
	}
}

// TestTable_ClassificationNonEmpty verifies every row resolves to a
// non-empty classification, so a row added without one fails loudly here
// rather than silently carrying a blank nudge string.
func TestTable_ClassificationNonEmpty(t *testing.T) {
	for _, row := range Table {
		if row.Classification == "" {
			t.Errorf("row %q has empty classification", row.Name)
		}
	}
}

// TestTable_InTreeConfigPath pins every row's InTreeConfigPath, including the
// empty ones, so a row added without a decision on its in-tree registry-config
// path fails here rather than silently being excluded (or included) by
// accident wherever a consumer filters on it.
func TestTable_InTreeConfigPath(t *testing.T) {
	want := map[string]string{
		"cargo":  ".cargo/config.toml",
		"npm":    ".npmrc",
		"yarn":   ".yarnrc.yml",
		"pnpm":   "pnpm-workspace.yaml",
		"go":     "",
		"gradle": "",
	}
	for _, row := range Table {
		path, ok := want[row.Name]
		if !ok {
			t.Errorf("row %q has no expected InTreeConfigPath in this test", row.Name)
			continue
		}
		if row.InTreeConfigPath != path {
			t.Errorf("row %q InTreeConfigPath = %q, want %q", row.Name, row.InTreeConfigPath, path)
		}
	}
}

// TestTable_Order pins the row order the nudge's first-hit precedence
// depends on, so a reorder that would silently change which ecosystem a
// mixed repo classifies as fails here rather than in the Box.
func TestTable_Order(t *testing.T) {
	want := []string{"cargo", "npm", "yarn", "pnpm", "go", "gradle"}
	if len(Table) != len(want) {
		t.Fatalf("got %d rows, want %d", len(Table), len(want))
	}
	for i, name := range want {
		if Table[i].Name != name {
			t.Errorf("row %d = %q, want %q", i, Table[i].Name, name)
		}
	}
}

// TestTable_PatternsNonEmptyExceptGradle verifies every row carries at least
// one allowlist pattern, except gradle, whose nil Patterns is deliberate
// (its Binding is a home-level init script, not allowlisted paths) rather
// than an omission.
func TestTable_PatternsNonEmptyExceptGradle(t *testing.T) {
	for _, row := range Table {
		if row.Name == "gradle" {
			if row.Patterns != nil {
				t.Errorf("row %q: want nil Patterns, got %d", row.Name, len(row.Patterns))
			}
			continue
		}
		if len(row.Patterns) == 0 {
			t.Errorf("row %q: want non-empty Patterns", row.Name)
		}
	}
}
