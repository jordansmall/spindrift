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

// ExtractUsage sums the per-turn tallies of every step_finish in the opencode
// NDJSON log at logPath into one usage.Report (see driverkit.ExtractUsage).
// Each step_finish is an independent per-turn tally, not a cumulative
// snapshot, so a plain sum is correct here. DurationApiMs stays zero because
// opencode logs no api-only timing. Found is false when the log has no
// step_finish event or is absent.
func ExtractUsage(logPath string) (usage.Report, error) {
	return driverkit.ExtractUsage(logPath, NewDecoder, breakdownByModel)
}
