// Command driver is the fixture Driver Connector exercising every Driver
// capability: patterns, a write tool, a stateful decoder, session flags,
// ResolveExit, classification, totals, result and heartbeat tuning.
package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"spindrift.dev/sdk/driver"
	"spindrift.dev/sdk/event"
)

func init() {
	driver.Register(driver.Strategy{
		Name:      "fixture",
		WriteTool: "Put",
		TransientPatterns: []driver.Pattern{
			{Substr: "slow down", Reason: driver.RateLimit},
		},
		TerminalPatterns: []driver.Pattern{
			{Substr: "bad flag", Reason: driver.UnsupportedFlag},
		},
		// Each decoder numbers the lines it has seen. The line "forge" makes it
		// emit an event the host must reject.
		NewDecoder: func() driver.Decoder {
			n := 0
			return func(line string) []event.Event {
				n++
				if strings.TrimSpace(line) == "forge" {
					return []event.Event{{Kind: event.KindHostOp}}
				}
				return []event.Event{{Kind: event.KindText, Text: fmt.Sprintf("%d:%s", n, line)}}
			}
		},
		ResultEvent: func(text string) ([]byte, error) { return []byte("result " + text + "\n"), nil },
		SessionFlags: func(mode, repoSlug, issue, home string) string {
			return strings.Join([]string{mode, repoSlug, issue, home}, "|")
		},
		ResolveExit: func(log []byte, exitCode int) (int, error) {
			if len(log) == 0 {
				return 0, fmt.Errorf("empty log")
			}
			return exitCode + strings.Count(string(log), "\n"), nil
		},
		Classify:            driver.ClassifyOptions{ChunkOversized: true, FirstMatch: true},
		Totals:              driver.SessionTotals,
		Result:              driver.LastMessage,
		DefaultTopLevelRole: "lead",
		Heartbeat: driver.Heartbeat{
			// Echoing the raw input proves it crosses as JSON, not as a string.
			Phase: func(tool string, input json.RawMessage) string {
				return tool + "@" + string(input)
			},
			Kind:        func(tool string) string { return strings.ToLower(tool) },
			KindOrder:   []string{"read", "edit"},
			KindPlurals: map[string]string{"search": "searches"},
			Family: func(model string) (string, int) {
				if strings.Contains(model, "opus") {
					return "opus", 0
				}
				return model, 3
			},
		},
	})
}

func main() {}
