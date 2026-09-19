package forge

import "testing"

// CODE_FORGE_REMOTE_URL commonly carries embedded credentials
// (https://oauth2:<token>@host/repo.git) for hosts without a credential helper,
// and git's own error text echoes that URL verbatim on auth and network
// failures, so the redaction has to run before those errors reach a public
// issue comment.
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
		{
			name: "literal @ in password",
			in:   "https://user:p@ssword@host/repo.git",
			want: "https://host/repo.git",
		},
		{
			name: "path segment with @, no userinfo, unchanged",
			in:   "https://host/path@ver",
			want: "https://host/path@ver",
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
