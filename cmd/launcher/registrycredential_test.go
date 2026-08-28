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

	got, err := resolveRegistryProxyCredential("", "SPINDRIFT_TEST_REGISTRY_CRED")
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

	got, err := resolveRegistryProxyCredential(path, "")
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

			_, err := resolveRegistryProxyCredential(path, "")
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

	got, err := resolveRegistryProxyCredential(path, "")
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

	_, err := resolveRegistryProxyCredential(path, "")
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
	got, err := resolveRegistryProxyCredential("", "")
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

	_, err := resolveRegistryProxyCredential(path, "")
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

	_, err := resolveRegistryProxyCredential("", name)
	if err == nil {
		t.Fatal("expected error when fromEnv names a variable that is not set, got nil")
	}
}

// TestResolveRegistryProxyCredential_FromEnvEmptyVarIsError verifies that a
// fromEnv reference naming a variable set to the empty string fails closed,
// the same as an unset variable.
func TestResolveRegistryProxyCredential_FromEnvEmptyVarIsError(t *testing.T) {
	t.Setenv("SPINDRIFT_TEST_REGISTRY_CRED_EMPTY", "")

	_, err := resolveRegistryProxyCredential("", "SPINDRIFT_TEST_REGISTRY_CRED_EMPTY")
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

	got, err := peekRegistryProxyCredential("", "SPINDRIFT_TEST_REGISTRY_CRED_PEEK")
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

	got, err := peekRegistryProxyCredential(path, "")
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
	_, err = peekRegistryProxyCredential(newlinePath, "")
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

	got, err := resolveRegistryProxyCredential(path, "SPINDRIFT_TEST_REGISTRY_CRED_BOTH")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "envsecret" {
		t.Errorf("got %q, want %q (env preferred over file)", got, "envsecret")
	}
}
