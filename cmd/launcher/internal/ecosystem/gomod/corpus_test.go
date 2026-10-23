package gomod_test

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystem/gomod"
	"spindrift.dev/launcher/internal/ecosystemtest"
)

// TestCorpus replays the go Ecosystem corpus through the ecosystemtest kit
// (ADR 0066 decision 4).
func TestCorpus(t *testing.T) {
	ecosystemtest.Run(t, gomod.Strategy, filepath.Join("testdata", "corpus"), lookup)
}

func lookup(name string) (ecosystem.Strategy, bool) {
	if name == gomod.Strategy.Name {
		return gomod.Strategy, true
	}
	return ecosystem.Strategy{}, false
}

// The replay pipeline reads the process-wide registry (route parsing validates
// ecosystem names against it), and the Bundle that owns the real Register call
// is not linked here.
func TestMain(m *testing.M) {
	ecosystem.Register(gomod.Strategy)
	os.Exit(m.Run())
}
