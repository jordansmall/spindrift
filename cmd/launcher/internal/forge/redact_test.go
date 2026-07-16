package forge

import "testing"

// TestRedactURLCredentials_StripsUserinfo verifies that a URL's embedded
// userinfo (user:pass@) is stripped wherever it occurs in a string, while
// the rest of the string — including the host/path — is left untouched.
// CODE_FORGE_REMOTE_URL commonly carries embedded credentials
// (https://oauth2:<token>@host/repo.git) for hosts without a credential
// helper, and git's own error text echoes that URL verbatim on auth/network
// failures — this guards the redaction those errors need before they reach
// a public issue comment.
func TestRedactURLCredentials_StripsUserinfo(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "bare url with token",
			in:   "https://oauth2:sometoken@git.example.com/org/repo.git",
			want: "https://git.example.com/org/repo.git",
		},
		{
			name: "embedded in git error text",
			in:   "fatal: unable to access 'https://user:secrettoken123@127.0.0.1:1/repo.git/': Failed to connect",
			want: "fatal: unable to access 'https://127.0.0.1:1/repo.git/': Failed to connect",
		},
		{
			name: "no credentials, unchanged",
			in:   "git clone https://git.example.com/org/repo.git: exit status 128",
			want: "git clone https://git.example.com/org/repo.git: exit status 128",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RedactURLCredentials(tc.in); got != tc.want {
				t.Errorf("RedactURLCredentials(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
