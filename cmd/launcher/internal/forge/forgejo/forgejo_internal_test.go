package forgejo

import (
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
// adapter's default.
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

// The Ledger site and the clone site must name one repository URL, because the
// git credential is scoped to that URL's path and git matches whole segments.
// GitRemoteURL therefore mirrors the Connector's CloneURL: it keeps a base
// subpath and drops userinfo, query and fragment.
func TestForgejoGitRemoteURL_MatchesConnectorCloneURL(t *testing.T) {
	for name, tc := range map[string]struct{ base, want string }{
		"subpath":        {"https://host/forgejo", "https://host/forgejo/owner/repo.git"},
		"trailing slash": {"https://host/forgejo/", "https://host/forgejo/owner/repo.git"},
		"root slash":     {"https://host/", "https://host/owner/repo.git"},
		"query":          {"https://host/forgejo?x=1#f", "https://host/forgejo/owner/repo.git"},
		"userinfo":       {"https://u:p@host/forgejo", "https://host/forgejo/owner/repo.git"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := GitRemoteURL(tc.base, "owner/repo"); got != tc.want {
				t.Fatalf("GitRemoteURL(%q) = %q, want %q", tc.base, got, tc.want)
			}
		})
	}
}
