package console

import (
	"os"
	"testing"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/seamtest"
)

// The console tests decode the canonical-event grammar, not a real Driver's.
// seamtest.Strategy registers under "claude", driver's default name, so every
// driver.New("") here resolves to it. Unlike seamtest.RegisterDriver,
// driver.Register panics on a duplicate name, so linking the Driver bundle
// into this binary would panic; driver/bundle_guard_test.go keeps it out.
func TestMain(m *testing.M) {
	driver.Register(seamtest.Strategy)
	os.Exit(m.Run())
}
