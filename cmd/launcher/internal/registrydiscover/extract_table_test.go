package registrydiscover

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/testutil"
)

// TestExtract_ReadsEveryPathFromTable verifies Extract reads each config file
// from wherever ecosystem.Table's Strategy names it as InTreeConfigPath, rather than
// a path hardcoded in registrydiscover itself -- the coupling this issue
// (#3184) closes. A directory planted at the path makes the read fail, and the
// failure names the path, so the check needs no Strategy's config format.
func TestExtract_ReadsEveryPathFromTable(t *testing.T) {
	for _, row := range ecosystem.Table {
		if row.InTreeConfigPath == "" || row.ConfigParser == nil {
			continue
		}
		t.Run(row.Name, func(t *testing.T) {
			dir := t.TempDir()
			testutil.WriteFixture(t, dir, row.InTreeConfigPath+"/placeholder", "")

			_, _, err := Extract(dir)
			if err == nil {
				t.Fatal("Extract: want a read error for a directory at the row's path, got nil")
			}
			if !strings.Contains(err.Error(), row.InTreeConfigPath) {
				t.Errorf("Extract error = %q, want it to name %q (ecosystem.Table's %s row)", err, row.InTreeConfigPath, row.Name)
			}
		})
	}
}
