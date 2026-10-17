package opencode_test

import (
	"testing"

	"spindrift.dev/launcher/internal/driver"
)

// newDriver returns the registered opencode Driver, the seam the reader tests
// in this package exercise.
func newDriver(t *testing.T) driver.Driver {
	t.Helper()
	d, err := driver.New("opencode")
	if err != nil {
		t.Fatal(err)
	}
	return d
}
