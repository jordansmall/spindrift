package settle

import (
	"os"
	"testing"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/seamtest"
)

func TestMain(m *testing.M) {
	// Tests that parse a stamped log need the "claude" Driver registered, and
	// nothing else in this package links it in.
	driver.Register(seamtest.Strategy)
	os.Exit(m.Run())
}
