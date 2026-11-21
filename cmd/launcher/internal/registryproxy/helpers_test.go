package registryproxy

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"spindrift.dev/launcher/internal/ecosystemtest/synthetic"
	"spindrift.dev/launcher/internal/registryvocab"
)

// newProxy assigns prefixes over routes and builds a handler with rows,
// failing the test on a construction error. It hands the assigned routes back
// because callers build their request paths from each Prefix.
func newProxy(t *testing.T, rows []registryvocab.RewriteRow, routes []Route) (http.Handler, []Route) {
	t.Helper()
	assigned := AssignPrefixes(routes)
	handler, err := New(assigned, rows)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return handler, assigned
}

// newWithSyntheticRow builds a handler with the synthetic ecosystem's rewrite
// row, so its callers exercise the proxy's generic rewrite machinery without
// naming a real ecosystem.
func newWithSyntheticRow(t *testing.T, routes ...Route) (http.Handler, []Route) {
	t.Helper()
	return newProxy(t, synthetic.Strategy.RewriteRows, routes)
}

// syntheticRoute builds the registry.example.com Route that the synthetic row
// rewrites for: a download URL naming that host is repointed at the proxy.
func syntheticRoute(upstream, credential string) Route {
	return Route{MatchHost: "registry.example.com", EnforcedPaths: []string{"/"}, EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "synthetic", Path: "/"}}, Upstream: upstream, Credential: credential}
}

// newUpstream starts a test upstream and registers its Close on cleanup.
// Callers may close it early (to force a dial failure): httptest.Server's
// Close is observed to be idempotent, not a documented contract.
func newUpstream(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(func() { srv.Close() })
	return srv
}

// newPlainProxy builds a handler with no rewrite rows: the plain forwarding
// path, which is what most tests are about.
func newPlainProxy(t *testing.T, routes ...Route) (http.Handler, []Route) {
	t.Helper()
	return newProxy(t, nil, routes)
}

// plainRoute builds the single-path, no-credential Route shape most tests
// route through, every other field zero.
func plainRoute(upstream string) Route {
	return Route{EnforcedPaths: []string{"/"}, Upstream: upstream}
}

// serve drives a request through p and returns the recorded response.
func serve(p http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, req)
	return rr
}

// captureLog redirects the standard logger into a buffer for the rest of t.
// The package under test logs through the standard logger with no injectable
// seam, so this swap is process-wide: no test using it may call t.Parallel().
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prevOutput) })
	return &buf
}
