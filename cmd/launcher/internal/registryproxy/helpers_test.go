package registryproxy

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	// ecosystem is test-only here: it supplies the real rewrite rows so the
	// round-trip tests exercise the rows registryproxy actually runs in
	// production, not a stand-in -- production code never imports ecosystem
	// (see the import-graph check).
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registryvocab"
)

// newProxy assigns prefixes over routes, builds a handler with rows, and
// fails the test on a construction error. It is the shared constructor
// behind newWithEcosystemRows and newPlainProxy.
func newProxy(t *testing.T, rows []registryvocab.RewriteRow, routes []Route) (http.Handler, []Route) {
	t.Helper()
	assigned := AssignPrefixes(routes)
	handler, err := New(assigned, rows)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return handler, assigned
}

// newWithEcosystemRows builds a handler over routes with every ecosystem's
// real rewrite rows -- cargo's is the only one today -- so its callers
// exercise the rows a production run uses. It returns the routes back
// because AssignPrefixes mutates them in place -- callers need the assigned
// Prefix to build request paths. It fails the test on a construction error:
// only the tests that assert on New rejecting a route care which error
// comes back, and they call New directly.
func newWithEcosystemRows(t *testing.T, routes ...Route) (http.Handler, []Route) {
	t.Helper()
	return newProxy(t, ecosystem.ResponseRewriteRows(), routes)
}

// newUpstream starts a test upstream and registers its Close on cleanup, so
// callers that deliberately close it early (e.g. to force a dial failure)
// don't need their own defer -- Close is idempotent.
func newUpstream(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(func() { srv.Close() })
	return srv
}

// newPlainProxy assigns prefixes and builds a handler with no rewrite rows.
// It returns the routes back because AssignPrefixes mutates them in place --
// callers need the assigned Prefix to build request paths.
func newPlainProxy(t *testing.T, routes ...Route) (http.Handler, []Route) {
	t.Helper()
	return newProxy(t, nil, routes)
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
