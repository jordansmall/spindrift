package credresolver

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// killBackgroundChildOnCleanup schedules t.Cleanup to kill the process whose
// PID is written to pidFile -- the exec tests below deliberately leave a
// background child (e.g. `sleep 60`) holding the command's stdout pipe open
// well past WaitDelay so they can prove Peek doesn't hang on it; without this
// cleanup, that child would otherwise run for a full minute per test, and a
// stress run stacking many iterations would strand a growing pile of
// orphaned sleeps.
func killBackgroundChildOnCleanup(t *testing.T, pidFile string) {
	t.Cleanup(func() {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			return
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			return
		}
		_ = proc.Kill()
	})
}

// TestNew_EnvPeekDoesNotUnset verifies that New's env-var adapter resolves
// the value via Peek without unsetting the source variable -- doctor's
// non-destructive read must not consume the credential ahead of the real
// resolution that still has to run later.
func TestNew_EnvPeekDoesNotUnset(t *testing.T) {
	t.Setenv("SPINDRIFT_TEST_CREDRESOLVER_PEEK", "s3kr3t")

	got, err := New(Config{FromEnv: "SPINDRIFT_TEST_CREDRESOLVER_PEEK", FileFormat: "raw"}).Peek()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "s3kr3t" {
		t.Errorf("got %q, want %q", got, "s3kr3t")
	}
	if v := os.Getenv("SPINDRIFT_TEST_CREDRESOLVER_PEEK"); v != "s3kr3t" {
		t.Errorf("source env var must still be set after peek, got %q", v)
	}
}

// TestNew_EnvResolveUnsets verifies that New's env-var adapter resolves the
// value via Resolve and unsets the source variable before returning -- the
// load-bearing distinction from Peek.
func TestNew_EnvResolveUnsets(t *testing.T) {
	t.Setenv("SPINDRIFT_TEST_CREDRESOLVER_RESOLVE", "s3kr3t")

	got, err := New(Config{FromEnv: "SPINDRIFT_TEST_CREDRESOLVER_RESOLVE", FileFormat: "raw"}).Resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "s3kr3t" {
		t.Errorf("got %q, want %q", got, "s3kr3t")
	}
	if v := os.Getenv("SPINDRIFT_TEST_CREDRESOLVER_RESOLVE"); v != "" {
		t.Errorf("source env var must be unset after resolve, still has value %q", v)
	}
}

// TestNew_EnvUnsetOrEmptyIsError verifies that both Peek and Resolve fail
// closed when the named env var is unset or set to empty.
func TestNew_EnvUnsetOrEmptyIsError(t *testing.T) {
	const unset = "SPINDRIFT_TEST_CREDRESOLVER_UNSET"
	if _, ok := os.LookupEnv(unset); ok {
		t.Fatalf("test precondition failed: %s is set in the environment", unset)
	}
	t.Setenv("SPINDRIFT_TEST_CREDRESOLVER_EMPTY", "")

	for _, name := range []string{unset, "SPINDRIFT_TEST_CREDRESOLVER_EMPTY"} {
		t.Run(name, func(t *testing.T) {
			r := New(Config{FromEnv: name, FileFormat: "raw"})
			if _, err := r.Peek(); err == nil {
				t.Error("expected Peek error, got nil")
			}
			if _, err := r.Resolve(); err == nil {
				t.Error("expected Resolve error, got nil")
			}
		})
	}
}

// TestNew_NeitherSetReturnsEmpty verifies that with no credential source
// configured, both Peek and Resolve return an empty credential and no
// error -- the one case where empty is not a failure.
func TestNew_NeitherSetReturnsEmpty(t *testing.T) {
	r := New(Config{FileFormat: "raw"})
	if got, err := r.Peek(); err != nil || got != "" {
		t.Errorf("Peek: got (%q, %v), want (\"\", nil)", got, err)
	}
	if got, err := r.Resolve(); err != nil || got != "" {
		t.Errorf("Resolve: got (%q, %v), want (\"\", nil)", got, err)
	}
}

// TestNew_RawFileTrimsWhitespaceAndDefaultsFormat verifies that New's raw
// file adapter trims leading/trailing whitespace, and that fileFormat=""
// resolves the same way as fileFormat="raw".
func TestNew_RawFileTrimsWhitespaceAndDefaultsFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cred")
	if err := os.WriteFile(path, []byte("  tok123 \t\n"), 0o600); err != nil {
		t.Fatalf("failed to write temp cred file: %v", err)
	}

	for _, format := range []string{"raw", ""} {
		t.Run("format="+format, func(t *testing.T) {
			r := New(Config{FromFile: path, FileFormat: format})
			for _, call := range []struct {
				name string
				fn   func() (string, error)
			}{
				{"Peek", r.Peek},
				{"Resolve", r.Resolve},
			} {
				got, err := call.fn()
				if err != nil {
					t.Fatalf("%s: unexpected error: %v", call.name, err)
				}
				if got != "tok123" {
					t.Errorf("%s: got %q, want %q", call.name, got, "tok123")
				}
			}
		})
	}
}

// TestNew_RawFileEmptyContentIsError verifies that a raw credential file
// whose contents trim to empty fails closed, across an empty file, a
// newline-only file, and a CRLF-only file.
func TestNew_RawFileEmptyContentIsError(t *testing.T) {
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

			if _, err := New(Config{FromFile: path, FileFormat: "raw"}).Peek(); err == nil {
				t.Fatal("expected error when credential file trims to empty, got nil")
			}
		})
	}
}

// TestNew_RawFileEmbeddedNewlineIsError verifies that a raw credential
// file whose trimmed contents still contain an embedded newline or
// carriage return fails closed, naming the path and the newline.
func TestNew_RawFileEmbeddedNewlineIsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cred")
	if err := os.WriteFile(path, []byte("tok\n123\n"), 0o600); err != nil {
		t.Fatalf("failed to write temp cred file: %v", err)
	}

	_, err := New(Config{FromFile: path, FileFormat: "raw"}).Peek()
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

// TestNew_RawFileMissingIsError verifies that a raw fromFile reference
// naming a nonexistent path errors, naming the path.
func TestNew_RawFileMissingIsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist")

	_, err := New(Config{FromFile: path, FileFormat: "raw"}).Peek()
	if err == nil {
		t.Fatal("expected error for nonexistent credential file, got nil")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("expected error to mention the path %q, got: %v", path, err)
	}
}

// TestNew_UnrecognizedFileFormatIsError proves an unrecognized fileFormat
// value fails closed and names both the credential file and the bad format
// string -- New's "default" branch (unrecognizedFormatResolver), kept as
// defense in depth here too.
func TestNew_UnrecognizedFileFormatIsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cred")
	if err := os.WriteFile(path, []byte("irrelevant"), 0o600); err != nil {
		t.Fatalf("failed to write temp cred file: %v", err)
	}

	_, err := New(Config{FromFile: path, FileFormat: "bogus-format", UpstreamURL: "https://registry.example.com"}).Peek()
	if err == nil {
		t.Fatal("expected error for unrecognized fileFormat, got nil")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("expected error to mention the credential file path %q, got: %v", path, err)
	}
	if !strings.Contains(err.Error(), "bogus-format") {
		t.Errorf("expected error to mention the unrecognized format, got: %v", err)
	}
}

// multiTableCargoCredentials is a cargo credentials.toml with several
// registry tables, used to prove registryName-matching picks the right
// table rather than always the first.
const multiTableCargoCredentials = `[registries.other]
token = "wrong-token"

[registries.myreg]
token = "s3cr3t"

[registries.yet-another]
token = "also-wrong"
`

// TestNew_CargoFormatResolvesMatchingRegistry proves New's cargo-credentials
// adapter resolves the table named by registryName, through both Peek and
// Resolve.
func TestNew_CargoFormatResolvesMatchingRegistry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.toml")
	if err := os.WriteFile(path, []byte(multiTableCargoCredentials), 0o600); err != nil {
		t.Fatalf("failed to write temp credentials.toml file: %v", err)
	}

	r := New(Config{FromFile: path, FileFormat: "cargo-credentials", RegistryName: "myreg"})
	for _, call := range []struct {
		name string
		fn   func() (string, error)
	}{
		{"Peek", r.Peek},
		{"Resolve", r.Resolve},
	} {
		got, err := call.fn()
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", call.name, err)
		}
		if got != "s3cr3t" {
			t.Errorf("%s: got %q, want %q", call.name, got, "s3cr3t")
		}
	}
}

// TestNew_CargoFormatEmptyRegistryNameIsError proves that
// fileFormat=cargo-credentials with an empty registryName fails closed and
// names the missing knob, and that the file must still be readable first
// (cargoFileResolver reads before it checks registryName).
func TestNew_CargoFormatEmptyRegistryNameIsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.toml")
	if err := os.WriteFile(path, []byte(multiTableCargoCredentials), 0o600); err != nil {
		t.Fatalf("failed to write temp credentials.toml file: %v", err)
	}

	_, err := New(Config{FromFile: path, FileFormat: "cargo-credentials"}).Peek()
	if err == nil {
		t.Fatal("expected error when registryName is empty for cargo-credentials format, got nil")
	}
	if !strings.Contains(err.Error(), "REGISTRY_PROXY_CREDENTIAL_CARGO_REGISTRY_NAME") {
		t.Errorf("expected error to name the missing knob, got: %v", err)
	}
}

// TestNew_CargoFormatFileMissingIsError verifies that a cargo-credentials
// fromFile reference naming a nonexistent path errors and names the path,
// even when registryName is also unset -- the file read fails before the
// registryName check runs.
func TestNew_CargoFormatFileMissingIsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist")

	_, err := New(Config{FromFile: path, FileFormat: "cargo-credentials"}).Peek()
	if err == nil {
		t.Fatal("expected error for nonexistent credential file, got nil")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("expected error to mention the path %q, got: %v", path, err)
	}
	if strings.Contains(err.Error(), "REGISTRY_PROXY_CREDENTIAL_CARGO_REGISTRY_NAME") {
		t.Errorf("expected the missing-file error, not the missing-registryName error, got: %v", err)
	}
}

// TestNew_CargoFormatNoMatchingTableIsError proves a credentials.toml with
// no table for registryName fails closed through New's wired path and
// surfaces the underlying cargoCredentialsToken error -- detail coverage of
// that error's exact shape lives in cargocredentials_test.go.
func TestNew_CargoFormatNoMatchingTableIsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.toml")
	if err := os.WriteFile(path, []byte(multiTableCargoCredentials), 0o600); err != nil {
		t.Fatalf("failed to write temp credentials.toml file: %v", err)
	}

	_, err := New(Config{FromFile: path, FileFormat: "cargo-credentials", RegistryName: "no-such-registry"}).Peek()
	if err == nil {
		t.Fatal("expected error when credentials.toml has no table for registryName, got nil")
	}
	if !strings.Contains(err.Error(), "no-such-registry") {
		t.Errorf("expected error to mention the unmatched registry name, got: %v", err)
	}
}

// TestNew_CargoFormatNoTokenIsError proves a credentials.toml whose matching
// table has no token field fails closed through New's wired path.
func TestNew_CargoFormatNoTokenIsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.toml")
	const noToken = `[registries.myreg]
other-key = "value"
`
	if err := os.WriteFile(path, []byte(noToken), 0o600); err != nil {
		t.Fatalf("failed to write temp credentials.toml file: %v", err)
	}

	_, err := New(Config{FromFile: path, FileFormat: "cargo-credentials", RegistryName: "myreg"}).Peek()
	if err == nil {
		t.Fatal("expected error when the matching table has no token field, got nil")
	}
	if !strings.Contains(err.Error(), "myreg") {
		t.Errorf("expected error to mention the registry name, got: %v", err)
	}
}

// TestNew_CargoFormatNeverEchoesSecret proves that a Peek resolution error on
// the cargo-credentials path never contains a real secret value that happens
// to be in scope elsewhere in the same test -- the cargo-credentials
// analogue of TestNew_NeverEchoesSecret above.
func TestNew_CargoFormatNeverEchoesSecret(t *testing.T) {
	const secret = "s3kr3t-do-not-echo"

	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.toml")
	contents := "[registries.myreg]\ntoken = \"" + secret + "\"\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("failed to write temp credentials.toml file: %v", err)
	}

	got, err := New(Config{FromFile: path, FileFormat: "cargo-credentials", RegistryName: "myreg"}).Peek()
	if err != nil {
		t.Fatalf("unexpected error resolving valid secret: %v", err)
	}
	if got != secret {
		t.Fatalf("got %q, want %q", got, secret)
	}

	// The missing-table error path is the meaningful case for this format --
	// registryName is looked up against the same file whose bytes hold the
	// real secret in scope right up to the point the function errors out, so
	// this is where an accidental interpolation of the file's contents into
	// the error message would actually leak it.
	_, err = New(Config{FromFile: path, FileFormat: "cargo-credentials", RegistryName: "no-such-registry"}).Peek()
	if err == nil {
		t.Fatal("expected error for registry name with no matching table, got nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error must never echo the secret value, got: %v", err)
	}
}

// TestNew_BothSetPrefersEnv verifies the unreachable-under-normal-use
// fallback: if a caller skips validation and both fromFile and fromEnv are
// set anyway, New's dispatch deterministically prefers fromEnv.
func TestNew_BothSetPrefersEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cred")
	if err := os.WriteFile(path, []byte("filesecret"), 0o600); err != nil {
		t.Fatalf("failed to write temp cred file: %v", err)
	}
	t.Setenv("SPINDRIFT_TEST_CREDRESOLVER_BOTH", "envsecret")

	got, err := New(Config{FromFile: path, FromEnv: "SPINDRIFT_TEST_CREDRESOLVER_BOTH", FileFormat: "raw"}).Resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "envsecret" {
		t.Errorf("got %q, want %q (env preferred over file)", got, "envsecret")
	}
}

// TestNew_NeverEchoesSecret proves that a Peek resolution error never
// contains a real secret value that happens to be in scope elsewhere in
// the same test -- guards against a future adapter change accidentally
// interpolating the resolved value into an error message.
func TestNew_NeverEchoesSecret(t *testing.T) {
	const secret = "s3kr3t-do-not-echo"

	dir := t.TempDir()
	path := filepath.Join(dir, "cred")
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		t.Fatalf("failed to write temp cred file: %v", err)
	}

	got, err := New(Config{FromFile: path, FileFormat: "raw"}).Peek()
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
	_, err = New(Config{FromFile: newlinePath, FileFormat: "raw"}).Peek()
	if err == nil {
		t.Fatal("expected error for credential file with embedded newline, got nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error must never echo the secret value, got: %v", err)
	}
}

// multiEntryNetrc is a netrc file with several machine entries, used to
// prove host-matching picks the right entry rather than always the first.
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

// TestNew_NetrcFormatResolvesMatchingHost proves New's netrc adapter
// resolves the entry whose machine matches upstreamURL's host, through
// both Peek and Resolve.
func TestNew_NetrcFormatResolvesMatchingHost(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "netrc")
	if err := os.WriteFile(path, []byte(multiEntryNetrc), 0o600); err != nil {
		t.Fatalf("failed to write temp netrc file: %v", err)
	}

	r := New(Config{FromFile: path, FileFormat: "netrc", UpstreamURL: "https://registry.example.com"})
	for _, call := range []struct {
		name string
		fn   func() (string, error)
	}{
		{"Peek", r.Peek},
		{"Resolve", r.Resolve},
	} {
		got, err := call.fn()
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", call.name, err)
		}
		if got != "s3cr3t" {
			t.Errorf("%s: got %q, want %q", call.name, got, "s3cr3t")
		}
	}
}

// TestNew_NetrcFormatNoMatchingHostIsError proves a netrc file with no
// entry for upstreamURL's host fails closed and names the host.
func TestNew_NetrcFormatNoMatchingHostIsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "netrc")
	if err := os.WriteFile(path, []byte(multiEntryNetrc), 0o600); err != nil {
		t.Fatalf("failed to write temp netrc file: %v", err)
	}

	_, err := New(Config{FromFile: path, FileFormat: "netrc", UpstreamURL: "https://no-such-host.example.com"}).Peek()
	if err == nil {
		t.Fatal("expected error when netrc has no entry for the upstream host, got nil")
	}
	if !strings.Contains(err.Error(), "no-such-host.example.com") {
		t.Errorf("expected error to mention the unmatched host, got: %v", err)
	}
}

// TestNew_NetrcFormatMalformedUpstreamURLIsError proves a malformed
// upstreamURL fails closed, naming the bad URL, rather than panicking.
func TestNew_NetrcFormatMalformedUpstreamURLIsError(t *testing.T) {
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
			_, err := New(Config{FromFile: path, FileFormat: "netrc", UpstreamURL: upstreamURL}).Peek()
			if err == nil {
				t.Fatalf("expected error for malformed upstreamURL %q, got nil", upstreamURL)
			}
			if !strings.Contains(err.Error(), upstreamURL) {
				t.Errorf("expected error to mention the malformed upstreamURL %q, got: %v", upstreamURL, err)
			}
		})
	}
}

// TestNew_NetrcFormatFileMissingIsError verifies that a netrc fromFile
// reference naming a nonexistent path errors and names the path -- the
// file read happens before the netrc-specific host-parse step.
func TestNew_NetrcFormatFileMissingIsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist")

	_, err := New(Config{FromFile: path, FileFormat: "netrc", UpstreamURL: "https://registry.example.com"}).Peek()
	if err == nil {
		t.Fatal("expected error for nonexistent credential file, got nil")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("expected error to mention the path %q, got: %v", path, err)
	}
}

// TestNew_ExecResolvesFromTrimmedStdout proves New's exec adapter runs the
// argv and resolves to its stdout, trimmed, through both Peek and Resolve.
func TestNew_ExecResolvesFromTrimmedStdout(t *testing.T) {
	r := New(Config{ExecArgv: []string{"/bin/sh", "-c", "echo tok"}, MatchHost: "registry.example.com"})
	for _, call := range []struct {
		name string
		fn   func() (string, error)
	}{
		{"Peek", r.Peek},
		{"Resolve", r.Resolve},
	} {
		got, err := call.fn()
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", call.name, err)
		}
		if got != "tok" {
			t.Errorf("%s: got %q, want %q", call.name, got, "tok")
		}
	}
}

// TestNew_ExecNonZeroExitIsErrorNamingRouteAndCommandNeverStdout proves that
// a failing exec command fails resolution with an error naming the route
// and the command (argv[0] only, never the rest of argv), and never echoes
// the command's stdout -- even when that stdout happens to contain what
// looks like a secret. The stdout secret lives in the script file's body,
// not in argv, so it can only reach the error via an accidental
// stdout/stderr interpolation, never via the argv rendering. The argv
// secret is passed as an argument to the script, so it can only reach the
// error via an accidental full-argv interpolation, never via the argv[0]
// rendering.
func TestNew_ExecNonZeroExitIsErrorNamingRouteAndCommandNeverStdout(t *testing.T) {
	const stdoutSecret = "s3kr3t-do-not-echo"
	const argvSecret = "--token=sekrit-arg"
	dir := t.TempDir()
	script := filepath.Join(dir, "cred.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho "+stdoutSecret+"\nexit 1\n"), 0o700); err != nil {
		t.Fatalf("failed to write temp script: %v", err)
	}

	r := New(Config{
		ExecArgv:  []string{script, argvSecret},
		MatchHost: "registry.example.com",
	})

	_, err := r.Peek()
	if err == nil {
		t.Fatal("expected error for non-zero exit, got nil")
	}
	if !strings.Contains(err.Error(), "registry.example.com") {
		t.Errorf("expected error to mention the route %q, got: %v", "registry.example.com", err)
	}
	if !strings.Contains(err.Error(), script) {
		t.Errorf("expected error to mention the command, got: %v", err)
	}
	if strings.Contains(err.Error(), stdoutSecret) {
		t.Errorf("error must never echo the command's stdout, got: %v", err)
	}
	if strings.Contains(err.Error(), argvSecret) {
		t.Errorf("error must never echo argv beyond argv[0], got: %v", err)
	}
}

// TestExecResolver_PeekTimesOut proves that a credential helper that blocks
// (e.g. `op read` waiting on biometric confirmation) does not hang doctor's
// route Peek forever -- Peek must bound the run and fail closed with a
// timeout error well before the helper would ever return on its own.
func TestExecResolver_PeekTimesOut(t *testing.T) {
	r := execResolver{argv: []string{"sleep", "5"}, matchHost: "x", timeout: 100 * time.Millisecond}

	start := time.Now()
	_, err := r.Peek()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timeout") && !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected error to mention the timeout, got: %v", err)
	}
	if !strings.Contains(err.Error(), "100ms") {
		t.Errorf("expected error to mention the timeout duration, got: %v", err)
	}
	if !strings.Contains(err.Error(), "x") {
		t.Errorf("expected error to mention the route's match host, got: %v", err)
	}
	if elapsed >= time.Second {
		t.Errorf("Peek took %s, expected it to return well under a second", elapsed)
	}
}

// TestExecResolver_PeekSucceedsWithBackgroundChildHoldingStdout proves that a
// helper which exits 0 quickly but leaves a background process inheriting
// its stdout (e.g. `pass` spawning gpg-agent, `op read` spawning its daemon)
// still resolves promptly instead of hanging on the inherited pipe.
func TestExecResolver_PeekSucceedsWithBackgroundChildHoldingStdout(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "bg.pid")
	killBackgroundChildOnCleanup(t, pidFile)
	r := execResolver{
		argv:      []string{"/bin/sh", "-c", "echo tok; sleep 60 & echo $! >" + pidFile + "; exit 0"},
		matchHost: "x",
		timeout:   500 * time.Millisecond,
		waitDelay: 50 * time.Millisecond,
	}

	type result struct {
		v   string
		err error
	}
	done := make(chan result, 1)
	go func() {
		v, err := r.Peek()
		done <- result{v, err}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("unexpected error: %v", res.err)
		}
		if res.v != "tok" {
			t.Errorf("got %q, want %q", res.v, "tok")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Peek did not return within 3s; it hung on the background child's inherited stdout")
	}
}

// TestExecResolver_PeekTimesOutWithBackgroundChildHoldingStdout proves that
// when the helper itself blocks past the deadline AND a background child
// also inherits its stdout, Peek still returns the timeout error promptly --
// the background child must not keep the pipe open past WaitDelay.
func TestExecResolver_PeekTimesOutWithBackgroundChildHoldingStdout(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "bg.pid")
	killBackgroundChildOnCleanup(t, pidFile)
	r := execResolver{
		argv:      []string{"/bin/sh", "-c", "sleep 60 & echo $! >" + pidFile + "; sleep 60"},
		matchHost: "x",
		timeout:   100 * time.Millisecond,
		waitDelay: 50 * time.Millisecond,
	}

	type result struct {
		v   string
		err error
	}
	done := make(chan result, 1)
	go func() {
		v, err := r.Peek()
		done <- result{v, err}
	}()

	select {
	case res := <-done:
		if res.err == nil {
			t.Fatal("expected timeout error, got nil")
		}
		if !strings.Contains(res.err.Error(), "timed out") {
			t.Errorf("expected error to mention the timeout, got: %v", res.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Peek did not return within 3s; it hung on the background child's inherited stdout")
	}
}

// TestNew_ExecEmptyOutputIsError proves that an exec command exiting zero
// but producing only whitespace stdout fails closed, naming the route and
// command.
func TestNew_ExecEmptyOutputIsError(t *testing.T) {
	r := New(Config{ExecArgv: []string{"/bin/sh", "-c", "echo   "}, MatchHost: "registry.example.com"})

	_, err := r.Peek()
	if err == nil {
		t.Fatal("expected error for empty command output, got nil")
	}
	if !strings.Contains(err.Error(), "registry.example.com") {
		t.Errorf("expected error to mention the route, got: %v", err)
	}
	if !strings.Contains(err.Error(), "/bin/sh") {
		t.Errorf("expected error to mention the command, got: %v", err)
	}
}

// TestNew_ExecEmbeddedNewlineIsError proves that an exec command whose
// trimmed stdout still contains an embedded newline fails closed -- a
// single-line credential is expected, same rule as the raw file source.
func TestNew_ExecEmbeddedNewlineIsError(t *testing.T) {
	r := New(Config{ExecArgv: []string{"/bin/sh", "-c", "printf 'tok\\n123\\n'"}, MatchHost: "registry.example.com"})

	_, err := r.Peek()
	if err == nil {
		t.Fatal("expected error for embedded newline in command output, got nil")
	}
	if !strings.Contains(err.Error(), "newline") {
		t.Errorf("expected error to mention the embedded newline, got: %v", err)
	}
}
