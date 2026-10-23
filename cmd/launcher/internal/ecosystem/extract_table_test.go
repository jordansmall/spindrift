package ecosystem_test

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registrydiscover"
	"spindrift.dev/launcher/internal/testutil"
)

// Extract reads each registered Strategy's config from its own InTreeConfigPath
// (issue #3184). registrydiscover pins the mechanism on stand-ins; this holds
// the real Strategies to it, since a core package may not name them.
func TestExtract_ReadsEveryRegisteredPath(t *testing.T) {
	var checked int
	for _, row := range ecosystem.Registered() {
		if row.InTreeConfigPath == "" || row.ConfigParser == nil {
			continue
		}
		checked++
		t.Run(row.Name, func(t *testing.T) {
			dir := t.TempDir()
			testutil.WriteFixture(t, dir, row.InTreeConfigPath+"/placeholder", "")

			_, _, err := registrydiscover.Extract(dir)
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
