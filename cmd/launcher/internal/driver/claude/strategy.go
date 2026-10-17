package claude

import "spindrift.dev/launcher/internal/driver/driverkit"

// Strategy declares claude as a Driver. No ResolveExit: claude's stream-json
// type:"result" event already carries a trustworthy is_error/subtype pair, so
// the caller's exitCode stands.
//
// Strategy is read-only by convention: the registry copies it at init and tests
// pass it to driverkit.New and conformance.Run, so never assign to it.
var Strategy = driverkit.Strategy{
	Name:                "claude",
	NewDecoder:          NewDecoder,
	TransientPatterns:   TransientPatterns,
	TerminalPatterns:    TerminalPatterns,
	Classify:            ClassifyOptions,
	Totals:              driverkit.SessionTotals,
	WriteTool:           WriteTool,
	Heartbeat:           HeartbeatTracking,
	DefaultTopLevelRole: driverkit.ImplementorRole,
	Result:              driverkit.ResultEvents,
	ResultEvent:         ResultEvent,
	SessionFlags:        SessionFlags,
}
