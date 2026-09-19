package forgejo

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// The token must be embedded as the URL's userinfo, the only shape `git clone`
// and `git push` accept for HTTP(S) auth.
func TestForgejoGitRemoteURL_EmbedsTokenAsUserinfo(t *testing.T) {
	got := forgejoGitRemoteURL("https://codeberg.org", "owner/repo", "tok")
	want := "https://tok@codeberg.org/owner/repo.git"
	if got != want {
		t.Fatalf("forgejoGitRemoteURL(...) = %q, want %q", got, want)
	}
}

// The embedded token must be exactly what forge.RedactURLCredentials strips, so
// a log line built from this URL (a clone failure, say) never leaks it.
func TestForgejoGitRemoteURL_RedactedStripsToken(t *testing.T) {
	remote := forgejoGitRemoteURL("https://codeberg.org", "owner/repo", "tok")
	redacted := forge.RedactURLCredentials(remote)
	if strings.Contains(redacted, "tok") {
		t.Fatalf("RedactURLCredentials(%q) = %q, still contains the token", remote, redacted)
	}
}

// The control character in the base URL is what makes url.Parse fail, taking
// the fallback branch. That branch must keep the token rather than yield an
// anonymous remote that would fail to push.
func TestForgejoGitRemoteURL_FallbackKeepsToken(t *testing.T) {
	got := forgejoGitRemoteURL("https://forge.test\x7f", "owner/repo", "tok")
	if !strings.Contains(got, "tok@") {
		t.Fatalf("forgejoGitRemoteURL(...) = %q, want it to contain %q (token as userinfo)", got, "tok@")
	}
}

// Issue #2256's shared-client seam: given a tracker built by NewForgejoClient,
// the CodeForge's *rest.Client must be the identical pointer, not a second
// client against the same repo. A nil tracker (different backend, or none
// configured) falls back to building its own.
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

	// The reuse path discards newForgejoCodeForge's own timed client, so the
	// timeout must come from the tracker's client. If NewForgejoClient defaulted
	// to the untimed http.DefaultClient, a hung Forgejo instance would block
	// Probe/Merge/IssueTracker calls forever.
	if timeout := fc.rest.HTTPClientForTest().Timeout; timeout <= 0 {
		t.Fatalf("tracker built by NewForgejoClient with no HTTPClient override: rest client Timeout = %v, want a bounded (>0) timeout", timeout)
	}

	cfNoTracker := newForgejoCodeForge(ForgejoCodeForgeConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"}, nil, "unused")
	if cfNoTracker.rest == fc.rest {
		t.Fatal("newForgejoCodeForge(..., nil, ...).rest unexpectedly shares the unrelated tracker's *rest.Client")
	}
}
