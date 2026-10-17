package opencode

import (
	"io"

	"spindrift.dev/launcher/internal/driver/driverkit"
)

// New returns a heartbeat writer that forwards every byte to raw and writes
// heartbeat lines to out. opencode's log has no sub-agent attribution, so the
// top-level role is "" and OnModel reports an empty role.
func New(raw io.Writer, issue string, out io.Writer) *driverkit.HeartbeatWriter {
	return driverkit.NewHeartbeatWriter(raw, issue, out, NewDecoder, "")
}
