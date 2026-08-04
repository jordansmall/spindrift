package forgejo_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
)

// TestForgejoClient_Issue_NotFound verifies Issue() surfaces forge.ErrNotFound
// (via errors.Is) when the requested issue does not exist on the Forgejo
// instance — the one new checkable behavior slice 2 of issue #2256 introduces
// by migrating forgejoClient's own call sites onto rest.Client, whose
// StatusMap maps 404 to forge.ErrNotFound.
func TestForgejoClient_Issue_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	if _, err := fc.Issue("999"); !errors.Is(err, forge.ErrNotFound) {
		t.Fatalf("Issue() error = %v, want errors.Is(err, forge.ErrNotFound)", err)
	}
}
