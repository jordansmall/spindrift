package bundle_test

import (
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/bundle"
	"spindrift.dev/launcher/internal/driver/drivertest"
)

var loadDrivers = sync.OnceValues(func() (*driver.Set, error) {
	return drivertest.NewDrivers(bundle.Drivers())
})

// bundledDrivers is the Set a binary linking the bundle offers: the bundle's
// native Drivers plus claude from its row. It is built once, as loading the
// claude Connector compiles its wasm.
func bundledDrivers(t testing.TB) *driver.Set {
	t.Helper()
	s, err := loadDrivers()
	if err != nil {
		t.Fatal(err)
	}
	return s
}
