package ecosystem_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystemtest"
	"spindrift.dev/launcher/internal/registryvocab"
)

// TestCorpus replays each corpus directory through the ecosystemtest kit
// (issues #5654, #5655): one subtest per directory, one per case beneath it.
// It walks testdata/corpus rather than naming Strategies, so a directory added there
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
			ecosystemtest.Run(t, row, filepath.Join("testdata", "corpus", name), tableRow)
		})
	}
}

func tableRow(name string) (ecosystem.Strategy, bool) {
	for _, row := range ecosystem.Table {
		if row.Name == name {
			return row, true
		}
	}
	return ecosystem.Strategy{}, false
}

// TestCorpus_EveryInTreeRowDerivesAPath guards the Table against a Strategy whose
// in-tree config parses but derives no path, which would leave the proxy
// refusing every request that ecosystem makes, or one whose subtree admits no
// path beneath itself. Strategies with an empty InTreeConfigPath derive nothing by
// design and are excluded.
func TestCorpus_EveryInTreeRowDerivesAPath(t *testing.T) {
	for _, row := range ecosystem.Table {
		if row.InTreeConfigPath == "" {
			continue
		}
		t.Run(row.Name, func(t *testing.T) {
			found := false
			goldens, err := filepath.Glob(filepath.Join("testdata", "corpus", row.Name, "*", "pathset.golden.json"))
			if err != nil {
				t.Fatal(err)
			}
			for _, g := range goldens {
				raw, err := os.ReadFile(g)
				if err != nil {
					t.Fatal(err)
				}
				var doc struct {
					Derived []struct {
						Subtrees []struct {
							Ecosystem string `json:"ecosystem"`
							Path      string `json:"path"`
						}
					} `json:"derived"`
				}
				if err := json.Unmarshal(raw, &doc); err != nil {
					t.Fatalf("%s: %v", g, err)
				}
				for _, d := range doc.Derived {
					roots := make(registryvocab.PathSet, len(d.Subtrees))
					for i, s := range d.Subtrees {
						roots[i] = s.Path
					}
					for _, s := range d.Subtrees {
						if s.Ecosystem != row.Name {
							continue
						}
						found = true
						if !roots.Admits(s.Path + "/config.json") {
							t.Errorf("%s: derived %s subtree %q does not admit a path under itself", g, row.Name, s.Path)
						}
					}
				}
			}
			if !found {
				t.Errorf("no corpus case under testdata/corpus/%s derives a %s subtree", row.Name, row.Name)
			}
		})
	}
}

// A name added to a Strategy's LockfileNames without a case holding that file under
// the Strategy's corpus directory would go unclassified and unscanned, since each
// stage replays only what a case's repo/ holds.
func TestCorpus_EveryLockfileNameHasACase(t *testing.T) {
	for _, row := range ecosystem.Table {
		for _, lockfile := range row.LockfileNames {
			t.Run(row.Name+"/"+lockfile, func(t *testing.T) {
				matches, err := filepath.Glob(filepath.Join("testdata", "corpus", row.Name, "*", "repo", lockfile))
				if err != nil {
					t.Fatal(err)
				}
				if len(matches) == 0 {
					t.Errorf("no corpus case under testdata/corpus/%s holds repo/%s", row.Name, lockfile)
				}
			})
		}
	}
}

// The lockfile scan's golden is empty unless a lockfile names the Forwarder, so
// a Strategy whose cases all leave it empty proves nothing about the scan reaching
// that Strategy.
func TestCorpus_EveryLockfileRowHasAScanHit(t *testing.T) {
	for _, row := range ecosystem.Table {
		if len(row.LockfileNames) == 0 {
			continue
		}
		t.Run(row.Name, func(t *testing.T) {
			goldens, err := filepath.Glob(filepath.Join("testdata", "corpus", row.Name, "*", "lockfile-scan.golden.txt"))
			if err != nil {
				t.Fatal(err)
			}
			for _, g := range goldens {
				info, err := os.Stat(g)
				if err != nil {
					t.Fatal(err)
				}
				if info.Size() > 0 {
					return
				}
			}
			t.Errorf("no corpus case under testdata/corpus/%s has a non-empty lockfile-scan.golden.txt", row.Name)
		})
	}
}
