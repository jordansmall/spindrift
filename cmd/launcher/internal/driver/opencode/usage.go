package opencode

import (
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/usage"
)

// breakdownByModel indirects so tests can simulate an I/O error without a real
// filesystem race against the usage aggregation scan.
var breakdownByModel = func(p string) ([]usage.ModelUsage, error) {
	return driverkit.UsageByModel(p, NewDecoder)
}

// ExtractUsage sums the step_finish tallies in the log at logPath (see
// driverkit.ExtractUsage). The decoder attributes no subagent roles, so
// SummedByAgent is the one usage.MainLoopAgent row; its APICalls is what
// `spindrift stats --by role` reads for an opencode pass.
func ExtractUsage(logPath string) (usage.Report, error) {
	return driverkit.ExtractUsage(logPath, NewDecoder, breakdownByModel)
}
