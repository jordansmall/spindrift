// Command pi is the pi Driver's host half as a Connector (ADR 0073), ported
// from the native Strategy under cmd/launcher/internal/driver/pi, which stays
// registered and authoritative until the cut-over. The native package's corpus
// pins it, replayed through the compiled Connector by the conformance check;
// the unit tests beside the code cover exit resolution, the stream filter,
// transient patterns, the result and the session flags.
package main

import "spindrift.dev/sdk/driver"

func init() {
	driver.Register(driver.Strategy{
		Name:              "pi",
		NewDecoder:        NewDecoder,
		TransientPatterns: TransientPatterns,
		Totals:            driver.PerCallTotals,
		WriteTool:         WriteTool,
		Result:            driver.LastMessage,
		ResolveExit:       ResolveExit,
		ResultEvent:       ResultEvent,
		SessionFlags:      SessionFlags,
		NewStreamFilter:   NewStreamFilter,
	})
}

// A reactor's exports are all it does; main is never called.
func main() {}
