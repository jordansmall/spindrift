package main

import (
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/bundle"
	"spindrift.dev/launcher/internal/driver/drivertest"
)

// loadDrivers is built once per test binary: claude's Connector compiles on
// its first load, and nothing here writes to the Set.
var loadDrivers = sync.OnceValues(func() (*driver.Set, error) { return drivertest.NewDrivers(bundle.Drivers()) })

// testDrivers is the Driver set a Box runs with: the bundled natives plus the
// claude Connector.
func testDrivers(t testing.TB) *driver.Set {
	t.Helper()
	s, err := loadDrivers()
	if err != nil {
		t.Fatal(err)
	}
	return s
}
