package pi

import "spindrift.dev/launcher/internal/driver/driverkit"

// Strategy declares pi as a Driver. pi's main-loop events carry no role, so
// DefaultTopLevelRole is empty (issue #2092); decodeSubagent attributes each
// subagent child's messages to its agent (issue #5483). Result is the last
// assistant message only, as claude's result is: an outcome-looking line in
// mid-pass prose must not end the run (issue #5354). ResolveExit surfaces a
// failed turn the CLI hides behind exit 0. Heartbeat tracking is left zero, so
// a model is labelled by its exact id.
//
// Strategy is read-only by convention: the registry copies it at init and tests
// pass it to driverkit.New and conformance.Run, so never assign to it.
var Strategy = driverkit.Strategy{
	Name:              "pi",
	NewDecoder:        NewDecoder,
	TransientPatterns: TransientPatterns,
	Totals:            driverkit.PerCallTotals,
	WriteTool:         WriteTool,
	Result:            driverkit.LastMessage,
	ResolveExit:       ResolveExit,
	ResultEvent:       ResultEvent,
	SessionFlags:      SessionFlags,
	NewStreamFilter:   NewStreamFilter,
}
