package ecosystem_test

import (
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystemtest"
)

// TestCorpus replays cargo's corpus through the kit (issue #5654): a
// registry config discovery reads, the cargo-credentials store, the
// repo-aware home config and the config.json rewrite.
func TestCorpus(t *testing.T) {
	var cargo ecosystem.Row
	for _, row := range ecosystem.Table {
		if row.Name == "cargo" {
			cargo = row
		}
	}
	if cargo.Name == "" {
		t.Fatal("ecosystem.Table has no cargo row")
	}
	ecosystemtest.Run(t, cargo, filepath.Join("testdata", "corpus", "cargo"))
}
