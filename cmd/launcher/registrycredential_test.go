package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateRegistryProxyCredential_BothSetIsError verifies that naming
// both fromFile and fromEnv is a mutual-exclusion configuration error.
func TestValidateRegistryProxyCredential_BothSetIsError(t *testing.T) {
	err := validateRegistryProxyCredential("/some/file", "SOME_ENV")
	if err == nil {
		t.Fatal("expected error when both fromFile and fromEnv are set, got nil")
	}
}

// TestValidateRegistryProxyCredential_OnlyFileSetIsValid verifies that
// fromFile alone is accepted.
func TestValidateRegistryProxyCredential_OnlyFileSetIsValid(t *testing.T) {
	if err := validateRegistryProxyCredential("/some/file", ""); err != nil {
		t.Errorf("expected nil error with only fromFile set, got: %v", err)
	}
}

// TestValidateRegistryProxyCredential_OnlyEnvSetIsValid verifies that
// fromEnv alone is accepted.
func TestValidateRegistryProxyCredential_OnlyEnvSetIsValid(t *testing.T) {
	if err := validateRegistryProxyCredential("", "SOME_ENV"); err != nil {
		t.Errorf("expected nil error with only fromEnv set, got: %v", err)
	}
}

// TestValidateRegistryProxyCredential_NeitherSetIsValid verifies that
// leaving both unset is accepted -- no credential source is not an error.
func TestValidateRegistryProxyCredential_NeitherSetIsValid(t *testing.T) {
	if err := validateRegistryProxyCredential("", ""); err != nil {
		t.Errorf("expected nil error with neither set, got: %v", err)
	}
}

// The resolution matrix itself (env/file/netrc/cargo-credentials dispatch,
// trim/newline/empty rules, error-text pinning) lives entirely in
// internal/credresolver/resolver_test.go, exercised directly against
// credresolver.New. The cases below are a thin smoke layer proving only that
// resolveRegistryProxyCredential and peekRegistryProxyCredential forward to
// that seam correctly -- not re-deriving the matrix.

// TestResolveRegistryProxyCredential_FromEnvReturnsValueAndUnsets verifies
// that resolveRegistryProxyCredential forwards a fromEnv reference to
// credresolver's Resolve, which unsets the source variable before
// returning -- see credresolver.TestNew_EnvResolveUnsets for the underlying
// behavior this pins through the wrapper.
func TestResolveRegistryProxyCredential_FromEnvReturnsValueAndUnsets(t *testing.T) {
	t.Setenv("SPINDRIFT_TEST_REGISTRY_CRED", "s3kr3t")

	got, err := resolveRegistryProxyCredential("", "SPINDRIFT_TEST_REGISTRY_CRED", "raw", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "s3kr3t" {
		t.Errorf("got %q, want %q", got, "s3kr3t")
	}
	if v := os.Getenv("SPINDRIFT_TEST_REGISTRY_CRED"); v != "" {
		t.Errorf("source env var must be unset after resolution, still has value %q", v)
	}
}

// TestResolveRegistryProxyCredential_FromFileReturnsTrimmedContents verifies
// that resolveRegistryProxyCredential forwards a fromFile reference to
// credresolver's Resolve and returns its resolved value -- see
// credresolver.TestNew_RawFileTrimsWhitespaceAndDefaultsFormat for the
// underlying trim/format rules this pins through the wrapper.
func TestResolveRegistryProxyCredential_FromFileReturnsTrimmedContents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cred")
	if err := os.WriteFile(path, []byte("filesecret\n"), 0o600); err != nil {
		t.Fatalf("failed to write temp cred file: %v", err)
	}

	got, err := resolveRegistryProxyCredential(path, "", "raw", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "filesecret" {
		t.Errorf("got %q, want %q", got, "filesecret")
	}
}

// TestResolveRegistryProxyCredential_FromFileMissingIsError verifies that a
// resolution error from credresolver -- here, a fromFile reference naming a
// nonexistent path -- passes through resolveRegistryProxyCredential
// unchanged, naming the path. See
// credresolver.TestNew_RawFileMissingIsError for the underlying error.
func TestResolveRegistryProxyCredential_FromFileMissingIsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist")

	_, err := resolveRegistryProxyCredential(path, "", "raw", "", "")
	if err == nil {
		t.Fatal("expected error for nonexistent credential file, got nil")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("expected error to mention the path %q, got: %v", path, err)
	}
}

// TestPeekRegistryProxyCredential_FromEnvDoesNotUnset verifies that, unlike
// resolveRegistryProxyCredential, peekRegistryProxyCredential forwards to
// credresolver's Peek and does not unset the source variable -- see
// credresolver.TestNew_EnvPeekDoesNotUnset for the underlying behavior this
// pins through the wrapper.
func TestPeekRegistryProxyCredential_FromEnvDoesNotUnset(t *testing.T) {
	t.Setenv("SPINDRIFT_TEST_REGISTRY_CRED_PEEK", "s3kr3t")

	got, err := peekRegistryProxyCredential("", "SPINDRIFT_TEST_REGISTRY_CRED_PEEK", "raw", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "s3kr3t" {
		t.Errorf("got %q, want %q", got, "s3kr3t")
	}
	if v := os.Getenv("SPINDRIFT_TEST_REGISTRY_CRED_PEEK"); v != "s3kr3t" {
		t.Errorf("source env var must still be set after peek, got %q", v)
	}
}

// multiEntryNetrc is a netrc file with several machine entries, used by
// bootstrap_test.go's TestBootstrap_ResolvableRegistryProxyCredentialNetrc_WithUpstreamURL_Succeeds
// to prove host-matching, not "first entry wins", resolves the end-to-end
// bootstrap credential. The resolution-matrix-level netrc host-matching
// tests live in internal/credresolver/resolver_test.go, which defines its
// own copy -- that package cannot import this one's test-only const.
const multiEntryNetrc = `machine other.example.com
login someone
password wrong-entry

machine registry.example.com
login someone
password s3cr3t

machine yet-another.example.com
login someone
password also-wrong
`

// TestValidateRegistryProxyUpstreamURL_EmptyIsValid verifies that an unset
// upstream URL is not this function's problem to reject -- unset is the
// documented opt-out that disables the registry proxy entirely.
func TestValidateRegistryProxyUpstreamURL_EmptyIsValid(t *testing.T) {
	if err := validateRegistryProxyUpstreamURL(""); err != nil {
		t.Errorf("expected nil error for empty upstream URL, got: %v", err)
	}
}

// TestValidateRegistryProxyUpstreamURL_BareOriginIsValid verifies that a
// scheme+host URL, with or without a trailing slash, is accepted -- and that
// a query string, which the rewrite hook deliberately merges rather than
// rejects, does not trip the path check either.
func TestValidateRegistryProxyUpstreamURL_BareOriginIsValid(t *testing.T) {
	for _, u := range []string{
		"https://registry.example.com",
		"https://registry.example.com/",
		"https://registry.example.com?token=abc",
	} {
		t.Run(u, func(t *testing.T) {
			if err := validateRegistryProxyUpstreamURL(u); err != nil {
				t.Errorf("expected nil error for bare origin %q, got: %v", u, err)
			}
		})
	}
}

// TestValidateRegistryProxyUpstreamURL_NonEmptyPathIsError verifies that a
// URL carrying a non-empty path is rejected -- the rewrite logic would
// otherwise double that path onto every proxied request path, guaranteeing
// upstream 404s.
func TestValidateRegistryProxyUpstreamURL_NonEmptyPathIsError(t *testing.T) {
	for _, u := range []string{
		"https://registry.example.com/foo",
		"https://registry.example.com/artifactory/api/cargo/crates/index/",
	} {
		t.Run(u, func(t *testing.T) {
			err := validateRegistryProxyUpstreamURL(u)
			if err == nil {
				t.Fatalf("expected error for upstream URL with a path %q, got nil", u)
			}
			if !strings.Contains(err.Error(), "bare origin") {
				t.Errorf("expected error to state the bare-origin requirement, got: %v", err)
			}
		})
	}
}

// TestValidateRegistryProxyUpstreamURL_SchemeLessPathIsError verifies that a
// scheme-less "host:port/path" upstream -- missing the "//" that would make
// it parse as an absolute URL -- is still caught here. net/url parses that
// shape as scheme "host", opaque "port/path" rather than populating Path, so
// the path lives in u.Opaque; missing that case would let this plausible
// operator typo slip past this check and fail downstream at
// registryproxy.New with an unrelated "must be absolute" error instead of
// naming the actual problem.
func TestValidateRegistryProxyUpstreamURL_SchemeLessPathIsError(t *testing.T) {
	err := validateRegistryProxyUpstreamURL("registry.internal:8080/artifactory/index")
	if err == nil {
		t.Fatal("expected error for scheme-less upstream URL with a path, got nil")
	}
	if !strings.Contains(err.Error(), "bare origin") {
		t.Errorf("expected error to state the bare-origin requirement, got: %v", err)
	}
}

// TestValidateRegistryProxyUpstreamURL_MalformedURLIsError verifies that a
// URL that fails net/url parsing produces a clear, wrapped error naming the
// URL, rather than panicking or being silently accepted.
func TestValidateRegistryProxyUpstreamURL_MalformedURLIsError(t *testing.T) {
	const bad = "://bad"
	err := validateRegistryProxyUpstreamURL(bad)
	if err == nil {
		t.Fatal("expected error for malformed upstream URL, got nil")
	}
	if !strings.Contains(err.Error(), bad) {
		t.Errorf("expected error to mention the malformed URL %q, got: %v", bad, err)
	}
}
