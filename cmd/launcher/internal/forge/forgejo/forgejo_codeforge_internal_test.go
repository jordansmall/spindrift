package forgejo

import (
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
