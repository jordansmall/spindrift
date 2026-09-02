package credresolver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNpmrcAuthToken_ResolvesTokenForMatchHost verifies that an npmrc file
// with a "//host/:_authToken=" entry for the requested host resolves the
// token value.
func TestNpmrcAuthToken_ResolvesTokenForMatchHost(t *testing.T) {
	content := []byte("//registry.example.com/:_authToken=s3kr3t\n")

	got, err := npmrcAuthToken(content, "/some/npmrc", "registry.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "s3kr3t" {
		t.Errorf("got %q, want %q", got, "s3kr3t")
	}
}

// TestNpmrcAuthToken_NoMatchingHostIsError verifies that an npmrc file with
// no entry for the requested host fails closed with an error naming both the
// file path and the host that was looked for -- never an empty string with a
// nil error, since that would let a proxy run unauthenticated without any
// signal.
func TestNpmrcAuthToken_NoMatchingHostIsError(t *testing.T) {
	content := []byte("//other.example.com/:_authToken=s3kr3t\n")
	const path = "/some/npmrc"
	const host = "missing.example.com"

	_, err := npmrcAuthToken(content, path, host)
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

// TestNew_NpmrcFormatMissingFileReportsReadingError verifies that New's
// "npmrc" dispatch reports a "reading ... file" error for a missing file --
// like every other file adapter, the file-existence check must run before
// any npmrc-specific parsing or the missing-match-host guard.
func TestNew_NpmrcFormatMissingFileReportsReadingError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.npmrc")

	r := New(Config{FromFile: path, FileFormat: "npmrc", MatchHost: "registry.example.com"})
	_, err := r.Peek()
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), "reading") {
		t.Errorf("expected a \"reading ... file\" error, got: %v", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("expected error to mention the path %q, got: %v", path, err)
	}
}

// TestNew_NpmrcFormatEmptyMatchHostIsError verifies that New's "npmrc"
// dispatch fails closed, naming the route-flavored reason, when the route
// has no match host to key on -- npmrc has no other host source (unlike
// netrc, which falls back to the route's upstream-base-url).
func TestNew_NpmrcFormatEmptyMatchHostIsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "npmrc")
	if err := os.WriteFile(path, []byte("//registry.example.com/:_authToken=s3kr3t\n"), 0o600); err != nil {
		t.Fatalf("writing test npmrc file: %v", err)
	}

	r := New(Config{FromFile: path, FileFormat: "npmrc", MatchHost: ""})
	_, err := r.Peek()
	if err == nil {
		t.Fatal("expected error for empty match host, got nil")
	}
	if !strings.Contains(err.Error(), "match host") {
		t.Errorf("expected error to mention the missing match host, got: %v", err)
	}
}

// TestNpmrcAuthToken_HostMatchIsCaseInsensitivePortAndPathTolerant verifies
// that a registry spec matches host case-insensitively, ignores a path
// segment following the host, and ignores a ":port" present on either side
// of the comparison.
func TestNpmrcAuthToken_HostMatchIsCaseInsensitivePortAndPathTolerant(t *testing.T) {
	content := []byte("//Registry.Example.com:8080/api/npm/npm/:_authToken=s3kr3t\n")

	got, err := npmrcAuthToken(content, "/some/npmrc", "registry.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "s3kr3t" {
		t.Errorf("got %q, want %q", got, "s3kr3t")
	}
}

// TestNpmrcAuthToken_QuotedValueIsUnquoted verifies that a value wrapped in
// double quotes has the quotes stripped -- npm accepts (and sometimes
// writes) quoted values.
func TestNpmrcAuthToken_QuotedValueIsUnquoted(t *testing.T) {
	content := []byte(`//registry.example.com/:_authToken="s3kr3t"` + "\n")

	got, err := npmrcAuthToken(content, "/some/npmrc", "registry.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "s3kr3t" {
		t.Errorf("got %q, want %q", got, "s3kr3t")
	}
}

// TestNpmrcAuthToken_EmptyValueIsError verifies that a matching entry whose
// value is empty fails closed with an error naming the file and host,
// rather than resolving to an empty credential.
func TestNpmrcAuthToken_EmptyValueIsError(t *testing.T) {
	content := []byte("//registry.example.com/:_authToken=\n")
	const path = "/some/npmrc"
	const host = "registry.example.com"

	_, err := npmrcAuthToken(content, path, host)
	if err == nil {
		t.Fatal("expected error for empty _authToken value, got nil")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("expected error to mention the path %q, got: %v", path, err)
	}
	if !strings.Contains(err.Error(), host) {
		t.Errorf("expected error to mention the host %q, got: %v", host, err)
	}
}

// TestNpmrcAuthToken_LineWithNoSlashAfterHostIsSkipped verifies that a
// "//"-prefixed line with no "/" anywhere after the leading "//" (so there
// is no registry-spec/path-and-key split point at all) is skipped rather
// than mis-parsed -- the lookup falls through to the "no entry" error.
func TestNpmrcAuthToken_LineWithNoSlashAfterHostIsSkipped(t *testing.T) {
	content := []byte("//registry.example.com:_authToken=tok\n")
	const path = "/some/npmrc"
	const host = "registry.example.com"

	_, err := npmrcAuthToken(content, path, host)
	if err == nil {
		t.Fatal("expected error for a line with no slash after the host, got nil")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("expected error to mention the path %q, got: %v", path, err)
	}
	if !strings.Contains(err.Error(), host) {
		t.Errorf("expected error to mention the host %q, got: %v", host, err)
	}
}

// TestNpmrcAuthToken_IPv6HostsWithDistinctAddressesDoNotCrossMatch verifies
// that two bracketed IPv6 match hosts differing only after the first ":"
// (e.g. "[fe80::1]" vs "[fe80::2]") are never conflated -- npmrcHostname
// must strip only a trailing ":port", not truncate at the first ":" inside
// the address itself.
func TestNpmrcAuthToken_IPv6HostsWithDistinctAddressesDoNotCrossMatch(t *testing.T) {
	content := []byte("//[fe80::2]:8080/:_authToken=other-tok\n")

	_, err := npmrcAuthToken(content, "/some/npmrc", "[fe80::1]")
	if err == nil {
		t.Fatal("expected error: entry is for [fe80::2], not [fe80::1], so it must not resolve")
	}
}

// TestNpmrcAuthToken_IPv6HostWithPortResolves verifies that a bracketed IPv6
// registry spec with a ":port" suffix still resolves for the bracketed
// address alone, once the port is correctly stripped.
func TestNpmrcAuthToken_IPv6HostWithPortResolves(t *testing.T) {
	content := []byte("//[fe80::1]:4873/:_authToken=tok\n")

	got, err := npmrcAuthToken(content, "/some/npmrc", "[fe80::1]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "tok" {
		t.Errorf("got %q, want %q", got, "tok")
	}
}

// TestNpmrcAuthToken_EmbeddedCRIsError verifies that a resolved value
// containing a mid-line "\r" fails closed -- bufio.Scanner's default
// ScanLines only strips a trailing "\r" immediately before the "\n"
// delimiter, so a "\r" embedded earlier in the value survives into the
// returned token and would reach the HTTP proxy's header-write path. The
// error must name the file, never the value itself.
func TestNpmrcAuthToken_EmbeddedCRIsError(t *testing.T) {
	content := []byte("//registry.example.com/:_authToken=s3kr3t\rX-Injected: evil\n")
	const path = "/some/npmrc"
	const host = "registry.example.com"

	_, err := npmrcAuthToken(content, path, host)
	if err == nil {
		t.Fatal("expected error for a value with an embedded CR, got nil")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("expected error to mention the path %q, got: %v", path, err)
	}
	if strings.Contains(err.Error(), "s3kr3t") {
		t.Errorf("expected error not to print the credential value, got: %v", err)
	}
}

// TestNpmrcAuthToken_VariableExpansionIsError verifies that a value using
// npm's "${VAR}" environment-variable expansion syntax fails closed rather
// than resolving to the literal, unexpanded placeholder string -- this
// resolver does not implement npm's expansion, so returning the literal
// text would let doctor report green while the proxy sends a bogus token
// upstream.
func TestNpmrcAuthToken_VariableExpansionIsError(t *testing.T) {
	content := []byte("//registry.example.com/:_authToken=${NPM_TOKEN}\n")
	const path = "/some/npmrc"
	const host = "registry.example.com"

	_, err := npmrcAuthToken(content, path, host)
	if err == nil {
		t.Fatal("expected error for a value using npm variable expansion, got nil")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("expected error to mention the path %q, got: %v", path, err)
	}
	if !strings.Contains(err.Error(), host) {
		t.Errorf("expected error to mention the host %q, got: %v", host, err)
	}
	if !strings.Contains(err.Error(), "variable expansion") {
		t.Errorf("expected error to mention npm variable expansion, got: %v", err)
	}
}

// TestNpmrcAuthToken_CommentsAndBlankLinesAreSkipped verifies that "#" and
// ";"-prefixed comment lines and blank lines never confuse the parse -- the
// real entry following them still resolves.
func TestNpmrcAuthToken_CommentsAndBlankLinesAreSkipped(t *testing.T) {
	content := []byte(
		"# a comment\n" +
			"; another comment\n" +
			"\n" +
			"//registry.example.com/:_authToken=s3kr3t\n",
	)

	got, err := npmrcAuthToken(content, "/some/npmrc", "registry.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "s3kr3t" {
		t.Errorf("got %q, want %q", got, "s3kr3t")
	}
}
