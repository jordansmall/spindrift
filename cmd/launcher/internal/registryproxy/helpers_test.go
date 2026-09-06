package registryproxy

import (
	"net/http"
	"testing"

	// ecosystem is test-only here: it supplies the real rewrite rows so the
	// round-trip tests exercise the rows registryproxy actually runs in
	// production, not a stand-in -- production code never imports ecosystem
	// (see the import-graph check).
	"spindrift.dev/launcher/internal/ecosystem"
)

// newWithEcosystemRows builds a handler over routes with every ecosystem's
// real rewrite rows -- cargo's is the only one today -- so its callers
// exercise the rows a production run uses. It fails the test on a
// construction error: only the tests that assert on New rejecting a route
// care which error comes back, and they call New directly.
func newWithEcosystemRows(t *testing.T, routes []Route) http.Handler {
	t.Helper()
	handler, err := New(routes, ecosystem.ResponseRewriteRows())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return handler
}
