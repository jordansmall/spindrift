// Command seamtest-driver is cmd/launcher/internal/seamtest's fake Driver
// Strategy compiled as a Connector: one JSON-encoded event per line. Keep it
// in step with that Strategy.
package main

import (
	"encoding/json"
	"strings"

	"spindrift.dev/sdk/driver"
	"spindrift.dev/sdk/event"
)

func init() {
	driver.Register(driver.Strategy{
		Name:      "claude",
		WriteTool: "Write",
		NewDecoder: func() driver.Decoder {
			return func(line string) []event.Event {
				line = strings.TrimSpace(line)
				if line == "" {
					return nil
				}
				var ev event.Event
				if err := json.Unmarshal([]byte(line), &ev); err != nil || ev.Kind == "" {
					return []event.Event{{Kind: event.KindUnparsed, Text: line}}
				}
				return []event.Event{ev}
			}
		},
		ResultEvent: func(text string) ([]byte, error) {
			b, err := json.Marshal(event.Event{Kind: event.KindResult, Text: text})
			if err != nil {
				return nil, err
			}
			return append(b, '\n'), nil
		},
		SessionFlags: func(mode, repoSlug, issue, home string) string { return "" },
	})
}

func main() {}
