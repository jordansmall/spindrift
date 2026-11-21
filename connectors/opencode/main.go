// Command opencode is the opencode Driver's host half as a Connector (ADR
// 0073), ported from the native Strategy in cmd/launcher/internal/driver/opencode,
// which stays registered and authoritative. The decoder attributes no subagent
// roles, so per-role usage is the main loop's alone. DefaultTopLevelRole is
// empty because opencode names no role until a pass_start does (issue #2092).
// Result is the last assistant message only, so mid-pass prose cannot end the
// run (issue #5354). ResolveExit surfaces an unrecovered mid-run error event
// the CLI hides behind exit 0 (issues #2263, #5391).
package main

import "spindrift.dev/sdk/driver"

func init() {
	driver.Register(driver.Strategy{
		Name:              "opencode",
		NewDecoder:        newDecoder,
		TransientPatterns: TransientPatterns,
		TerminalPatterns:  TerminalPatterns,
		Totals:            driver.PerCallTotals,
		WriteTool:         WriteTool,
		Result:            driver.LastMessage,
		ResolveExit:       ResolveExit,
		ResultEvent:       ResultEvent,
		SessionFlags:      SessionFlags,
	})
}

// A reactor's exports are all it does; main is never called.
func main() {}
