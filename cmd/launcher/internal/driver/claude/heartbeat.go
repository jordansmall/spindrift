package claude

import (
	"io"

	"spindrift.dev/launcher/internal/driver/driverkit"
)

// Writer is the shared driverkit heartbeat, aliased for existing callers.
type Writer = driverkit.HeartbeatWriter

// New returns a Writer that emits heartbeat lines for issue to out.
func New(raw io.Writer, issue string, out io.Writer) *Writer {
	return NewWithTopLevelRole(raw, issue, out, "")
}

// NewWithTopLevelRole is like New, but attributes top-level messages to
// topLevelRole, for a pass the orchestrator owns as something other than
// implementation (issue #2092). An empty topLevelRole keeps the
// ImplementorRole default.
func NewWithTopLevelRole(raw io.Writer, issue string, out io.Writer, topLevelRole string) *Writer {
	if topLevelRole == "" {
		topLevelRole = ImplementorRole
	}
	return driverkit.NewHeartbeatWriter(raw, issue, out, NewDecoder, topLevelRole).Track(driverkit.HeartbeatTracking{
		Phase: toolToPhase,
		Kind:  toolKind,
		// The live heartbeat groups by family, not exact model id: it is a
		// coarse in-flight signal, so one row per family stays readable. The
		// final per-model token table (usage.go) diverges on purpose and keys
		// on the exact id (issue #2110).
		Family: ModelFamily,
	})
}
