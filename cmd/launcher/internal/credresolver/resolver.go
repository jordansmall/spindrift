// Package credresolver is the credential-resolution seam behind a registry
// route's resolve (resolveRegistryRoutesFromFile) and peek
// (registryProxyRoutesCheck's Probe) call sites: it turns a route's
// Credential reference (ADR 0045) into adapters over each of that
// reference's possible sources -- a single env var, a raw file, a netrc
// file, a cargo credentials.toml, an npmrc file, a gradle.properties file,
// or an exec command -- so the two call sites share one dispatch and one
// set of trim/newline/empty/fail-closed rules instead of each
// reimplementing them.
package credresolver

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Resolver resolves a Credential reference (ADR 0044) to its value for a
// registry route's host. Peek and Resolve are split because exactly one
// adapter -- the env-var one -- must consume its source on a real
// resolution (os.Unsetenv, so the credential never leaks into a Box's
// snapshotted environment) while never doing so on a non-destructive check
// (doctor's peek, which must not consume a credential ahead of the real
// resolution that still has to run later). Every other adapter's Resolve
// behaves identically to its Peek.
type Resolver interface {
	// Peek resolves the credential without consuming it. Safe to call
	// repeatedly and safe to call ahead of a later Resolve.
	Peek() (string, error)
	// Resolve resolves the credential, consuming its source when the
	// adapter has one to consume (only the env-var adapter does; see the
	// interface doc).
	Resolve() (string, error)
}

// envResolver reads the credential from a single environment variable named
// name, failing closed if it is unset or empty.
type envResolver struct {
	name string
}

func (r envResolver) Peek() (string, error) {
	v, ok := os.LookupEnv(r.name)
	if !ok || v == "" {
		return "", fmt.Errorf("registry proxy credential env var %s is unset or empty", r.name)
	}
	return v, nil
}

// Resolve unsets name unconditionally after reading it, even when the read
// itself failed -- an unset var is a no-op for os.Unsetenv, so name is
// still unset on the way out either way. This must run before any Box is
// launched, since both runtimes build a Box's environment from process
// state captured after this call.
func (r envResolver) Resolve() (string, error) {
	v, err := r.Peek()
	if uerr := os.Unsetenv(r.name); uerr != nil {
		return "", fmt.Errorf("unsetting registry proxy credential env var %s: %w", r.name, uerr)
	}
	return v, err
}

// Config is a Credential reference (ADR 0044) plus the route facts its file
// formats key on: UpstreamURL for netrc's host match, RegistryName for
// cargo-credentials' table match, PropertyKey for gradle-properties' key
// lookup. MatchHost does double duty: it's npmrcFileResolver's lookup key
// (the route's match host, ADR 0045 -- npmrc has no upstream-URL-shaped
// field to key on the way netrc does), and it's also what execResolver names
// the route with in an exec failure's error.
type Config struct {
	FromFile     string
	FromEnv      string
	FileFormat   string
	UpstreamURL  string
	RegistryName string
	PropertyKey  string
	ExecArgv     []string
	MatchHost    string
}

// NamesNoSource reports whether c names none of New's three sources
// (FromEnv, FromFile, ExecArgv) -- the zero-Config shape a route whose
// credential key is absent altogether resolves to (ADR 0045's documented
// unauthenticated pass-through).
func (c Config) NamesNoSource() bool {
	return c.FromEnv == "" && c.FromFile == "" && len(c.ExecArgv) == 0
}

// New selects the Resolver adapter for a Credential reference (ADR 0045):
// c.FromEnv wins when both c.FromEnv and c.FromFile/c.ExecArgv are set
// (normally unreachable -- registryroutes.Parse's parseCredential already
// rejects a route naming more than one source; this is only the
// deterministic fallback for a caller that skips that validation), then
// c.FromFile, then c.ExecArgv.
// c.FileFormat only matters when c.FromFile is used; "" defaults to "raw".
// c.NamesNoSource() resolves to a no-op Resolver that always returns
// ("", nil).
func New(c Config) Resolver {
	if c.FromEnv != "" {
		return envResolver{name: c.FromEnv}
	}
	if c.FromFile != "" {
		switch c.FileFormat {
		case "", "raw":
			return peekOnly{rawFileResolver{path: c.FromFile}}
		case "netrc":
			return peekOnly{netrcFileResolver{path: c.FromFile, upstreamURL: c.UpstreamURL}}
		case "cargo-credentials":
			return peekOnly{cargoFileResolver{path: c.FromFile, registryName: c.RegistryName}}
		case "npmrc":
			return peekOnly{npmrcFileResolver{path: c.FromFile, matchHost: c.MatchHost}}
		case "gradle-properties":
			return peekOnly{gradlePropertiesFileResolver{path: c.FromFile, propertyKey: c.PropertyKey}}
		default:
			return peekOnly{unrecognizedFormatResolver{path: c.FromFile, format: c.FileFormat}}
		}
	}
	if len(c.ExecArgv) > 0 {
		return peekOnly{execResolver{argv: c.ExecArgv, matchHost: c.MatchHost, timeout: execCredentialTimeout, waitDelay: execCredentialWaitDelay}}
	}
	return noneResolver{}
}

// readCredentialFile is the shared first step of every file-backed adapter
// -- the file is read exactly once, before any format-specific parsing, so
// e.g. a missing cargo-credentials file reports "reading ... file" rather
// than a misleading format-specific error.
func readCredentialFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading registry proxy credential file %s: %w", path, err)
	}
	return b, nil
}

// peeker is a credential source with no ambient state to consume.
type peeker interface{ Peek() (string, error) }

// peekOnly adapts a peeker into a Resolver whose Resolve equals Peek --
// only the env-var adapter has state to consume on Resolve.
type peekOnly struct{ peeker }

func (p peekOnly) Resolve() (string, error) { return p.Peek() }

// rawFileResolver treats the whole file at path as the credential, trimmed
// of all leading/trailing whitespace.
type rawFileResolver struct {
	path string
}

func (r rawFileResolver) Peek() (string, error) {
	b, err := readCredentialFile(r.path)
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", fmt.Errorf("registry proxy credential file %s is empty", r.path)
	}
	if strings.ContainsAny(v, "\r\n") {
		return "", fmt.Errorf("registry proxy credential file %s contains an embedded newline", r.path)
	}
	return v, nil
}

// netrcFileResolver parses the file at path as netrc-format text and
// extracts the password of the entry whose machine matches upstreamURL's
// bare host.
type netrcFileResolver struct {
	path        string
	upstreamURL string
}

func (r netrcFileResolver) Peek() (string, error) {
	b, err := readCredentialFile(r.path)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(r.upstreamURL)
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("registry proxy credential file %s is in netrc format but the route's upstream-base-url %q has no parseable host", r.path, r.upstreamURL)
	}
	// u.Hostname() strips any port, so a netrc entry keyed "machine
	// host:port" never matches -- the match is host-only.
	return netrcCredential(b, r.path, u.Hostname())
}

// cargoFileResolver parses the file at path as a cargo credentials.toml
// and extracts the token of the "[registries.NAME]" table named by
// registryName.
type cargoFileResolver struct {
	path         string
	registryName string
}

func (r cargoFileResolver) Peek() (string, error) {
	b, err := readCredentialFile(r.path)
	if err != nil {
		return "", err
	}
	// The file must be readable before this check runs, so a missing file
	// always reports "reading ... file", never "registryName is unset",
	// even when both are true.
	if r.registryName == "" {
		return "", fmt.Errorf("registry proxy credential file %s is in cargo-credentials format but the route's credential has no registry-name key: it must be set when credential = { cargo-credentials = ... }", r.path)
	}
	return cargoCredentialsToken(b, r.path, r.registryName)
}

// npmrcFileResolver parses the file at path as npmrc-format text and
// extracts the "_authToken" value of the "//<registry>/:_authToken=" entry
// keyed on matchHost, the route's match host (ADR 0045) -- unlike
// netrcFileResolver, which keys on the route's upstream-base-url's host,
// npmrc has no analogous upstream-URL concept to key on.
type npmrcFileResolver struct {
	path      string
	matchHost string
}

func (r npmrcFileResolver) Peek() (string, error) {
	b, err := readCredentialFile(r.path)
	if err != nil {
		return "", err
	}
	// Same missing-file-first ordering as cargoFileResolver.Peek above.
	if r.matchHost == "" {
		return "", fmt.Errorf("registry proxy credential file %s is in npmrc format but the route has no match host to key on", r.path)
	}
	return npmrcAuthToken(b, r.path, r.matchHost)
}

// gradlePropertiesFileResolver parses the file at path as Java-properties
// text (the shape of a Gradle ~/.gradle/gradle.properties file) and
// extracts the value of the property named propertyKey.
type gradlePropertiesFileResolver struct {
	path        string
	propertyKey string
}

func (r gradlePropertiesFileResolver) Peek() (string, error) {
	b, err := readCredentialFile(r.path)
	if err != nil {
		return "", err
	}
	// Same missing-file-first ordering as cargoFileResolver.Peek above. This
	// format is only reachable from the routes file (ADR 0045), never a
	// scalar REGISTRY_PROXY_* knob, so the error names the route-flavored
	// gap rather than a knob name.
	if r.propertyKey == "" {
		return "", fmt.Errorf("registry proxy credential file %s is in gradle-properties format but the credential's \"key\" is unset", r.path)
	}
	return gradlePropertiesValue(b, r.path, r.propertyKey)
}

// unrecognizedFormatResolver is reached only when fileFormat names neither
// "", "raw", "netrc", "cargo-credentials", "npmrc", nor "gradle-properties".
// registryroutes.Parse, the only path that can set FileFormat (ADR 0045),
// already assigns it from its own fixed set of recognized credential keys,
// so this is unreachable through normal configuration -- kept as defense in
// depth for a caller that skips that validation.
type unrecognizedFormatResolver struct {
	path   string
	format string
}

func (r unrecognizedFormatResolver) Peek() (string, error) {
	if _, err := readCredentialFile(r.path); err != nil {
		return "", err
	}
	return "", fmt.Errorf("registry proxy credential file %s has unrecognized format %q", r.path, r.format)
}

// execCredentialTimeout bounds every execResolver run (see execResolver's
// timeout field doc).
const execCredentialTimeout = 30 * time.Second

// execCredentialWaitDelay bounds every execResolver run's Cmd.WaitDelay (see
// execResolver's waitDelay field doc). Some credential helpers (e.g. `pass`
// spawning gpg-agent, `op read` spawning its daemon) leave a background
// process behind that inherits stdout; without WaitDelay, Cmd.Output blocks
// forever reading that inherited pipe even after the direct child has
// exited or been killed by the context deadline, hanging doctor's route
// check and the launch gate.
const execCredentialWaitDelay = 2 * time.Second

// execResolver runs argv as the credential-helper pattern: its trimmed
// stdout is the credential. Peek runs the command (there is no ambient
// state to consume, unlike envResolver) -- this is deliberate, not an
// oversight: doctor's route check Peeks every route, and skipping the run
// on Peek would leave exec routes with no non-destructive check at all.
type execResolver struct {
	argv      []string
	matchHost string
	// timeout bounds the run; New always sets this to execCredentialTimeout,
	// but the zero value also defaults to execCredentialTimeout in Peek, so a
	// zero-value execResolver built directly (as some tests do) still has a
	// bound. Doctor Peeks every route, so a credential helper that blocks
	// on interactive input (e.g. `op read` waiting on biometric
	// confirmation) would otherwise hang the launch gate, and an unattended
	// dispatch, forever.
	timeout time.Duration
	// waitDelay bounds how long Wait keeps reading the command's stdout pipe
	// after the direct child has exited or been killed, before force-closing
	// it; New always sets this to execCredentialWaitDelay, but the zero value
	// also defaults to execCredentialWaitDelay in Peek, same reasoning as
	// timeout above.
	waitDelay time.Duration
}

func (r execResolver) Peek() (string, error) {
	timeout := r.timeout
	if timeout == 0 {
		timeout = execCredentialTimeout
	}
	waitDelay := r.waitDelay
	if waitDelay == 0 {
		waitDelay = execCredentialWaitDelay
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, r.argv[0], r.argv[1:]...)
	cmd.WaitDelay = waitDelay
	out, err := cmd.Output()
	if err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("running registry proxy credential command %q for route %q timed out after %s", r.argv[0], r.matchHost, timeout)
		}
		// Never interpolate stdout or stderr, or the full argv, here:
		// stdout/stderr can hold a credential or a fragment of one, and
		// argv beyond argv[0] can hold one too (e.g. `helper --token=...`);
		// this is the failure path most likely to be logged verbatim, and
		// the command name alone is enough to identify the helper.
		return "", fmt.Errorf("running registry proxy credential command %q for route %q: %v", r.argv[0], r.matchHost, err)
	}
	// exec.ErrWaitDelay means the direct child itself exited 0 but WaitDelay
	// force-closed the pipe because a background child (e.g. gpg-agent) was
	// still holding it open -- that is success, not a failure, and out still
	// holds whatever the direct child already wrote before exiting.
	v := strings.TrimSpace(string(out))
	if v == "" {
		return "", fmt.Errorf("registry proxy credential command %q for route %q produced no output", r.argv[0], r.matchHost)
	}
	if strings.ContainsAny(v, "\r\n") {
		return "", fmt.Errorf("registry proxy credential command %q for route %q produced output with an embedded newline", r.argv[0], r.matchHost)
	}
	return v, nil
}

// noneResolver is the zero-configuration case: a route whose credential key
// is absent from the routes file altogether (registryroutes.Parse's
// parseCredential maps that to a zero Config), an explicit, documented way
// to declare an unauthenticated pass-through route (ADR 0045). "" is not a
// failure here, unlike every other branch's empty-value checks.
type noneResolver struct{}

func (noneResolver) Peek() (string, error)    { return "", nil }
func (noneResolver) Resolve() (string, error) { return "", nil }
