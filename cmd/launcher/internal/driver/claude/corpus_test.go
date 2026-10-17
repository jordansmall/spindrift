package claude_test

import (
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/driver/driverkit/conformance"
)

func TestCorpus(t *testing.T) {
	conformance.Run(t, claude.Strategy, filepath.Join("testdata", "corpus"))
}
