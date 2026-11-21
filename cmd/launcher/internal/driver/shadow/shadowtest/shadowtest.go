// Package shadowtest holds the native Strategy the shadow tests compare the
// fixture Connector against. It lives under internal/driver, where a native
// Strategy decoder may be built, so tests elsewhere can borrow it.
package shadowtest

import (
	"strings"

	"spindrift.dev/launcher/internal/driver/driverkit"
)

// Native returns a Strategy that decodes every non-blank line to one text event
// holding the line. The fixture driver.wasm decodes differently, so a replay of
// any log through the two diverges on every line.
func Native() driverkit.Strategy {
	return driverkit.Strategy{
		Name: "native",
		NewDecoder: func() driverkit.Decoder {
			return func(line string) []driverkit.Event {
				if strings.TrimSpace(line) == "" {
					return nil
				}
				return []driverkit.Event{{Kind: driverkit.KindText, Text: line}}
			}
		},
		ResultEvent: func(text string) ([]byte, error) { return []byte("native " + text + "\n"), nil },
	}
}
