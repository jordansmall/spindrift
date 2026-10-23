package gradle_test

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystem/gradle"
	"spindrift.dev/launcher/internal/ecosystemtest"
)

// TestCorpus replays the gradle Ecosystem corpus through the ecosystemtest kit
// (ADR 0066 decision 4). Cases that name a neighbour (npm) resolve it through
// the core package's own row, so the neighbour's behaviour is its own
// package's concern, not this one's.
func TestCorpus(t *testing.T) {
	ecosystemtest.Run(t, gradle.Strategy, filepath.Join("testdata", "corpus"), lookup)
}

func lookup(name string) (ecosystem.Strategy, bool) {
	switch name {
	case gradle.Strategy.Name:
		return gradle.Strategy, true
	case ecosystem.Npm.Name:
		return ecosystem.Npm, true
	}
	return ecosystem.Strategy{}, false
}

// The replay pipeline reads the process-wide registry (route parsing validates
// ecosystem names against it), and the Bundle that owns the real Register call
// is not linked here. Register the Strategies the corpus names, in the order
// the Bundle lists them.
func TestMain(m *testing.M) {
	ecosystem.Register(ecosystem.Npm, gradle.Strategy)
	os.Exit(m.Run())
}
