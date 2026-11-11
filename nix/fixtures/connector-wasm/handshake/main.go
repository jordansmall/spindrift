// Command handshake is the minimal fixture Connector: it registers two
// capabilities and does nothing else.
package main

import "spindrift.dev/sdk/connector"

func init() {
	connector.Register("fixture.alpha", "fixture.beta")
}

func main() {}
