package npmfamily_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/registrydiscover"
)

// Extract reads each registered Strategy's config from its own InTreeConfigPath
// (issue #3184). registrydiscover pins the mechanism on stand-ins; this holds
// the real Strategies to it, since a core package may not name them.
func TestExtract_ReadsEveryRegisteredPath(t *testing.T) {
	var checked int
	for _, row := range strategies {
		if row.InTreeConfigPath == "" || row.ConfigParser == nil {
			continue
		}
		checked++
		t.Run(row.Name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, row.InTreeConfigPath), 0o755); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}

			_, _, err := registrydiscover.Extract(dir, strategies)
			if err == nil {
				t.Fatal("Extract: want a read error for a directory at the row's path, got nil")
			}
			if !strings.Contains(err.Error(), row.InTreeConfigPath) {
				t.Errorf("Extract error = %q, want it to name %q (the %s Strategy's path)", err, row.InTreeConfigPath, row.Name)
			}
		})
	}
	if checked == 0 {
		t.Fatal("no registered Strategy carries an InTreeConfigPath and ConfigParser")
	}
}
