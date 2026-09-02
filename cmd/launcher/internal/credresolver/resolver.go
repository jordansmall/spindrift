// Package credresolver is the credential-resolution seam behind
// resolveRegistryProxyCredential and peekRegistryProxyCredential: it turns a
// registry route's Credential reference (ADR 0044) into adapters over each
// of that reference's possible sources -- a single env var, a raw file, a
// netrc file, a cargo credentials.toml, or an exec command -- so the two
// callers share one
// dispatch and one set of trim/newline/empty/fail-closed rules instead of
// each reimplementing them.
package credresolver

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
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

// Config is a Credential reference (ADR 0044) plus the two route facts its
// file formats key on: UpstreamURL for netrc's host match, RegistryName for
// cargo-credentials' table match. ExecArgv and MatchHost back the exec
// source: MatchHost is the route's match host, used only to name the route
// in an exec failure's error, not to select behavior.
type Config struct {
	FromFile     string
	FromEnv      string
	FileFormat   string
	UpstreamURL  string
	RegistryName string
	ExecArgv     []string
	MatchHost    string
}

// New selects the Resolver adapter for a Credential reference (ADR 0044):
// c.FromEnv wins when both c.FromEnv and c.FromFile/c.ExecArgv are set
// (normally unreachable -- validateRegistryProxyCredential rejects more than
// one source being set; this is only the deterministic fallback for a
// caller that skips that validation), then c.FromFile, then c.ExecArgv.
// c.FileFormat only matters when c.FromFile is used; "" defaults to "raw".
// None of c.FromFile, c.FromEnv, c.ExecArgv set resolves to a no-op Resolver
// that always returns ("", nil).
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
		default:
			return peekOnly{unrecognizedFormatResolver{path: c.FromFile, format: c.FileFormat}}
		}
	}
	if len(c.ExecArgv) > 0 {
		return peekOnly{execResolver{argv: c.ExecArgv, matchHost: c.MatchHost}}
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
		return "", fmt.Errorf("registry proxy credential file %s is in netrc format but REGISTRY_PROXY_UPSTREAM_URL %q has no parseable host", r.path, r.upstreamURL)
	}
	// u.Hostname() strips any port, so a netrc entry keyed "machine
	// host:port" never matches -- the match is host-only, same as
	// REGISTRY_PROXY_UPSTREAM_URL's other consumers.
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
		return "", fmt.Errorf("registry proxy credential file %s is in cargo-credentials format but REGISTRY_PROXY_CREDENTIAL_CARGO_REGISTRY_NAME is unset: it must be set when REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT=cargo-credentials", r.path)
	}
	return cargoCredentialsToken(b, r.path, r.registryName)
}

// unrecognizedFormatResolver is reached only when fileFormat names neither
// "", "raw", "netrc", nor "cargo-credentials" -- unreachable through
// configuration, since choiceKnobRegistry rejects any
// REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT value outside that set before
// bootstrap ever reaches this adapter. Kept as defense in depth for a
// caller that skips that validation.
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

// execResolver runs argv as the credential-helper pattern: its trimmed
// stdout is the credential. Peek runs the command (there is no ambient
// state to consume, unlike envResolver) -- this is deliberate, not an
// oversight: doctor's route check Peeks every route, and skipping the run
// on Peek would leave exec routes with no non-destructive check at all.
type execResolver struct {
	argv      []string
	matchHost string
}

func (r execResolver) Peek() (string, error) {
	cmd := exec.Command(r.argv[0], r.argv[1:]...)
	out, err := cmd.Output()
	if err != nil {
		// Never interpolate stdout or stderr here: either can hold a
		// credential or a fragment of one, and this is the failure path
		// most likely to be logged verbatim.
		return "", fmt.Errorf("running registry proxy credential command %q for route %q: %v", r.argv, r.matchHost, err)
	}
	v := strings.TrimSpace(string(out))
	if v == "" {
		return "", fmt.Errorf("registry proxy credential command %q for route %q produced no output", r.argv, r.matchHost)
	}
	if strings.ContainsAny(v, "\r\n") {
		return "", fmt.Errorf("registry proxy credential command %q for route %q produced output with an embedded newline", r.argv, r.matchHost)
	}
	return v, nil
}

// noneResolver is the zero-configuration case: neither fromFile nor fromEnv
// set. "" is not a failure here, unlike every other branch's empty-value
// checks -- no credential source configured is the documented opt-out.
type noneResolver struct{}

func (noneResolver) Peek() (string, error)    { return "", nil }
func (noneResolver) Resolve() (string, error) { return "", nil }
