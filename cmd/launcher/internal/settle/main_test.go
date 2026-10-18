package settle

import (
	"os"
	"testing"

	"spindrift.dev/launcher/internal/seamtest"
)

func TestMain(m *testing.M) {
	// Tests that parse a stamped log, or resolve "claude" directly, need that
	// Driver registered, and nothing else in this package links it in.
	seamtest.RegisterStrategy(seamtest.Strategy)
	os.Exit(m.Run())
}
