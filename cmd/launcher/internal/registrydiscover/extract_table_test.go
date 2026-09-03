package registrydiscover

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
)

// cargoRow looks up ecosystem.Table's cargo row, failing the test if it's
// ever renamed or removed -- these table-driven tests must never fall back
// to a string literal for the path they assert against.
func cargoRow(t *testing.T) ecosystem.Row {
	t.Helper()
	for _, row := range ecosystem.Table {
		if row.Name == "cargo" {
			return row
		}
	}
	t.Fatal("ecosystem.Table has no \"cargo\" row")
	return ecosystem.Row{}
}

// TestExtractors_MatchInTreeRows guards against a future ecosystem.Table row
// acquiring a non-empty InTreeConfigPath with no matching entry in
// extractors: Extract silently skips such a row (see its own doc), so
// without this guard a new in-tree ecosystem could go undiscovered with no
// test failure anywhere. Checks both directions: every in-tree row has a
// parser, and extractors names no ecosystem that isn't (currently) an
// in-tree row.
func TestExtractors_MatchInTreeRows(t *testing.T) {
	inTree := make(map[string]bool)
	for _, row := range ecosystem.Table {
		if row.InTreeConfigPath != "" {
			inTree[row.Name] = true
		}
	}

	for name := range inTree {
		if _, ok := extractors[name]; !ok {
			t.Errorf("ecosystem.Table row %q has a non-empty InTreeConfigPath but no entry in extractors", name)
		}
	}
	for name := range extractors {
		if !inTree[name] {
			t.Errorf("extractors has an entry for %q, but no ecosystem.Table row of that name has a non-empty InTreeConfigPath", name)
		}
	}
}

// TestExtract_ReadsPathFromTable verifies Extract reads the cargo config
// from wherever ecosystem.Table's cargo row names as InTreeConfigPath,
// rather than a path hardcoded in registrydiscover itself -- the coupling
// this issue (#3184) closes.
func TestExtract_ReadsPathFromTable(t *testing.T) {
	row := cargoRow(t)
	dir := t.TempDir()
	full := filepath.Join(dir, row.InTreeConfigPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	cargoConfig := `
[registries.mycorp]
index = "sparse+https://cargo.example.com/index/"
`
	if err := os.WriteFile(full, []byte(cargoConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	declared, _, err := Extract(dir)
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if len(declared) != 1 {
		t.Fatalf("declared = %+v, want exactly 1", declared)
	}
	if declared[0].ConfigPath != row.InTreeConfigPath {
		t.Errorf("declared[0].ConfigPath = %q, want %q (ecosystem.Table's cargo row)", declared[0].ConfigPath, row.InTreeConfigPath)
	}
}
