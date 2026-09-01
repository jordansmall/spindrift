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

// TestResolveRegistryProxyCredential_FromEnvReturnsValueAndUnsets verifies
// that a fromEnv reference resolves to the variable's value and unsets the
// source variable before returning.
func TestResolveRegistryProxyCredential_FromEnvReturnsValueAndUnsets(t *testing.T) {
	t.Setenv("SPINDRIFT_TEST_REGISTRY_CRED", "s3kr3t")

	got, err := resolveRegistryProxyCredential("", "SPINDRIFT_TEST_REGISTRY_CRED", "raw", "")
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
// that a fromFile reference resolves to the file's contents with a trailing
// newline trimmed.
func TestResolveRegistryProxyCredential_FromFileReturnsTrimmedContents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cred")
	if err := os.WriteFile(path, []byte("filesecret\n"), 0o600); err != nil {
		t.Fatalf("failed to write temp cred file: %v", err)
	}

	got, err := resolveRegistryProxyCredential(path, "", "raw", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "filesecret" {
		t.Errorf("got %q, want %q", got, "filesecret")
	}
}

// TestResolveRegistryProxyCredential_EmptyFileFormatDefaultsToRaw verifies
// that an empty fileFormat resolves a file credential the same way "raw"
// does -- credentialFromSource's zero-value branch exists for a caller that
// leaves fileFormat unset, and no test exercised it directly.
func TestResolveRegistryProxyCredential_EmptyFileFormatDefaultsToRaw(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cred")
	if err := os.WriteFile(path, []byte("filesecret\n"), 0o600); err != nil {
		t.Fatalf("failed to write temp cred file: %v", err)
	}

	got, err := resolveRegistryProxyCredential(path, "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "filesecret" {
		t.Errorf("got %q, want %q", got, "filesecret")
	}
}

// TestResolveRegistryProxyCredential_FromFileEmptyContentIsError verifies
// that a credential file whose contents trim to empty fails closed rather
// than silently starting the proxy unauthenticated.
func TestResolveRegistryProxyCredential_FromFileEmptyContentIsError(t *testing.T) {
	for name, contents := range map[string]string{
		"empty":       "",
		"newlineOnly": "\n",
		"crlfOnly":    "\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "cred")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatalf("failed to write temp cred file: %v", err)
			}

			_, err := resolveRegistryProxyCredential(path, "", "raw", "")
			if err == nil {
				t.Fatal("expected error when credential file trims to empty, got nil")
			}
		})
	}
}

// TestResolveRegistryProxyCredential_FromFileLeadingTrailingSpaceIsTrimmed
// proves a space-padded credential file resolves to the fully-trimmed
// credential value (issue #2850 review finding): the old
// strings.TrimRight(..., "\r\n") left interior/leading/trailing spaces and
// tabs untouched, so a value like " tok123 \n" resolved to "tok123 " with a
// trailing space still attached, which then failed downstream as an opaque
// bad request/502 rather than a clear config error.
func TestResolveRegistryProxyCredential_FromFileLeadingTrailingSpaceIsTrimmed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cred")
	if err := os.WriteFile(path, []byte("  tok123 \t\n"), 0o600); err != nil {
		t.Fatalf("failed to write temp cred file: %v", err)
	}

	got, err := resolveRegistryProxyCredential(path, "", "raw", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "tok123" {
		t.Errorf("got %q, want %q (leading/trailing whitespace trimmed)", got, "tok123")
	}
}

// TestResolveRegistryProxyCredential_FromFileEmbeddedNewlineIsError proves a
// credential file whose trimmed contents still contain an embedded newline
// or carriage return fails closed with a clear config error (issue #2850
// review finding), instead of surfacing later as a cryptic HTTP
// header-validation error when the value is attached to an outbound request.
func TestResolveRegistryProxyCredential_FromFileEmbeddedNewlineIsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cred")
	if err := os.WriteFile(path, []byte("tok\n123\n"), 0o600); err != nil {
		t.Fatalf("failed to write temp cred file: %v", err)
	}

	_, err := resolveRegistryProxyCredential(path, "", "raw", "")
	if err == nil {
		t.Fatal("expected error when credential file contains an embedded newline, got nil")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("expected error to mention the path %q, got: %v", path, err)
	}
	if !strings.Contains(err.Error(), "newline") {
		t.Errorf("expected error to mention the embedded newline, got: %v", err)
	}
}

// TestResolveRegistryProxyCredential_NeitherSetReturnsEmpty verifies that
// with no credential source configured, resolution returns an empty
// credential and no error -- the one case where empty is not a failure.
func TestResolveRegistryProxyCredential_NeitherSetReturnsEmpty(t *testing.T) {
	got, err := resolveRegistryProxyCredential("", "", "raw", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

// TestResolveRegistryProxyCredential_FromFileMissingIsError verifies that a
// fromFile reference naming a nonexistent path errors, naming the path.
func TestResolveRegistryProxyCredential_FromFileMissingIsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist")

	_, err := resolveRegistryProxyCredential(path, "", "raw", "")
	if err == nil {
		t.Fatal("expected error for nonexistent credential file, got nil")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("expected error to mention the path %q, got: %v", path, err)
	}
}

// TestResolveRegistryProxyCredential_FromEnvUnsetVarIsError verifies that a
// fromEnv reference naming an unset variable fails closed.
func TestResolveRegistryProxyCredential_FromEnvUnsetVarIsError(t *testing.T) {
	const name = "SPINDRIFT_TEST_REGISTRY_CRED_DOES_NOT_EXIST"
	if _, ok := os.LookupEnv(name); ok {
		t.Fatalf("test precondition failed: %s is set in the environment", name)
	}

	_, err := resolveRegistryProxyCredential("", name, "raw", "")
	if err == nil {
		t.Fatal("expected error when fromEnv names a variable that is not set, got nil")
	}
}

// TestResolveRegistryProxyCredential_FromEnvEmptyVarIsError verifies that a
// fromEnv reference naming a variable set to the empty string fails closed,
// the same as an unset variable.
func TestResolveRegistryProxyCredential_FromEnvEmptyVarIsError(t *testing.T) {
	t.Setenv("SPINDRIFT_TEST_REGISTRY_CRED_EMPTY", "")

	_, err := resolveRegistryProxyCredential("", "SPINDRIFT_TEST_REGISTRY_CRED_EMPTY", "raw", "")
	if err == nil {
		t.Fatal("expected error when fromEnv names a variable that is set but empty, got nil")
	}
}

// TestPeekRegistryProxyCredential_FromEnvDoesNotUnset verifies that, unlike
// resolveRegistryProxyCredential, peekRegistryProxyCredential resolves a
// fromEnv reference to the variable's value without unsetting the source
// variable -- doctor uses this non-destructive read so it doesn't consume
// the credential before bootstrap's real resolution runs.
func TestPeekRegistryProxyCredential_FromEnvDoesNotUnset(t *testing.T) {
	t.Setenv("SPINDRIFT_TEST_REGISTRY_CRED_PEEK", "s3kr3t")

	got, err := peekRegistryProxyCredential("", "SPINDRIFT_TEST_REGISTRY_CRED_PEEK", "raw", "")
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

// TestPeekRegistryProxyCredential_NeverEchoesSecret proves that a peek
// resolution error never contains a real secret value that happens to be in
// scope elsewhere in the same test -- guards against a future change
// accidentally interpolating the resolved value into an error message.
func TestPeekRegistryProxyCredential_NeverEchoesSecret(t *testing.T) {
	const secret = "s3kr3t-do-not-echo"

	dir := t.TempDir()
	path := filepath.Join(dir, "cred")
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		t.Fatalf("failed to write temp cred file: %v", err)
	}

	got, err := peekRegistryProxyCredential(path, "", "raw", "")
	if err != nil {
		t.Fatalf("unexpected error resolving valid secret: %v", err)
	}
	if got != secret {
		t.Fatalf("got %q, want %q", got, secret)
	}

	// The embedded-newline error path is the meaningful case: v (the
	// trimmed file content) holds the real secret in scope right up to the
	// point the function errors out, so this is where an accidental
	// interpolation of v into the error message would actually leak it.
	newlinePath := filepath.Join(dir, "cred-with-newline")
	if err := os.WriteFile(newlinePath, []byte(secret+"\nextra-line"), 0o600); err != nil {
		t.Fatalf("failed to write temp cred file: %v", err)
	}
	_, err = peekRegistryProxyCredential(newlinePath, "", "raw", "")
	if err == nil {
		t.Fatal("expected error for credential file with embedded newline, got nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error must never echo the secret value, got: %v", err)
	}
}

// TestResolveRegistryProxyCredential_BothSetPrefersEnv verifies the
// unreachable-under-normal-use fallback documented on
// resolveRegistryProxyCredential: if a caller skips validation and both
// fromFile and fromEnv are set anyway, fromEnv wins deterministically.
func TestResolveRegistryProxyCredential_BothSetPrefersEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cred")
	if err := os.WriteFile(path, []byte("filesecret"), 0o600); err != nil {
		t.Fatalf("failed to write temp cred file: %v", err)
	}
	t.Setenv("SPINDRIFT_TEST_REGISTRY_CRED_BOTH", "envsecret")

	got, err := resolveRegistryProxyCredential(path, "SPINDRIFT_TEST_REGISTRY_CRED_BOTH", "raw", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "envsecret" {
		t.Errorf("got %q, want %q (env preferred over file)", got, "envsecret")
	}
}

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

// multiEntryNetrc is a netrc file with several machine entries, used by the
// netrc-format tests below to prove host-matching picks the right entry
// rather than e.g. always the first one.
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

// TestResolveRegistryProxyCredential_NetrcFormatResolvesMatchingHost proves
// the netrc format reaches resolveRegistryProxyCredential end to end: given a
// netrc file with several machine entries, it resolves the entry whose
// machine matches upstreamURL's host, not some other entry (e.g. not
// whichever entry happens to come first).
func TestResolveRegistryProxyCredential_NetrcFormatResolvesMatchingHost(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "netrc")
	if err := os.WriteFile(path, []byte(multiEntryNetrc), 0o600); err != nil {
		t.Fatalf("failed to write temp netrc file: %v", err)
	}

	got, err := resolveRegistryProxyCredential(path, "", "netrc", "https://registry.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "s3cr3t" {
		t.Errorf("got %q, want %q (entry for registry.example.com, not another machine)", got, "s3cr3t")
	}
}

// TestResolveRegistryProxyCredential_NetrcFormatNoMatchingHostIsError proves
// a netrc file with no entry for upstreamURL's host fails closed and names
// the host in the error.
func TestResolveRegistryProxyCredential_NetrcFormatNoMatchingHostIsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "netrc")
	if err := os.WriteFile(path, []byte(multiEntryNetrc), 0o600); err != nil {
		t.Fatalf("failed to write temp netrc file: %v", err)
	}

	_, err := resolveRegistryProxyCredential(path, "", "netrc", "https://no-such-host.example.com")
	if err == nil {
		t.Fatal("expected error when netrc has no entry for the upstream host, got nil")
	}
	if !strings.Contains(err.Error(), "no-such-host.example.com") {
		t.Errorf("expected error to mention the unmatched host, got: %v", err)
	}
}

// TestPeekRegistryProxyCredential_NetrcFormatResolvesMatchingHost proves the
// netrc path also works through peekRegistryProxyCredential, the same as
// resolveRegistryProxyCredential's own netrc test above.
func TestPeekRegistryProxyCredential_NetrcFormatResolvesMatchingHost(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "netrc")
	if err := os.WriteFile(path, []byte(multiEntryNetrc), 0o600); err != nil {
		t.Fatalf("failed to write temp netrc file: %v", err)
	}

	got, err := peekRegistryProxyCredential(path, "", "netrc", "https://registry.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "s3cr3t" {
		t.Errorf("got %q, want %q", got, "s3cr3t")
	}
}

// TestResolveRegistryProxyCredential_NetrcFormatMalformedUpstreamURLIsError
// proves a malformed upstreamURL fails closed with a clear error rather than
// panicking or silently resolving: a URL that fails to parse, one that
// parses but has no host, and the "bareHost" case -- a scheme-less string
// that looks like a real hostname (e.g. "registry.example.com") rather than
// an obviously-malformed one, which url.Parse treats the same way: the
// whole string lands in Path, not Host.
func TestResolveRegistryProxyCredential_NetrcFormatMalformedUpstreamURLIsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "netrc")
	if err := os.WriteFile(path, []byte(multiEntryNetrc), 0o600); err != nil {
		t.Fatalf("failed to write temp netrc file: %v", err)
	}

	for name, upstreamURL := range map[string]string{
		"unparseable": "://bad",
		"noHost":      "not-a-url",
		"bareHost":    "registry.example.com",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := resolveRegistryProxyCredential(path, "", "netrc", upstreamURL)
			if err == nil {
				t.Fatalf("expected error for malformed upstreamURL %q, got nil", upstreamURL)
			}
			if !strings.Contains(err.Error(), upstreamURL) {
				t.Errorf("expected error to mention the malformed upstreamURL %q, got: %v", upstreamURL, err)
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

// TestResolveRegistryProxyCredential_UnrecognizedFileFormatIsError proves an
// unrecognized fileFormat value (i.e. neither "", "raw", nor "netrc") fails
// closed rather than silently falling back to some default, and names both
// the credential file and the bad format string in the error -- the
// default: branch of credentialFromSource's fileFormat switch.
func TestResolveRegistryProxyCredential_UnrecognizedFileFormatIsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "netrc")
	if err := os.WriteFile(path, []byte(multiEntryNetrc), 0o600); err != nil {
		t.Fatalf("failed to write temp netrc file: %v", err)
	}

	_, err := resolveRegistryProxyCredential(path, "", "bogus-format", "https://registry.example.com")
	if err == nil {
		t.Fatal("expected error for unrecognized fileFormat, got nil")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("expected error to mention the credential file path %q, got: %v", path, err)
	}
	if !strings.Contains(err.Error(), "bogus-format") {
		t.Errorf("expected error to mention the unrecognized format %q, got: %v", "bogus-format", err)
	}
}
