package pi_test

import (
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit/conformance"
	"spindrift.dev/launcher/internal/driver/pi"
)

func TestCorpus(t *testing.T) {
	conformance.Run(t, pi.Strategy, filepath.Join("testdata", "corpus"))
}
