package butler

import (
	"os"
	"testing"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/seamtest"
)

// testDrivers is the Driver set the Records store reads logs through.
var testDrivers *driver.Set

func TestMain(m *testing.M) {
	// Under the claude name the Strategy sums its result summaries, as the real
	// claude Driver does, so the turn, duration and cost totals the tests read
	// off result lines still count.
	claude := seamtest.Strategy()
	claude.Totals = driverkit.SessionTotals
	testDrivers = driver.NewSet(claude)
	os.Exit(m.Run())
}
