// Package connectortest is the scripted-Connector vocabulary the Connector test
// kits share: a Connector as a harness loads it, the canned answers a backend
// serves ahead of its model, the repository and token every scripted backend
// uses, and the fixture wasm and Route table builders. It names no tracker or
// forge, so connectortrackertest, connectorforgetest and connectorconformance
// build on it without depending on one another.
package connectortest

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/fixturebuild"
)

const (
	// Repo is the repository the scripted backend serves.
	Repo = "o/r"
	// Token is the bearer token the host attaches to every Connector request.
	Token = "s3cr3t-token"
)

// Scripted is one canned answer the backend serves to a request whose method
// and URL path match, ahead of its own model, for a dialect the model does not
// speak. Body may carry "{host}", answered as the request's Host, for a URL
// back to the backend.
type Scripted struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Status int    `json:"status"`
	Body   string `json:"body"`
}

// Connector is a Connector as the harness loads it: its wasm and its declared
// Route table, whatever its name.
type Connector struct {
	Wasm []byte
	// Routes is the declared table: any host, and "{repo}" for the repository.
	// The harness rebinds every host to the backend's and substitutes Repo.
	Routes []connectorhost.Route
	// Script is served ahead of the backend's model.
	Script []Scripted
}

// ServeScript answers r from the first scripted entry matching its method and
// path, and reports whether it did.
func ServeScript(w http.ResponseWriter, r *http.Request, script []Scripted) bool {
	for _, s := range script {
		if s.Method == r.Method && s.Path == r.URL.Path {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(s.Status)
			_, _ = w.Write([]byte(strings.ReplaceAll(s.Body, "{host}", r.Host)))
			return true
		}
	}
	return false
}

// BoundRoutes is c's declared Routes with Repo substituted and every host
// rebound to the backend's.
func (c Connector) BoundRoutes(host string) []connectorhost.Route {
	routes := connectortracker.Routes(c.Routes, Repo)
	for i := range routes {
		routes[i].Host = host
	}
	return routes
}

// FixtureWasm reads the named fixture Connector wasm file.
func FixtureWasm(t *testing.T, wasmFile string) []byte {
	t.Helper()
	dir, err := fixturebuild.ConnectorDir()
	if err != nil {
		t.Fatal(err)
	}
	wasm, err := os.ReadFile(filepath.Join(dir, wasmFile))
	if err != nil {
		t.Fatal(err)
	}
	return wasm
}

// RoutePerMethod is the Route table of a Connector that declares one Route per
// method over the repository's subtree on host, the shape every scripted
// forgejo-shaped backend serves. nix/fixtures/connector-conformance.nix builds
// the same table in Nix.
func RoutePerMethod(host string, methods ...string) []connectorhost.Route {
	var routes []connectorhost.Route
	for _, m := range methods {
		routes = append(routes, connectorhost.Route{Method: m, Host: host, Path: "/api/v1/repos/{repo}"})
	}
	return routes
}
