package cargo_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystem/cargo"
	"spindrift.dev/launcher/internal/ecosystemtest"
	"spindrift.dev/launcher/internal/registryvocab"
)

var corpusDir = filepath.Join("testdata", "corpus")

// TestCorpus replays the cargo Ecosystem corpus through the ecosystemtest kit
// (ADR 0066 decision 4).
func TestCorpus(t *testing.T) {
	ecosystemtest.Run(t, cargo.Strategy, corpusDir, lookup)
}

func lookup(name string) (ecosystem.Strategy, bool) {
	if name == cargo.Strategy.Name {
		return cargo.Strategy, true
	}
	return ecosystem.Strategy{}, false
}

// The replay pipeline reads the process-wide registry (route parsing validates
// ecosystem names against it), and the Bundle that owns the real Register call
// is not linked here.
func TestMain(m *testing.M) {
	ecosystem.Register(cargo.Strategy)
	os.Exit(m.Run())
}

// TestCorpus_DerivesAPath guards against an in-tree config that parses but
// derives no path, which would leave the proxy refusing every request cargo
// makes, or one whose subtree admits no path beneath itself.
func TestCorpus_DerivesAPath(t *testing.T) {
	found := false
	goldens, err := filepath.Glob(filepath.Join(corpusDir, "*", "pathset.golden.json"))
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
				if s.Ecosystem != cargo.Strategy.Name {
					continue
				}
				found = true
				if !roots.Admits(s.Path + "/config.json") {
					t.Errorf("%s: derived %s subtree %q does not admit a path under itself", g, cargo.Strategy.Name, s.Path)
				}
			}
		}
	}
	if !found {
		t.Errorf("no corpus case derives a %s subtree", cargo.Strategy.Name)
	}
}

// A name added to LockfileNames without a case holding that file would go
// unclassified and unscanned, since each stage replays only what a case's
// repo/ holds.
func TestCorpus_EveryLockfileNameHasACase(t *testing.T) {
	for _, lockfile := range cargo.Strategy.LockfileNames {
		t.Run(lockfile, func(t *testing.T) {
			matches, err := filepath.Glob(filepath.Join(corpusDir, "*", "repo", lockfile))
			if err != nil {
				t.Fatal(err)
			}
			if len(matches) == 0 {
				t.Errorf("no corpus case holds repo/%s", lockfile)
			}
		})
	}
}

// The lockfile scan's golden is empty unless a lockfile names the Forwarder, so
// a corpus whose cases all leave it empty proves nothing about the scan
// reaching cargo.
func TestCorpus_HasALockfileScanHit(t *testing.T) {
	goldens, err := filepath.Glob(filepath.Join(corpusDir, "*", "lockfile-scan.golden.txt"))
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
	t.Error("no corpus case has a non-empty lockfile-scan.golden.txt")
}
