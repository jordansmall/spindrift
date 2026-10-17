package opencode_test

import (
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit/conformance"
	"spindrift.dev/launcher/internal/driver/opencode"
)

func TestCorpus(t *testing.T) {
	conformance.Run(t, opencode.Strategy, filepath.Join("testdata", "corpus"))
}
