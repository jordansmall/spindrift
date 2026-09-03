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
