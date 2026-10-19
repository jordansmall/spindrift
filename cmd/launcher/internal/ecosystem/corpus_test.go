package ecosystem_test

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystemtest"
)

// TestCorpus replays each corpus directory through the ecosystemtest kit
// (issues #5654, #5655): one subtest per directory, one per case beneath it.
// It walks testdata/corpus rather than naming rows, so a directory added there
// is replayed without a second edit here.
func TestCorpus(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("testdata", "corpus"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		t.Run(name, func(t *testing.T) {
			row, ok := tableRow(name)
			if !ok {
				t.Fatalf("testdata/corpus/%s matches no ecosystem.Table row", name)
			}
			ecosystemtest.Run(t, row, filepath.Join("testdata", "corpus", name))
		})
	}
}

func tableRow(name string) (ecosystem.Row, bool) {
	for _, row := range ecosystem.Table {
		if row.Name == name {
			return row, true
		}
	}
	return ecosystem.Row{}, false
}
