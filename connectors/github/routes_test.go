package main

import (
	"maps"
	"net/http"
	"slices"
	"testing"

	"spindrift.dev/sdk/connector"
)

// TestRoutesDeclareOnlyHeadersSent pins the table to the request helper: no
// Route declares a request header the Connector never sends. The other
// direction, a sent header left undeclared, is the fake host's refusal of the
// request itself.
func TestRoutesDeclareOnlyHeadersSent(t *testing.T) {
	routes, err := connector.LoadRoutes()
	if err != nil {
		t.Fatalf("%v\nthe Connector's checks export the Route table: run `nix run .#connector-dev -- github flake check`", err)
	}
	var sent []string
	host.Serve(t, func(r connector.Request) (connector.Response, error) {
		for k := range r.Headers {
			sent = append(sent, http.CanonicalHeaderKey(k))
		}
		return connector.Response{Status: 200}, nil
	})
	if _, err := sendWith("POST", "https://api.github.com", "/repos/o/r/issues", map[string]string{}, nil, map[string]string{"If-None-Match": `"e"`}); err != nil {
		t.Fatal(err)
	}
	for _, r := range routes {
		for _, h := range canonical(r.RequestHeaders) {
			if !slices.Contains(sent, h) {
				t.Errorf("%s %s declares requestHeader %q, the Connector sends %v", r.Method, r.Path, h, sent)
			}
		}
	}
}

// TestRoutesDeclareTheScopesHeaderOnGraphQLOnly pins the one non-fixed response
// header the Connector reads, X-OAuth-Scopes, to the Route its read rides.
func TestRoutesDeclareTheScopesHeaderOnGraphQLOnly(t *testing.T) {
	routes, err := connector.LoadRoutes()
	if err != nil {
		t.Fatalf("%v\nthe Connector's checks export the Route table: run `nix run .#connector-dev -- github flake check`", err)
	}
	for _, r := range routes {
		want := []string(nil)
		if r.Method == "POST" && r.Path == "/graphql" {
			want = []string{"X-OAuth-Scopes"}
		}
		if !slices.Equal(r.ResponseHeaders, want) {
			t.Errorf("%s %s declares responseHeaders %v, want %v", r.Method, r.Path, r.ResponseHeaders, want)
		}
	}
}

// canonical returns names in the form the stub records, sorted.
func canonical(names []string) []string {
	var out []string
	for _, n := range names {
		out = append(out, http.CanonicalHeaderKey(n))
	}
	slices.Sort(out)
	return out
}

// TestGraphQLRouteDeclaresExactlyTheDocumentsSent pins the table to the Go
// documents map: only POST /graphql carries documents, and they are the texts
// the Connector sends, byte for byte.
func TestGraphQLRouteDeclaresExactlyTheDocumentsSent(t *testing.T) {
	routes, err := connector.LoadRoutes()
	if err != nil {
		t.Fatalf("%v\nthe Connector's checks export the Route table: run `nix run .#connector-dev -- github flake check`", err)
	}
	found := false
	for _, r := range routes {
		if r.Method == "POST" && r.Path == "/graphql" {
			found = true
			if !maps.Equal(r.Documents, documents) {
				t.Errorf("POST /graphql declares documents %v, the Connector sends %v", r.Documents, documents)
			}
		} else if len(r.Documents) != 0 {
			t.Errorf("%s %s declares documents %v, want none", r.Method, r.Path, r.Documents)
		}
	}
	if !found {
		t.Error("the Route table has no POST /graphql route")
	}
}
