package github

import (
	"encoding/json"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
)

// useGithubRowUnderHost applies a plain github row with GH_HOST answering host,
// the way the launcher resolves it for backendrows.Use.
func useGithubRowUnderHost(t *testing.T, host string) {
	t.Helper()
	routes := []connectorhost.Route{{Method: "GET", Host: "api.github.com", Path: "/repos"}}
	doc, err := json.Marshal([]backendrows.Row{{Name: "github", Module: "/x/github.wasm", Routes: routes, CredentialEnv: tokenCredentialEnv}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backendrows.Snapshot())
	lookup := func(k string) string {
		if k == "GH_HOST" {
			return host
		}
		return ""
	}
	if err := backendrows.Use(doc, lookup); err != nil {
		t.Fatal(err)
	}
}

// The row's Routes pin api.github.com, so under a GitHub Enterprise GH_HOST it
// applies none and the host's gh, which honours GH_HOST, answers: nothing is
// sent to api.github.com and no connector credential is read.
func TestGithubRow_GHEHostLeavesNativeGHInCharge(t *testing.T) {
	useGithubRowUnderHost(t, "ghe.example.com")
	if _, ok := backendrows.Lookup("github"); ok {
		t.Fatal("the github row is applied under GH_HOST=ghe.example.com")
	}
	if _, ok := routedRow(); ok {
		t.Fatal("routedRow reports a routed row under GH_HOST=ghe.example.com")
	}
	calls := countGH(t)
	c := NewExecClient(connectorRepo, forge.DispatchLabels{})
	_, _ = c.ListOpenIssues()
	if n := calls(); n == 0 {
		t.Error("ListOpenIssues never reached gh under a GitHub Enterprise host")
	}
}

func TestGithubRow_DefaultHostStillRoutes(t *testing.T) {
	for _, host := range []string{"", "github.com", "GitHub.com"} {
		t.Run(host, func(t *testing.T) {
			useGithubRowUnderHost(t, host)
			if _, ok := routedRow(); !ok {
				t.Errorf("routedRow is not routed under GH_HOST=%q", host)
			}
		})
	}
}
