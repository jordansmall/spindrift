package dispatchrecord

import (
	"os"
	"testing"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/seamtest"
)

// testDrivers is the Driver set every test reads logs through.
var testDrivers *driver.Set

func TestMain(m *testing.M) {
	// claude sums its result summaries, like the real claude Driver; seam-alt
	// sums per call, like the opencode Driver the stamped tests replaced.
	claude := seamtest.Strategy()
	claude.Totals = driverkit.SessionTotals
	alt := seamtest.Strategy()
	alt.Name = altDriver
	testDrivers = driver.NewSet(claude, alt)
	os.Exit(m.Run())
}
