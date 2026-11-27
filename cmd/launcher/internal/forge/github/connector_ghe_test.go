package github

import (
	"encoding/json"
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/credsource/credsourcetest"
	"spindrift.dev/launcher/internal/forge"
)

// githubRuntimeUnderHost is a runtime over a plain github row decoded with
// GH_HOST answering host and the row's real gate, the way the launcher
// resolves it, closed with the test.
func githubRuntimeUnderHost(t *testing.T, host string) *connectorruntime.Runtime {
	t.Helper()
	routes := []connectorhost.Route{{Method: "GET", Host: "api.github.com", Path: "/repos"}}
	doc, err := json.Marshal([]backendrows.Row{{Name: "github", Module: "/x/github.wasm", Routes: routes, CredentialEnv: tokenCredentialEnv}})
	if err != nil {
		t.Fatal(err)
	}
	lookup := func(k string) string {
		if k == "GH_HOST" {
			return host
		}
		return ""
	}
	set, err := backendrows.Decode(doc, lookup, map[string]backendrows.Gate{backend.GitHub.Name: Strategy.RowGate})
	if err != nil {
		t.Fatal(err)
	}
	rt := connectorruntime.New(set, credsourcetest.EnvRows(set.All()...), testSandbox())
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

// The row's Routes pin api.github.com, so under a GitHub Enterprise GH_HOST it
// keeps none: nothing is sent to api.github.com, no connector credential is
// read, and a read fails by name rather than reaching gh.
func TestGithubRow_GHEHostKeepsNoRow(t *testing.T) {
	rt := githubRuntimeUnderHost(t, "ghe.example.com")
	t.Setenv("GH_HOST", "ghe.example.com")
	if _, ok := rt.Row("github"); ok {
		t.Fatal("the runtime holds a github row under GH_HOST=ghe.example.com")
	}
	if _, ok := routedRow(rt); ok {
		t.Fatal("routedRow reports a routed row under GH_HOST=ghe.example.com")
	}
	calls := countGH(t)
	c := NewExecClient(connectorRepo, forge.DispatchLabels{}, WithConnectors(rt))
	if _, err := c.ListOpenIssues(); !errors.Is(err, ErrNoRoute) {
		t.Errorf("ListOpenIssues error = %v, want ErrNoRoute", err)
	}
	if n := calls(); n != 0 {
		t.Errorf("ListOpenIssues ran gh %d times under a GitHub Enterprise host", n)
	}
}

func TestGithubRow_DefaultHostStillRoutes(t *testing.T) {
	for _, host := range []string{"", "github.com", "GitHub.com"} {
		t.Run(host, func(t *testing.T) {
			t.Parallel()
			if _, ok := routedRow(githubRuntimeUnderHost(t, host)); !ok {
				t.Errorf("routedRow is not routed under GH_HOST=%q", host)
			}
		})
	}
}
