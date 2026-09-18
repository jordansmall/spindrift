package credresolver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// A missing host must fail closed, never return an empty string with a nil
// error: that would let a proxy run unauthenticated with no signal.
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

// Like every other file adapter, the file-existence check must run before any
// npmrc-specific parsing or the missing-match-host guard.
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

// npmrc has no host source other than the route's match host, unlike netrc,
// which falls back to the route's upstream base URL, so an empty match host
// must fail closed.
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

// The fixture combines mixed case, a port, and a trailing path segment so one
// entry covers all three tolerances at once.
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

// npm accepts, and sometimes writes, double-quoted values.
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

// A matching entry with an empty value must fail closed rather than resolve to
// an empty credential.
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

// The fixture line has no "/" after the leading "//", so the parser has no
// split point between registry spec and key. It must skip the line rather than
// mis-parse it, and the lookup falls through to the "no entry" error.
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

// npmrcHostname must strip only a trailing ":port", not truncate at the first
// ":" inside the address, or two IPv6 hosts differing only after that colon
// cross-match.
func TestNpmrcAuthToken_IPv6HostsWithDistinctAddressesDoNotCrossMatch(t *testing.T) {
	content := []byte("//[fe80::2]:8080/:_authToken=other-tok\n")

	_, err := npmrcAuthToken(content, "/some/npmrc", "[fe80::1]")
	if err == nil {
		t.Fatal("expected error: entry is for [fe80::2], not [fe80::1], so it must not resolve")
	}
}

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

// bufio.Scanner's ScanLines strips only a trailing "\r" before the "\n", so a
// "\r" earlier in the value survives into the token and would reach the HTTP
// proxy's header-write path. The error must name the file, never the value.
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

// This resolver does not implement npm's "${VAR}" expansion, so returning the
// literal placeholder would let doctor report green while the proxy sends a
// bogus token upstream.
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
