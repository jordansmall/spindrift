package opencode

import "spindrift.dev/launcher/internal/driver/driverkit"

// Strategy declares opencode as a Driver. The decoder attributes no subagent
// roles, so SummedByAgent is the one usage.MainLoopAgent row; its APICalls is
// what `spindrift stats --by role` reads for an opencode pass.
// DefaultTopLevelRole is empty because opencode's heartbeat carries no role
// attribution (issue #2092). Result is the last assistant message only, as
// claude's result is: an outcome-looking line in mid-pass prose must not end
// the run (issue #5354). ResolveExit surfaces an unrecovered mid-run error
// event the CLI hides behind exit 0 (issues #2263, #5391).
//
// Strategy is read-only by convention: the registry copies it at init and tests
// pass it to driverkit.New and conformance.Run, so never assign to it.
var Strategy = driverkit.Strategy{
	Name:              "opencode",
	NewDecoder:        NewDecoder,
	TransientPatterns: TransientPatterns,
	TerminalPatterns:  TerminalPatterns,
	Totals:            driverkit.PerCallTotals,
	WriteTool:         WriteTool,
	Result:            driverkit.LastMessage,
	ResolveExit:       ResolveExit,
	ResultEvent:       ResultEvent,
	SessionFlags:      SessionFlags,
}
