package forgejo

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

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

func TestCloneOutputTail(t *testing.T) {
	const maxTail = 512 // mirrors cloneOutputTail's limit

	t.Run("short output is kept whole", func(t *testing.T) {
		if got, want := cloneOutputTail([]byte("  fatal: nope\n")), ": fatal: nope"; got != want {
			t.Errorf("cloneOutputTail = %q, want %q", got, want)
		}
	})

	t.Run("blank output renders nothing", func(t *testing.T) {
		if got := cloneOutputTail([]byte(" \n")); got != "" {
			t.Errorf("cloneOutputTail = %q, want empty", got)
		}
	})

	t.Run("long output keeps only the tail", func(t *testing.T) {
		out := strings.Repeat("a", maxTail) + "END"
		got := cloneOutputTail([]byte(out))
		if want := ": ..." + out[len(out)-maxTail:]; got != want {
			t.Errorf("cloneOutputTail tail = %q, want %q", got, want)
		}
		if strings.Contains(got, "a"+strings.Repeat("a", maxTail)) {
			t.Errorf("cloneOutputTail kept more than %d bytes of output", maxTail)
		}
	})

	t.Run("a cut inside a multibyte rune advances to a rune boundary", func(t *testing.T) {
		// "é" is two bytes; the naive cut at len-maxTail lands on its
		// continuation byte.
		out := "é" + strings.Repeat("a", maxTail-1)
		if utf8.RuneStart(out[len(out)-maxTail]) {
			t.Fatal("fixture cut does not land mid-rune")
		}
		got := cloneOutputTail([]byte(out))
		if !utf8.ValidString(got) {
			t.Errorf("cloneOutputTail returned invalid UTF-8: %q", got)
		}
		if want := ": ..." + strings.Repeat("a", maxTail-1); got != want {
			t.Errorf("cloneOutputTail = %q, want %q", got, want)
		}
	})
}
