package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"testing"

	"spindrift.dev/sdk/connector"
)

// host admits only what the Connector's declared Route table lets through, as
// core's network host does.
var host *connector.FakeHost

func TestMain(m *testing.M) {
	routes, err := connector.LoadRoutes()
	if err == nil {
		routes, err = connector.ResolveRoutes(routes, site, "ENG")
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\nthe Connector's checks export the Route table: run `nix run .#connector-dev -- jira flake check`\n", err)
		os.Exit(1)
	}
	host = connector.NewFakeHost(routes)
	code := m.Run()
	// A filtered or listing run exercises only some Routes, so only a full run can judge them.
	if code == 0 && flag.Lookup("test.run").Value.String() == "" && flag.Lookup("test.skip").Value.String() == "" && flag.Lookup("test.list").Value.String() == "" {
		for _, r := range host.Unused() {
			fmt.Fprintf(os.Stderr, "declared Route never exercised: %+v\n", r)
			code = 1
		}
	}
	os.Exit(code)
}

// dialFails makes every admitted request fail at the transport.
func dialFails(t *testing.T) {
	t.Helper()
	host.Serve(t, func(connector.Request) (connector.Response, error) {
		return connector.Response{}, errors.New("dial")
	})
}
