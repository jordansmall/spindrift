// Command driver is the fixture Driver Connector exercising every Driver
// capability: patterns, a write tool, a stateful decoder, session flags and
// ResolveExit.
package main

import (
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
	})
}

func main() {}
