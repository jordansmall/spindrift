package dispatchrecord

import (
	"os"
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/seamtest"
)

func TestMain(m *testing.M) {
	// claude sums its result summaries, like the real claude Driver; seam-alt
	// sums per call, like the opencode Driver the stamped tests replaced.
	claude := seamtest.Strategy
	claude.Totals = driverkit.SessionTotals
	seamtest.RegisterStrategy(claude)
	alt := seamtest.Strategy
	alt.Name = altDriver
	seamtest.RegisterStrategy(alt)
	os.Exit(m.Run())
}
