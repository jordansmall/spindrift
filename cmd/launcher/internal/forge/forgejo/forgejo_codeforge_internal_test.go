package forgejo

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestForgejoGitRemoteURL_EmbedsTokenAsUserinfo verifies
// forgejoGitRemoteURL builds a token-authenticated git clone URL from a
// Forgejo instance base URL and owner/repo slug — the token rides as the
// URL's userinfo, the shape `git clone`/`git push` expect for HTTP(S) auth.
func TestForgejoGitRemoteURL_EmbedsTokenAsUserinfo(t *testing.T) {
	got := forgejoGitRemoteURL("https://codeberg.org", "owner/repo", "tok")
	want := "https://tok@codeberg.org/owner/repo.git"
	if got != want {
		t.Fatalf("forgejoGitRemoteURL(...) = %q, want %q", got, want)
	}
}

// TestForgejoGitRemoteURL_RedactedStripsToken verifies the constructed
// remote URL's embedded token is exactly what forge.RedactURLCredentials
// strips — so a log line built from this URL (e.g. a clone failure) never
// leaks the token.
func TestForgejoGitRemoteURL_RedactedStripsToken(t *testing.T) {
	remote := forgejoGitRemoteURL("https://codeberg.org", "owner/repo", "tok")
	redacted := forge.RedactURLCredentials(remote)
	if strings.Contains(redacted, "tok") {
		t.Fatalf("RedactURLCredentials(%q) = %q, still contains the token", remote, redacted)
	}
}

// TestForgejoGitRemoteURL_FallbackKeepsToken verifies that when baseURL
// fails to parse (here, a control character url.Parse rejects), the
// fallback branch still produces a token-authenticated remote rather than
// silently dropping the token and yielding an anonymous remote that would
// fail to push.
func TestForgejoGitRemoteURL_FallbackKeepsToken(t *testing.T) {
	got := forgejoGitRemoteURL("https://forge.test\x7f", "owner/repo", "tok")
	if !strings.Contains(got, "tok@") {
		t.Fatalf("forgejoGitRemoteURL(...) = %q, want it to contain %q (token as userinfo)", got, "tok@")
	}
}

// TestNewForgejoCodeForge_ReusesTrackerRESTClient asserts issue #2256's
// shared-client seam: when NewForgejoCodeForge is handed a tracker built by
// NewForgejoClient, the CodeForge's underlying *rest.Client is the identical
// pointer to the tracker's own -- not a second, separately constructed
// client against the same repo. Passing a nil tracker (the "different
// backend, or none configured" case) must fall back to building its own
// client instead.
func TestNewForgejoCodeForge_ReusesTrackerRESTClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	tracker := NewForgejoClient(ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	fc, ok := tracker.(*forgejoClient)
	if !ok {
		t.Fatalf("NewForgejoClient(...) = %T, want *forgejoClient", tracker)
	}

	cf := newForgejoCodeForge(ForgejoCodeForgeConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"}, tracker, "unused")
	if cf.rest != fc.rest {
		t.Fatalf("newForgejoCodeForge(..., tracker, ...).rest = %p, want the tracker's own *rest.Client %p (shared instance)", cf.rest, fc.rest)
	}

	// The shared instance must still carry a bounded HTTP timeout: when
	// NewForgejoClient built fc.rest, it must not have defaulted to the
	// untimed http.DefaultClient, since newForgejoCodeForge's own
	// locally-computed timed client is discarded in this reuse path -- a
	// hung Forgejo instance must not be able to block Probe/Merge/IssueTracker
	// calls forever (regression coverage for the shared-client seam).
	if timeout := fc.rest.HTTPClientForTest().Timeout; timeout <= 0 {
		t.Fatalf("tracker built by NewForgejoClient with no HTTPClient override: rest client Timeout = %v, want a bounded (>0) timeout", timeout)
	}

	// Sanity check the fallback: a nil tracker (or one that isn't a
	// *forgejoClient) must NOT share the same *rest.Client as some other
	// unrelated tracker -- it builds its own instead.
	cfNoTracker := newForgejoCodeForge(ForgejoCodeForgeConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"}, nil, "unused")
	if cfNoTracker.rest == fc.rest {
		t.Fatal("newForgejoCodeForge(..., nil, ...).rest unexpectedly shares the unrelated tracker's *rest.Client")
	}
}
