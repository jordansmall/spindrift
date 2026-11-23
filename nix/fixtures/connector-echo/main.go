// Command echo is a test-only Driver Connector named after no core Driver. It
// exists so connectorChecks' any-name path has a Connector to run the
// conformance kit against, over the corpus in testdata/corpus.
package main

import (
	"time"

	"spindrift.dev/sdk/driver"
	"spindrift.dev/sdk/event"
)

func init() {
	driver.Register(driver.Strategy{
		Name: "echo",
		// Every non-host line becomes one text event.
		NewDecoder: func(time.Time) driver.Decoder {
			return func(line string) []event.Event {
				if _, ok := driver.HostLine(line); ok {
					return []event.Event{{Kind: event.KindHostOp}}
				}
				return []event.Event{{Kind: event.KindText, Text: line}}
			}
		},
		ResultEvent:  func(text string) ([]byte, error) { return []byte(text + "\n"), nil },
		SessionFlags: func(mode, repoSlug, issue, home string) string { return "" },
		Result:       driver.LastMessage,
	})
}

func main() {}
