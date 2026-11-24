// Command connectorname is the connectorname Connector: a Driver's host half,
// compiled to a wasip1 reactor. Fill in the Strategy below; the corpus in
// testdata/corpus pins what it must produce.
package main

import (
	"time"

	"spindrift.dev/sdk/driver"
	"spindrift.dev/sdk/event"
)

func init() {
	driver.Register(driver.Strategy{
		// Must equal the Connector's name and its Driver row's name.
		Name: "connectorname",
		// Turns one line of the CLI's output into events. Every non-host line
		// becomes one text event here; decode the CLI's own stream instead.
		NewDecoder: func(time.Time) driver.Decoder {
			return func(line string) []event.Event {
				if _, ok := driver.HostLine(line); ok {
					return []event.Event{{Kind: event.KindHostOp}}
				}
				return []event.Event{{Kind: event.KindText, Text: line}}
			}
		},
		ResultEvent:  func(text string) ([]byte, error) { return []byte(text + "\n"), nil },
		SessionFlags: func(mode, repoSlug, issue, home string) driver.SessionFlag { return driver.SessionFlag{} },
		Result:       driver.LastMessage,
	})
}

// A reactor's exports are all it does; main is never called.
func main() {}
