package main

import (
	"strings"
	"testing"
)

// TestNetrcCredential_SingleMachineExactMatch verifies that a netrc file
// with a single machine entry resolves the password for an exact host
// match.
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

// TestNetrcCredential_MultipleMachinesResolvesCorrectOne verifies that a
// netrc file holding entries for several distinct hosts resolves the
// password of the requested host, not just the first or last entry --
// guards against an implementation that stops at the first machine token or
// only remembers the last password seen.
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

// TestNetrcCredential_NoMatchingHostIsError verifies that a netrc file with
// no entry for the requested host fails closed with an error naming both
// the file path and the host that was looked for -- never an empty string
// with a nil error, since that would let a proxy run unauthenticated
// without any signal.
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

// TestNetrcCredential_LoginPasswordOrderDoesNotMatter verifies that a
// machine stanza with password listed before login still resolves --
// the real netrc format does not guarantee login/password ordering within
// a stanza.
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

// TestNetrcCredential_DefaultStanzaDoesNotOverwritePriorMatch verifies that
// a trailing "default" stanza never clobbers a password already resolved
// for an earlier, exactly-matching machine entry -- guards against a
// word-scanner that never recognizes the "default" token and so keeps
// attributing every later login/password pair to the last-seen machine.
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

// TestNetrcCredential_MacdefBodyIsNotWordScanned verifies that a "macdef"
// macro body -- which can contain arbitrary text, including tokens that
// look like "password fake" -- is never tokenized as ordinary netrc fields.
// A macro body appearing after the matching entry must not overwrite the
// password already resolved for it.
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

// TestNetrcCredential_DuplicateMachineFirstMatchWins verifies that when two
// machine stanzas exist for the same host, the first one wins -- matching
// real netrc consumers like curl and git, which resolve first-match rather
// than last-match.
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

// TestNetrcCredential_CommentedEntryIsIgnored verifies that a "#"-prefixed
// comment line is never word-scanned -- guards against a stale, commented-out
// machine/password stanza (the kind that accumulates in a hand-maintained
// ~/.netrc) shadowing the real entry that follows it.
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

// TestNetrcCredential_NeverEchoesSecret proves that a lookup-miss error
// never contains a real password value that happens to be in scope
// elsewhere in the netrc content -- mirrors
// TestPeekRegistryProxyCredential_NeverEchoesSecret's spirit: guards against
// a future change accidentally interpolating an entry's password into the
// "no entry for host" error message.
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

// TestNetrcCredential_HostMatchIsCaseInsensitive verifies that a "machine"
// token is matched against host case-insensitively -- matching real netrc
// consumers like curl and git, which do not require the hostname's case in
// the netrc file to agree with the case of the host being looked up.
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

// TestNetrcCredential_HostMatchedButNoPassword verifies that a matching
// "machine" stanza with no "password" token produces a distinct error from
// the no-match case -- the host does have an entry, it is just missing a
// password field, which is a different problem than no entry existing at
// all.
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

// TestNetrcCredential_ValuelessMachineTokenDoesNotLeakPriorStanza verifies
// that a "machine" token with no value on its line -- malformed input --
// clears the in-progress stanza rather than leaving currentMachine/inMachine
// pointing at whatever host preceded it. Without this, a later, unrelated
// host's password would be misattributed to the earlier machine.
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

// TestNetrcCredential_MacdefOnSameLineAsCredentialsIsNotWordScanned verifies
// that a "macdef" token stops field tokenization for the rest of its line --
// a macro body crammed onto the same line as "macdef" (e.g.
// "macdef init password MACRO-TEXT") must never be mistaken for a real
// password token, the same guarantee TestNetrcCredential_MacdefBodyIsNotWordScanned
// already gives macro bodies on their own following lines.
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

// TestNetrcCredential_SameLineDuplicatePasswordFirstWins verifies that when
// two "password" tokens appear on the same line for the matching machine,
// the first one wins -- matching real netrc consumers like curl, which
// resolve first-match rather than letting a later token on the same line
// silently clobber the one already resolved.
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

// TestNetrcCredential_TrailingCommentAfterTokensIsIgnored verifies that a
// "#" token appearing after real machine/login/password fields on the same
// line starts a comment that is truncated before tokenizing -- guards
// against a trailing annotation (e.g. "# rotate password quarterly") being
// mistaken for real netrc fields and clobbering the password already
// resolved earlier on the line.
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

// TestNetrcCredential_TrailingCommentLineDoesNotShadowLaterPassword verifies
// that a trailing "#" comment on the machine/login line is truncated before
// tokenizing, so it never leaves a stray word (e.g. "later") in the field
// stream that a naive whole-line-only comment check would miss -- the real
// password on the following line must still resolve.
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
