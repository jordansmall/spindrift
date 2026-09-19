package credresolver

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The exec tests deliberately leave a background child holding the command's
// stdout pipe open past WaitDelay. Without this cleanup each one runs for a
// full minute, and a stress run strands a pile of orphaned sleeps.
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

// Doctor's non-destructive read must not consume the credential ahead of the
// real resolution that still has to run later.
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

// Unsetting the source variable is the load-bearing distinction from Peek.
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

// With no credential source configured, empty is not a failure. This is the
// only case where that holds.
func TestNew_NeitherSetReturnsEmpty(t *testing.T) {
	r := New(Config{FileFormat: "raw"})
	if got, err := r.Peek(); err != nil || got != "" {
		t.Errorf("Peek: got (%q, %v), want (\"\", nil)", got, err)
	}
	if got, err := r.Resolve(); err != nil || got != "" {
		t.Errorf("Resolve: got (%q, %v), want (\"\", nil)", got, err)
	}
}

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

// Covers New's default branch (unrecognizedFormatResolver) as defense in
// depth, on top of that resolver's own tests.
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

// Several registry tables, so the tests prove registryName-matching picks the
// right table rather than always the first.
const multiTableCargoCredentials = `[registries.other]
token = "wrong-token"

[registries.myreg]
token = "s3cr3t"

[registries.yet-another]
token = "also-wrong"
`

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

// The file must still be readable: cargoFileResolver reads it before it
// checks registryName.
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
	if !strings.Contains(err.Error(), "registry-name") {
		t.Errorf("expected error to name the missing route key, got: %v", err)
	}
}

// registryName is also unset here, so this pins the ordering: the file read
// fails before the registryName check runs.
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
	if strings.Contains(err.Error(), "registry-name") {
		t.Errorf("expected the missing-file error, not the missing-registryName error, got: %v", err)
	}
}

// This covers New's wired path only. Detail coverage of the
// cargoCredentialsToken error's exact shape lives in cargocredentials_test.go.
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

// The cargo-credentials analogue of TestNew_NeverEchoesSecret below.
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

	// The missing-table path is the meaningful case: registryName is looked up
	// against the same file whose bytes hold the real secret, so an accidental
	// interpolation of the contents into the error would leak it here.
	_, err = New(Config{FromFile: path, FileFormat: "cargo-credentials", RegistryName: "no-such-registry"}).Peek()
	if err == nil {
		t.Fatal("expected error for registry name with no matching table, got nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error must never echo the secret value, got: %v", err)
	}
}

// Validation normally rejects this config, so the case is unreachable in
// normal use. If a caller skips validation, New's dispatch must still pick
// fromEnv deterministically.
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

// Guards against a future adapter change interpolating the resolved value
// into an error message.
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

	// The embedded-newline path is the meaningful case: v, the trimmed file
	// content, holds the real secret right up to the point the function errors
	// out, so an accidental interpolation of v into the error would leak it.
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

// Several machine entries, so the tests prove host-matching picks the right
// entry rather than always the first.
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

// A bare host parses as a URL with no Host, so it belongs in this table even
// though it looks well formed.
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

// Pins the ordering: the file read happens before the netrc-specific
// host-parse step.
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

// The two secrets sit where each can only leak one way. The stdout secret
// lives in the script body, not argv, so it reaches the error only through a
// stdout or stderr interpolation. The argv secret is an argument to the
// script, so it reaches the error only through a full-argv interpolation
// rather than the argv[0] rendering.
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

// A credential helper can block indefinitely, for example `op read` waiting
// on biometric confirmation. Peek must bound the run and fail closed well
// before the helper would return on its own.
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

// Real helpers leave background processes inheriting stdout: `pass` spawns
// gpg-agent, `op read` spawns its daemon. Peek must not hang on that pipe.
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

// The helper blocks past the deadline and a background child also inherits
// its stdout. The child must not keep the pipe open past WaitDelay.
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

// A credential must be a single line, the same rule as the raw file source.
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

// The cases cover every branch New dispatches on, so this stays the one place
// that rule is asserted.
func TestConfig_NamesNoSource(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"zero value", Config{}, true},
		{"FromEnv set", Config{FromEnv: "SOME_VAR"}, false},
		{"FromFile set", Config{FromFile: "/path/to/file"}, false},
		{"ExecArgv set", Config{ExecArgv: []string{"cmd"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.NamesNoSource(); got != tc.want {
				t.Errorf("NamesNoSource() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResolveUnsetsEnv(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"FromEnv set", Config{FromEnv: "SOME_VAR"}, true},
		{"FromFile set", Config{FromFile: "/path/to/file"}, false},
		{"ExecArgv set", Config{ExecArgv: []string{"cmd"}}, false},
		{"zero value", Config{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveUnsetsEnv(tc.cfg); got != tc.want {
				t.Errorf("ResolveUnsetsEnv() = %v, want %v", got, tc.want)
			}
		})
	}
}

// Peek must not leak the launcher's ambient environment into the credential
// helper's child process (issue #3151). cmd.Env left nil means "inherit
// os.Environ() unconditionally" per os/exec, so an undeclared var like this
// one must never reach the child.
func TestExecResolver_PeekDoesNotLeakUndeclaredAmbientVariable(t *testing.T) {
	t.Setenv("SPINDRIFT_TEST_SECRET_LEAK", "leaked-value")
	r := execResolver{argv: []string{"/bin/sh", "-c", "printf 'SPINDRIFT_TEST_SECRET_LEAK=%s\\n' \"${SPINDRIFT_TEST_SECRET_LEAK:-absent}\""}, matchHost: "x"}

	got, err := r.Peek()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, "leaked-value") {
		t.Errorf("ambient var leaked into credential helper child: %q", got)
	}
	if got != "SPINDRIFT_TEST_SECRET_LEAK=absent" {
		t.Errorf("got %q, want the child to see the var as absent", got)
	}
}

// The allowlist must actually allow through what a real helper needs (`pass`
// spawning gpg-agent, `op read` reaching an unlocked agent), not just block
// everything.
func TestExecResolver_PeekForwardsAllowlistedVariable(t *testing.T) {
	t.Setenv("HOME", "/test/home/for/peek")
	r := execResolver{argv: []string{"/bin/sh", "-c", "printf '%s' \"$HOME\""}, matchHost: "x"}

	got, err := r.Peek()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/test/home/for/peek" {
		t.Errorf("got %q, want the allowlisted HOME value to reach the child", got)
	}
}

// Pins execCredentialEnv's output under this test's own ambient env (PATH,
// HOME, GNUPGHOME set; GPG_TTY, SSH_AUTH_SOCK unset), not the full allowlist
// in general -- a sixth allowlisted name would need its own case here.
func TestExecCredentialEnv_ForwardsOnlyAllowlistedSetVars(t *testing.T) {
	t.Setenv("PATH", "/test/path")
	t.Setenv("HOME", "/test/home")
	t.Setenv("GNUPGHOME", "/test/gnupg")
	// t.Setenv first, so the test's own cleanup restores whatever the ambient
	// value was: a bare os.Unsetenv would leak the removal into every later
	// test in this package.
	t.Setenv("GPG_TTY", "")
	os.Unsetenv("GPG_TTY")
	t.Setenv("SSH_AUTH_SOCK", "")
	os.Unsetenv("SSH_AUTH_SOCK")
	t.Setenv("SPINDRIFT_TEST_SECRET_LEAK", "leaked-value")

	got := execCredentialEnv()

	want := []string{
		"PATH=/test/path",
		"HOME=/test/home",
		"GNUPGHOME=/test/gnupg",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// The empty-but-non-nil slice is the whole point of execCredentialEnv (see
// its doc comment) -- a nil Env would make exec.Cmd inherit os.Environ()
// unconditionally.
func TestExecCredentialEnv_NoAllowlistedVarSetReturnsNonNilEmpty(t *testing.T) {
	for _, k := range execCredentialEnvAllowlist {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}

	got := execCredentialEnv()

	if got == nil {
		t.Fatal("got nil, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

// Per ADR 0045's issue #3151 amendment, a set-but-empty allowlisted name
// still forwards, as "NAME=", matching os.LookupEnv's semantics -- distinct from
// the unset case above, which omits the name entirely.
func TestExecCredentialEnv_SetButEmptyAllowlistedVarForwardsAsEmpty(t *testing.T) {
	t.Setenv("HOME", "")

	got := execCredentialEnv()

	want := "HOME="
	found := false
	for _, kv := range got {
		if kv == want {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("got %v, want it to contain %q", got, want)
	}
}
