package bundled

import (
	"os"
	"testing"

	"spindrift.dev/launcher/internal/driver/drivertest"
)

func TestMain(m *testing.M) { os.Exit(drivertest.RunWithClaudeConnector(m)) }
