// Package credresolver turns a registry route's Credential reference (ADR
// 0045) into an adapter over its source: an env var, a file (raw, netrc,
// cargo credentials.toml, npmrc, gradle.properties), or an exec command, so
// the resolve and peek call sites share one set of fail-closed rules.
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
// registry route's host. Only the env-var adapter consumes its source on
// Resolve (os.Unsetenv, so the credential never reaches a Box's snapshotted
// environment); Peek lets doctor check a route without consuming it.
type Resolver interface {
	// Peek resolves the credential without consuming it.
	Peek() (string, error)
	// Resolve resolves the credential, consuming its source when it has one.
	Resolve() (string, error)
}

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

// Resolve unsets name even when the read failed, and must run before any Box
// is launched: both runtimes build a Box's environment from process state
// captured after this call.
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
// lookup. MatchHost is npmrc's lookup key (ADR 0045; npmrc has no
// upstream-URL-shaped field) and names the route in an exec failure's error.
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

// NamesNoSource reports whether c names none of New's three sources, the
// zero-Config shape of a route with no credential key: a documented
// unauthenticated pass-through (ADR 0045).
func (c Config) NamesNoSource() bool {
	return c.FromEnv == "" && c.FromFile == "" && len(c.ExecArgv) == 0
}

// New selects the Resolver adapter for a Credential reference (ADR 0045):
// c.FromEnv, then c.FromFile (c.FileFormat, where "" means "raw"), then
// c.ExecArgv. registryroutes.Parse already rejects a route naming more than
// one source, so that precedence only matters for a caller that skips the
// validation. A Config naming no source resolves to ("", nil).
func New(c Config) Resolver {
	if c.FromEnv != "" {
		return envResolver{name: c.FromEnv}
	}
	if c.FromFile != "" {
		// The unrecognized-format fallback below still names the caller's
		// original (possibly empty) c.FileFormat, not this default.
		format := c.FileFormat
		if format == "" {
			format = "raw"
		}
		for _, k := range kindTable {
			if k.newFileResolver != nil && k.FileFormat == format {
				return k.newFileResolver(c)
			}
		}
		return peekOnly{unrecognizedFormatResolver{path: c.FromFile, format: c.FileFormat}}
	}
	if len(c.ExecArgv) > 0 {
		return peekOnly{execResolver{argv: c.ExecArgv, matchHost: c.MatchHost, timeout: execCredentialTimeout, waitDelay: execCredentialWaitDelay}}
	}
	return noneResolver{}
}

// readCredentialFile reads the file before any format-specific parsing, so a
// missing file reports "reading ... file" rather than a misleading
// format-specific error.
func readCredentialFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading registry proxy credential file %s: %w", path, err)
	}
	return b, nil
}

// peeker is a credential source with no ambient state to consume.
type peeker interface{ Peek() (string, error) }

// peekOnly adapts a peeker into a Resolver whose Resolve equals Peek; only
// the env-var adapter has state to consume.
type peekOnly struct{ peeker }

func (p peekOnly) Resolve() (string, error) { return p.Peek() }

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
	// u.Hostname() strips any port, so a netrc entry keyed "machine host:port"
	// never matches; the match is host-only.
	return netrcCredential(b, r.path, u.Hostname())
}

type cargoFileResolver struct {
	path         string
	registryName string
}

func (r cargoFileResolver) Peek() (string, error) {
	b, err := readCredentialFile(r.path)
	if err != nil {
		return "", err
	}
	// The read comes first so a missing file always reports "reading ... file",
	// never "registryName is unset", even when both are true.
	if r.registryName == "" {
		return "", fmt.Errorf("registry proxy credential file %s is in cargo-credentials format but the route's credential has no registry-name key: it must be set when credential = { cargo-credentials = ... }", r.path)
	}
	return cargoCredentialsToken(b, r.path, r.registryName)
}

// npmrcFileResolver keys its "//<registry>/:_authToken=" lookup on the
// route's match host (ADR 0045); npmrc has no upstream-URL concept to key on
// the way netrc does.
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

type gradlePropertiesFileResolver struct {
	path        string
	propertyKey string
}

func (r gradlePropertiesFileResolver) Peek() (string, error) {
	b, err := readCredentialFile(r.path)
	if err != nil {
		return "", err
	}
	// Same missing-file-first ordering as cargoFileResolver.Peek above. Only
	// the routes file reaches this format (ADR 0045), never a scalar
	// REGISTRY_PROXY_* knob, so the error names the route, not a knob.
	if r.propertyKey == "" {
		return "", fmt.Errorf("registry proxy credential file %s is in gradle-properties format but the credential's \"key\" is unset", r.path)
	}
	return gradlePropertiesValue(b, r.path, r.propertyKey)
}

// unrecognizedFormatResolver is unreachable through normal configuration:
// registryroutes.Parse is the only path that sets FileFormat (ADR 0045) and
// assigns it from a fixed set. It is kept for a caller that skips that
// validation.
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

const execCredentialTimeout = 30 * time.Second

// execCredentialWaitDelay bounds every execResolver run's Cmd.WaitDelay. Some
// credential helpers (`pass` spawning gpg-agent, `op read` spawning its
// daemon) leave a background process holding the inherited stdout; without
// WaitDelay, Cmd.Output blocks forever on that pipe even after the direct
// child exits, hanging doctor's route check and the launch gate.
const execCredentialWaitDelay = 2 * time.Second

// execResolver runs argv as a credential helper: its trimmed stdout is the
// credential. Peek runs the command deliberately, because doctor's route
// check Peeks every route and skipping the run would leave exec routes with
// no non-destructive check at all.
type execResolver struct {
	argv      []string
	matchHost string
	// timeout bounds the run, defaulting to execCredentialTimeout when zero so
	// a directly-built execResolver is still bounded. Doctor Peeks every
	// route, so a helper blocking on interactive input (`op read` waiting on
	// biometric confirmation) would otherwise hang an unattended dispatch
	// forever.
	timeout time.Duration
	// waitDelay bounds how long Wait keeps reading the command's stdout pipe
	// after the direct child exits or is killed, before force-closing it. Zero
	// defaults to execCredentialWaitDelay, same reasoning as timeout above.
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
		// Never interpolate stdout, stderr, or argv beyond argv[0] here: each
		// can hold the credential (`helper --token=...`), and this is the
		// failure path most likely to be logged verbatim.
		return "", fmt.Errorf("running registry proxy credential command %q for route %q: %v", r.argv[0], r.matchHost, err)
	}
	// exec.ErrWaitDelay means the direct child exited 0 but WaitDelay
	// force-closed the pipe a background child (gpg-agent) still held open.
	// That is success, and out holds what the child wrote before exiting.
	v := strings.TrimSpace(string(out))
	if v == "" {
		return "", fmt.Errorf("registry proxy credential command %q for route %q produced no output", r.argv[0], r.matchHost)
	}
	if strings.ContainsAny(v, "\r\n") {
		return "", fmt.Errorf("registry proxy credential command %q for route %q produced output with an embedded newline", r.argv[0], r.matchHost)
	}
	return v, nil
}

// noneResolver is a route with no credential key: a documented
// unauthenticated pass-through (ADR 0045). "" is not a failure here, unlike
// every other adapter's empty-value check.
type noneResolver struct{}

func (noneResolver) Peek() (string, error)    { return "", nil }
func (noneResolver) Resolve() (string, error) { return "", nil }
