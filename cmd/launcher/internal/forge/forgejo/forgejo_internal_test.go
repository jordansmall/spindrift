package forgejo

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
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

// The git credential reads FORGEJO_TOKEN each time git is run, so a rotated
// token reaches the next call and an unset one is anonymous.
func TestGitCredential_ReadsFORGEJOTOKENAtUse(t *testing.T) {
	envOf := func() []string {
		cmd := gitCredential.Apply(exec.Command("git", "ls-remote"), "https://forge.example/o/r.git")
		return cmd.Env
	}
	t.Setenv(ForgejoTokenEnv, "tok")
	env := envOf()
	for _, want := range []string{"GITEXEC_USERNAME=oauth2", "GITEXEC_PASSWORD=tok"} {
		if !slices.Contains(env, want) {
			t.Errorf("env lacks %s", want)
		}
	}
	t.Setenv(ForgejoTokenEnv, "rotated")
	if !slices.Contains(envOf(), "GITEXEC_PASSWORD=rotated") {
		t.Error("env lacks the rotated token")
	}
	t.Setenv(ForgejoTokenEnv, "")
	if env := envOf(); slices.ContainsFunc(env, func(e string) bool { return strings.HasPrefix(e, "GITEXEC_PASSWORD=") }) {
		t.Error("an unset token still reached git")
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
