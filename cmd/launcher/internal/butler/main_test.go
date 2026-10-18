package butler

import (
	"os"
	"testing"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/seamtest"
)

func TestMain(m *testing.M) {
	// Under the claude name the Strategy sums its result summaries, as the real
	// claude Driver does, so the turn, duration and cost totals the tests read
	// off result lines still count.
	claude := seamtest.Strategy
	claude.Totals = driverkit.SessionTotals
	driver.Register(claude)
	os.Exit(m.Run())
}
