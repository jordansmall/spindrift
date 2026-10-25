package gomod_test

import (
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
