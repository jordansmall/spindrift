package driver

import (
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/opencode"
)

// The decoder attributes no subagent roles, so SummedByAgent is the one
// usage.MainLoopAgent row; its APICalls is what `spindrift stats --by role`
// reads for an opencode pass. DefaultTopLevelRole is empty because opencode's
// heartbeat carries no role attribution (issue #2092). Result is the last
// assistant message only, as claude's result is: an outcome-looking line in
// mid-pass prose must not end the run (issue #5354). ResolveExit surfaces an
// unrecovered mid-run error event the CLI hides behind exit 0 (issues #2263,
// #5391).
func init() {
	register(driverkit.Strategy{
		Name:              "opencode",
		NewDecoder:        opencode.NewDecoder,
		TransientPatterns: opencode.TransientPatterns,
		TerminalPatterns:  opencode.TerminalPatterns,
		Totals:            driverkit.PerCallTotals,
		WriteTool:         opencode.WriteTool,
		Result:            driverkit.LastMessage,
		ResolveExit:       opencode.ResolveExit,
		ResultEvent:       opencode.ResultEvent,
		SessionFlags:      opencode.SessionFlags,
	})
}
