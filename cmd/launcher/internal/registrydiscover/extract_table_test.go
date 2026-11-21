package registrydiscover

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystemtest/synthetic"
	"spindrift.dev/launcher/internal/testutil"
)

// TestExtract_ReadsEveryPathFromRows verifies Extract reads each config file
// from the Strategy's InTreeConfigPath, not a hardcoded path (issue #3184). A
// directory planted at the path fails the read with the path named, so no
// config format is needed; two Strategies rule out one shared constant.
func TestExtract_ReadsEveryPathFromRows(t *testing.T) {
	rows := []ecosystem.Strategy{synthetic.Strategy, synthetic.Twin("twin")}
	for _, row := range rows {
		if row.InTreeConfigPath == "" || row.ConfigParser == nil {
			t.Fatalf("%s: the stand-in needs an InTreeConfigPath and ConfigParser", row.Name)
		}
		t.Run(row.Name, func(t *testing.T) {
			dir := t.TempDir()
			testutil.WriteFixture(t, dir, row.InTreeConfigPath+"/placeholder", "")

			_, _, err := Extract(dir, rows)
			if err == nil {
				t.Fatal("Extract: want a read error for a directory at the row's path, got nil")
			}
			if !strings.Contains(err.Error(), row.InTreeConfigPath) {
				t.Errorf("Extract error = %q, want it to name %q (the %s Strategy's path)", err, row.InTreeConfigPath, row.Name)
			}
		})
	}
}
