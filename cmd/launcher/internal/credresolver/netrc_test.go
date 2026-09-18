package credresolver

import (
	"strings"
	"testing"
)

func TestNetrcCredential_SingleMachineExactMatch(t *testing.T) {
	content := []byte("machine example.com\nlogin alice\npassword s3kr3t\n")

	got, err := netrcCredential(content, "/some/netrc", "example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "s3kr3t" {
		t.Errorf("got %q, want %q", got, "s3kr3t")
	}
}

// Guards against a parser that stops at the first machine token or only
// remembers the last password it saw. The requested host is deliberately the
// middle one.
func TestNetrcCredential_MultipleMachinesResolvesCorrectOne(t *testing.T) {
	content := []byte(
		"machine first.example.com\nlogin alice\npassword first-pass\n" +
			"machine second.example.com\nlogin bob\npassword second-pass\n" +
			"machine third.example.com\nlogin carol\npassword third-pass\n",
	)

	got, err := netrcCredential(content, "/some/netrc", "second.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "second-pass" {
		t.Errorf("got %q, want %q", got, "second-pass")
	}
}

// A miss must fail closed. An empty string with a nil error would let the
// proxy run unauthenticated with no signal, so the error has to name the path
// and the host.
func TestNetrcCredential_NoMatchingHostIsError(t *testing.T) {
	content := []byte("machine other.example.com\nlogin alice\npassword s3kr3t\n")
	const path = "/some/netrc"
	const host = "missing.example.com"

	_, err := netrcCredential(content, path, host)
	if err == nil {
		t.Fatal("expected error for host with no matching entry, got nil")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("expected error to mention the path %q, got: %v", path, err)
	}
	if !strings.Contains(err.Error(), host) {
		t.Errorf("expected error to mention the host %q, got: %v", host, err)
	}
}

// The netrc format does not fix the order of login and password within a
// stanza, so the fixture lists password first.
func TestNetrcCredential_LoginPasswordOrderDoesNotMatter(t *testing.T) {
	content := []byte("machine example.com\npassword s3kr3t\nlogin alice\n")

	got, err := netrcCredential(content, "/some/netrc", "example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "s3kr3t" {
		t.Errorf("got %q, want %q", got, "s3kr3t")
	}
}

// Guards against a word scanner that never recognizes the "default" token and
// so keeps attributing every later login/password pair to the last machine it
// saw.
func TestNetrcCredential_DefaultStanzaDoesNotOverwritePriorMatch(t *testing.T) {
	content := []byte(
		"machine registry.example.com\nlogin someone\npassword real\n\n" +
			"default\nlogin anon\npassword anonpw\n",
	)

	got, err := netrcCredential(content, "/some/netrc", "registry.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "real" {
		t.Errorf("got %q, want %q", got, "real")
	}
}

// A macdef body holds arbitrary text, including words that look like real
// netrc fields, so the parser must not tokenize it. The fixture puts the macro
// after the matching entry, where a naive scanner would overwrite the password.
func TestNetrcCredential_MacdefBodyIsNotWordScanned(t *testing.T) {
	content := []byte(
		"machine registry.example.com\nlogin someone\npassword real\n\n" +
			"macdef init\npassword fake\n\n",
	)

	got, err := netrcCredential(content, "/some/netrc", "registry.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "real" {
		t.Errorf("got %q, want %q", got, "real")
	}
}

// curl and git resolve the first matching stanza, not the last, so duplicates
// for one host must keep the earlier password.
func TestNetrcCredential_DuplicateMachineFirstMatchWins(t *testing.T) {
	content := []byte(
		"machine host\nlogin a\npassword A\n\n" +
			"machine host\nlogin b\npassword B\n",
	)

	got, err := netrcCredential(content, "/some/netrc", "host")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "A" {
		t.Errorf("got %q, want %q", got, "A")
	}
}

// Guards against a stale commented-out stanza, the kind that accumulates in a
// hand-maintained ~/.netrc, shadowing the real entry that follows it.
func TestNetrcCredential_CommentedEntryIsIgnored(t *testing.T) {
	content := []byte(
		"# machine registry.example.com password OLD-REVOKED\n" +
			"machine registry.example.com\nlogin someone\npassword real\n",
	)

	got, err := netrcCredential(content, "/some/netrc", "registry.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "real" {
		t.Errorf("got %q, want %q", got, "real")
	}
}

// Guards against a later change interpolating some other entry's password into
// the miss error. The fixture holds a password for a host nobody asks for.
func TestNetrcCredential_NeverEchoesSecret(t *testing.T) {
	const secret = "s3kr3t-do-not-echo"
	content := []byte("machine other.example.com\nlogin alice\npassword " + secret + "\n")

	_, err := netrcCredential(content, "/some/netrc", "missing.example.com")
	if err == nil {
		t.Fatal("expected error for host with no matching entry, got nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error must never echo a password value, got: %v", err)
	}
}

// curl and git do not require the case in the netrc file to agree with the case
// of the host being looked up.
func TestNetrcCredential_HostMatchIsCaseInsensitive(t *testing.T) {
	content := []byte("machine Registry.Example.com\nlogin alice\npassword s3kr3t\n")

	got, err := netrcCredential(content, "/some/netrc", "registry.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "s3kr3t" {
		t.Errorf("got %q, want %q", got, "s3kr3t")
	}
}

// A stanza that matches but carries no password is a different problem than no
// entry at all, so the two errors must stay distinguishable.
func TestNetrcCredential_HostMatchedButNoPassword(t *testing.T) {
	content := []byte("machine example.com\nlogin alice\n")
	const path = "/some/netrc"
	const host = "example.com"

	_, err := netrcCredential(content, path, host)
	if err == nil {
		t.Fatal("expected error for host with a matching entry but no password, got nil")
	}
	if strings.Contains(err.Error(), "has no entry for host") {
		t.Errorf("expected a distinct error for a matched host missing a password, got: %v", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("expected error to mention the path %q, got: %v", path, err)
	}
	if !strings.Contains(err.Error(), host) {
		t.Errorf("expected error to mention the host %q, got: %v", host, err)
	}
}

// A valueless "machine" token, which is malformed input, must clear the
// in-progress stanza. If it leaves currentMachine pointing at the preceding
// host, an unrelated host's password gets misattributed to it.
func TestNetrcCredential_ValuelessMachineTokenDoesNotLeakPriorStanza(t *testing.T) {
	content := []byte(
		"machine registry.example.com\n" +
			"machine\n" +
			"evil.example.com\n" +
			"login e\n" +
			"password EVIL-OTHER-HOST\n",
	)

	_, err := netrcCredential(content, "/some/netrc", "registry.example.com")
	if err == nil {
		t.Fatal("expected error, got a resolved credential from an unrelated host's stanza")
	}
}

// A macdef token stops tokenizing for the rest of its own line, not just the
// lines that follow it, so a macro body crammed onto the macdef line cannot
// pass for a real password token.
func TestNetrcCredential_MacdefOnSameLineAsCredentialsIsNotWordScanned(t *testing.T) {
	content := []byte(
		"machine registry.example.com\n" +
			"macdef init password MACRO-TEXT\n\n",
	)

	_, err := netrcCredential(content, "/some/netrc", "registry.example.com")
	if err == nil {
		t.Fatal("expected error, got a resolved credential from macro body text")
	}
}

// curl resolves the first password token, so a second one on the same line must
// not clobber it.
func TestNetrcCredential_SameLineDuplicatePasswordFirstWins(t *testing.T) {
	content := []byte("machine h password A password B\n")

	got, err := netrcCredential(content, "/some/netrc", "h")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "A" {
		t.Errorf("got %q, want %q", got, "A")
	}
}

// A "#" after real fields on the same line starts a comment. The fixture's
// annotation contains the word "password", which a parser that only truncates
// whole comment lines would read as a field and let clobber the real token.
func TestNetrcCredential_TrailingCommentAfterTokensIsIgnored(t *testing.T) {
	content := []byte(
		"machine registry.example.com login bot password ghp_REALTOKEN # rotate password quarterly\n",
	)

	got, err := netrcCredential(content, "/some/netrc", "registry.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "ghp_REALTOKEN" {
		t.Errorf("got %q, want %q", got, "ghp_REALTOKEN")
	}
}

// A whole-line-only comment check would leave the trailing annotation in the
// field stream, and first-match-wins would then resolve its last word as the
// password instead of the real one on the next line.
func TestNetrcCredential_TrailingCommentLineDoesNotShadowLaterPassword(t *testing.T) {
	content := []byte(
		"machine registry.example.com login bot # set password later\n" +
			"password ghp_REAL\n",
	)

	got, err := netrcCredential(content, "/some/netrc", "registry.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "ghp_REAL" {
		t.Errorf("got %q, want %q", got, "ghp_REAL")
	}
}
