package main

import (
	"flag"
	"fmt"
	"os"
	"testing"

	"spindrift.dev/sdk/connector"
)

// host admits only what the Connector's declared Route table lets through, as
// core's network host does.
var host *connector.FakeHost

// resolvedRoutes loads the Connector's declared Route table resolved against a
// forge base and repo.
func resolvedRoutes(api, repo string) ([]connector.Route, error) {
	routes, err := connector.LoadRoutes()
	if err != nil {
		return nil, err
	}
	return connector.ResolveRoutes(routes, api, repo)
}

func TestMain(m *testing.M) {
	routes, err := resolvedRoutes(tgt.API, tgt.Repo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\nthe Connector's checks export the Route table: run `nix run .#connector-dev -- github flake check`\n", err)
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
