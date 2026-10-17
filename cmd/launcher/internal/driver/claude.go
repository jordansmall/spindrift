package driver

import (
	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/driver/driverkit"
)

// No ResolveExit: claude's stream-json type:"result" event already carries a
// trustworthy is_error/subtype pair, so the caller's exitCode stands.
func init() {
	register(driverkit.Strategy{
		Name:                "claude",
		NewDecoder:          claude.NewDecoder,
		TransientPatterns:   claude.TransientPatterns,
		TerminalPatterns:    claude.TerminalPatterns,
		Classify:            claude.ClassifyOptions,
		Totals:              driverkit.SessionTotals,
		WriteTool:           claude.WriteTool,
		Heartbeat:           claude.HeartbeatTracking,
		DefaultTopLevelRole: driverkit.ImplementorRole,
		Result:              driverkit.ResultEvents,
		ResultEvent:         claude.ResultEvent,
		SessionFlags:        claude.SessionFlags,
	})
}
