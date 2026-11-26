package main

import (
	"maps"
	"net/http"
	"slices"
	"testing"

	"spindrift.dev/sdk/connector"
)

// hostFixedRequestHeaders mirrors the request headers the host admits with no
// declaration (connectorhost's fixed set); a header outside it must be declared
// on every Route the request reaches.
var hostFixedRequestHeaders = []string{
	"Accept", "Accept-Language", "Cache-Control", "Content-Type",
	"If-Match", "If-Modified-Since", "If-None-Match", "If-Unmodified-Since",
}

// TestRoutesDeclareExactlyTheExtraHeadersSent pins the table to the request
// helper: every Route carries the non-fixed headers it sends and no others.
func TestRoutesDeclareExactlyTheExtraHeadersSent(t *testing.T) {
	routes, err := connector.LoadRoutes()
	if err != nil {
		t.Fatalf("%v\nthe Connector's checks export the Route table: run `nix run .#connector-dev -- github flake check`", err)
	}
	var sent []string
	old := send
	t.Cleanup(func() { send = old })
	send = func(r connector.Request) (connector.Response, error) {
		for k := range r.Headers {
			k = http.CanonicalHeaderKey(k)
			if !slices.Contains(hostFixedRequestHeaders, k) && !slices.Contains(sent, k) {
				sent = append(sent, k)
			}
		}
		return connector.Response{Status: 200}, nil
	}
	if _, err := requestWith("POST", "https://api.github.com", "/x", []byte("{}"), map[string]string{"If-None-Match": `"e"`}); err != nil {
		t.Fatal(err)
	}
	slices.Sort(sent)
	for _, r := range routes {
		got := canonical(r.RequestHeaders)
		if !slices.Equal(got, sent) {
			t.Errorf("%s %s declares requestHeaders %v, the Connector sends %v", r.Method, r.Path, got, sent)
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
