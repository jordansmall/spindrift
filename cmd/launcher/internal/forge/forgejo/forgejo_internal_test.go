package forgejo

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/gitexec"
)

// The git remote carries no userinfo: the token travels out of band as a
// gitexec.Credential, so no git argv, config or error text ever holds it.
func TestForgejoGitRemoteURL_CarriesNoUserinfo(t *testing.T) {
	got := GitRemoteURL("https://codeberg.org", "owner/repo")
	want := "https://codeberg.org/owner/repo.git"
	if got != want {
		t.Fatalf("GitRemoteURL(...) = %q, want %q", got, want)
	}
}

// An unparseable base takes the concatenating fallback, which must stay
// credential-free too.
func TestForgejoGitRemoteURL_FallbackCarriesNoUserinfo(t *testing.T) {
	got := GitRemoteURL("https://forge.test\x7f", "owner/repo")
	if strings.Contains(got, "@") {
		t.Fatalf("GitRemoteURL(...) = %q, want no userinfo", got)
	}
	if want := "https://forge.test\x7f/owner/repo.git"; got != want {
		t.Fatalf("GitRemoteURL(...) = %q, want %q", got, want)
	}
}

func TestGitCredential(t *testing.T) {
	if got, want := GitCredential("tok"), (gitexec.Credential{Username: "oauth2", Password: "tok"}); got != want {
		t.Errorf("GitCredential(tok) = %+v, want %+v", got, want)
	}
	if got := GitCredential(""); got != (gitexec.Credential{}) {
		t.Errorf("GitCredential(\"\") = %+v, want the zero Credential", got)
	}

}

// An unset or unrecognized method falls back to "rebase", matching the github
// adapter's mergeMethodFlag default.
func TestMergeStyle(t *testing.T) {
	tests := []struct {
		method string
		want   string
	}{
		{"merge", "merge"},
		{"squash", "squash"},
		{"rebase", "rebase"},
		{"", "rebase"},
		{"bogus", "rebase"},
	}
	for _, tt := range tests {
		if got := MergeStyle(tt.method); got != tt.want {
			t.Errorf("MergeStyle(%q) = %q, want %q", tt.method, got, tt.want)
		}
	}
}

// The tracker's client must carry a bounded timeout when NewForgejoClient is
// given no HTTPClient override: a hung Forgejo instance would otherwise block
// IssueTracker calls forever.
func TestNewForgejoClient_DefaultClientIsTimeBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	tracker := NewForgejoClient(ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	fc, ok := tracker.(*forgejoClient)
	if !ok {
		t.Fatalf("NewForgejoClient(...) = %T, want *forgejoClient", tracker)
	}
	if timeout := fc.rest.HTTPClientForTest().Timeout; timeout <= 0 {
		t.Fatalf("rest client Timeout = %v, want a bounded (>0) timeout", timeout)
	}
}
