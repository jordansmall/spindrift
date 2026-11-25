// Command handshake is the minimal fixture Connector: it registers its
// capabilities and does nothing else.
package main

import (
	"strings"

	"spindrift.dev/sdk/connector"
)

// caps is the comma-separated capability list; the build overrides it
// (-X=main.caps=...) so one source stands in for a Connector of any axis.
var caps = "fixture.alpha,fixture.beta"

func init() {
	connector.Register(strings.Split(caps, ",")...)
}

func main() {}
