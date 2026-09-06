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
