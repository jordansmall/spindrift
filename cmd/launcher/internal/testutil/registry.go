package testutil

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// WriteRegistry marshals rows (a []promptassembly.FragmentRow, or nil for an
// empty registry) to registry.json in a fresh temp dir and returns its path.
func WriteRegistry(t *testing.T, rows any) string {
	t.Helper()
	if rows == nil {
		rows = []struct{}{}
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("marshal registry: %v", err)
	}
	path := filepath.Join(t.TempDir(), "registry.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	return path
}
