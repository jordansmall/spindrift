// Command claude is the claude Driver's host half as a Connector (ADR 0073),
// ported from the native Strategy, which issue #6799 deleted: this Connector is
// now the only claude Driver. The corpus under testdata and its tests
// (decode_test.go, classify_test.go, session_test.go for the session flags,
// dogfood_corpus_test.go) pin it.
package main

import "spindrift.dev/sdk/driver"

func init() {
	driver.Register(driver.Strategy{
		Name:              "claude",
		NewDecoder:        newDecoderAt,
		TransientPatterns: TransientPatterns,
		TerminalPatterns:  TerminalPatterns,
		Classify:          ClassifyOptions,
		Totals:            driver.SessionTotals,
		WriteTool:         WriteTool,
		Heartbeat: driver.Heartbeat{
			Phase:       toolToPhase,
			Kind:        toolKind,
			KindOrder:   []string{"read", "edit", "grep", "search", "bash", "subagent"},
			KindPlurals: map[string]string{"search": "searches", "bash": "bashes"},
			// The live heartbeat groups by family, not exact model id: it is a
			// coarse in-flight signal, so one row per family stays readable
			// (issue #2110).
			Family: ModelFamily,
		},
		DefaultTopLevelRole: driver.ImplementorRole,
		Result:              driver.ResultEvents,
		ResultEvent:         ResultEvent,
		SessionFlags:        SessionFlags,
	})
}

// A reactor's exports are all it does; main is never called.
func main() {}
