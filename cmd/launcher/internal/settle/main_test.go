package settle

import (
	"os"
	"testing"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/seamtest"
)

// testDrivers is the Driver set tests read stamped logs through: claude, which
// nothing else in this package links in.
var testDrivers *driver.Set

func TestMain(m *testing.M) {
	testDrivers = driver.NewSet(seamtest.Strategy())
	os.Exit(m.Run())
}
