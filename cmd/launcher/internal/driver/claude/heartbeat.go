package claude

import "spindrift.dev/launcher/internal/driver/driverkit"

// HeartbeatTracking is claude's phase, kind and family hooks for the shared
// driverkit heartbeat.
var HeartbeatTracking = driverkit.HeartbeatTracking{
	Phase: toolToPhase,
	Kind:  toolKind,
	// The live heartbeat groups by family, not exact model id: it is a
	// coarse in-flight signal, so one row per family stays readable. The
	// final per-model token table keys on the exact id and only orders by
	// family (SortModelUsage), diverging on purpose (issue #2110).
	Family: ModelFamily,
}
