package forge

import (
	"strings"
	"testing"
	"unicode/utf8"
)

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

func TestRedactedOutputTail(t *testing.T) {
	const maxTail = 512 // mirrors RedactedOutputTail's limit

	t.Run("short output is kept whole", func(t *testing.T) {
		if got, want := RedactedOutputTail([]byte("  fatal: nope\n")), ": fatal: nope"; got != want {
			t.Errorf("RedactedOutputTail = %q, want %q", got, want)
		}
	})

	t.Run("blank output renders nothing", func(t *testing.T) {
		if got := RedactedOutputTail([]byte(" \n")); got != "" {
			t.Errorf("RedactedOutputTail = %q, want empty", got)
		}
	})

	t.Run("long output keeps only the tail", func(t *testing.T) {
		out := strings.Repeat("a", maxTail) + "END"
		got := RedactedOutputTail([]byte(out))
		if want := ": ..." + out[len(out)-maxTail:]; got != want {
			t.Errorf("RedactedOutputTail tail = %q, want %q", got, want)
		}
		if strings.Contains(got, "a"+strings.Repeat("a", maxTail)) {
			t.Errorf("RedactedOutputTail kept more than %d bytes of output", maxTail)
		}
	})

	t.Run("a cut inside a multibyte rune advances to a rune boundary", func(t *testing.T) {
		// "é" is two bytes; the naive cut at len-maxTail lands on its
		// continuation byte.
		out := "é" + strings.Repeat("a", maxTail-1)
		if utf8.RuneStart(out[len(out)-maxTail]) {
			t.Fatal("fixture cut does not land mid-rune")
		}
		got := RedactedOutputTail([]byte(out))
		if !utf8.ValidString(got) {
			t.Errorf("RedactedOutputTail returned invalid UTF-8: %q", got)
		}
		if want := ": ..." + strings.Repeat("a", maxTail-1); got != want {
			t.Errorf("RedactedOutputTail = %q, want %q", got, want)
		}
	})
}
