// Package registrymanifest is the shared handoff type for the registry
// proxy's Box-facing contract (ADR 0045): everything a Box needs to know
// about the proxy -- where it is reachable, and which route prefixes map to
// which upstream hosts -- crosses in one JSON document carried by a single
// environment variable, rather than one environment variable per
// routes-file field. The launcher mints a Manifest and encodes it; the
// bind-registry verb parses the same string back. Both sides import this
// package so the shape can never drift between mint and parse.
package registrymanifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
)

// EnvVar is the environment variable name carrying the encoded manifest
// (ADR 0045). Both the launcher (mint) and the bind-registry verb (parse)
// import this constant rather than each hard-coding the string, so a rename
// can't silently split the two sides of the handoff.
const EnvVar = "REGISTRY_PROXY_MANIFEST"

// TCPSecretHeader is the HTTP header a Box must carry the per-run TCP
// secret in on every request when the registry proxy is served over
// loopback TCP (issue #3111) -- a unix socket needs no equivalent since
// its own filesystem permissions already gate access.
const TCPSecretHeader = "X-Spindrift-Registry-Proxy-Secret"

// endpointScheme discriminates an Endpoint's transport: exactly one of the
// two ADR-0045 forms, "unix://<path>" or "tcp://<host>:<port>" -- the probe
// this Endpoint is minted from (RegistryProxyTransport, issue #3111) always
// picks one or the other, never both, so the zero value (neither) exists
// only before ParseEndpoint/NewUnixEndpoint/NewTCPEndpoint has run.
type endpointScheme string

const (
	schemeUnix endpointScheme = "unix"
	schemeTCP  endpointScheme = "tcp"
)

// Endpoint is the typed value ADR 0045 assigns the manifest's "endpoint"
// field: a unix domain socket path, or a TCP host:port pair. Fields are
// unexported so a caller can't construct an incoherent value (e.g. a
// scheme with both a path and a host) -- use NewUnixEndpoint,
// NewTCPEndpoint, or ParseEndpoint.
type Endpoint struct {
	scheme endpointScheme
	path   string
	host   string
	port   string
}

// NewUnixEndpoint builds a unix-domain-socket Endpoint from a host path.
func NewUnixEndpoint(path string) Endpoint {
	return Endpoint{scheme: schemeUnix, path: path}
}

// NewTCPEndpoint builds a TCP Endpoint from a host and port. The TCP
// secret's value (carried per request via TCPSecretHeader) is deliberately
// not a field here -- ADR 0045 keeps it in a separate env var, off the
// manifest entirely, so it never round-trips through Encode/Parse.
func NewTCPEndpoint(host, port string) Endpoint {
	return Endpoint{scheme: schemeTCP, host: host, port: port}
}

// IsUnix reports whether e is a unix-domain-socket endpoint.
func (e Endpoint) IsUnix() bool { return e.scheme == schemeUnix }

// IsTCP reports whether e is a TCP endpoint.
func (e Endpoint) IsTCP() bool { return e.scheme == schemeTCP }

// SocketPath returns the socket path for a unix endpoint, or "" for any
// other endpoint (including the zero value).
func (e Endpoint) SocketPath() string { return e.path }

// Host returns the host for a TCP endpoint, or "" for any other endpoint.
func (e Endpoint) Host() string { return e.host }

// Port returns the port for a TCP endpoint, or "" for any other endpoint.
func (e Endpoint) Port() string { return e.port }

// String renders e in the ADR-0045 string form, the exact inverse of
// ParseEndpoint for any Endpoint ParseEndpoint itself produced.
func (e Endpoint) String() string {
	switch e.scheme {
	case schemeUnix:
		return "unix://" + e.path
	case schemeTCP:
		return "tcp://" + net.JoinHostPort(e.host, e.port)
	default:
		return ""
	}
}

// MarshalJSON renders e as its ADR-0045 string form -- the manifest's
// "endpoint" field is a string, not an object, so Endpoint's Go struct
// shape stays a private implementation detail on the wire.
func (e Endpoint) MarshalJSON() ([]byte, error) {
	return json.Marshal(e.String())
}

// UnmarshalJSON parses the "endpoint" field's string form via ParseEndpoint,
// so a malformed endpoint fails at Manifest-decode time with an
// *EndpointError, not later when some accessor is first called.
func (e *Endpoint) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	parsed, err := ParseEndpoint(raw)
	if err != nil {
		return err
	}
	*e = parsed
	return nil
}

// EndpointError reports why Raw could not be parsed as an Endpoint. It
// always names Raw in Error(), so a caller that only has the error (e.g. a
// warning logged from bindregistry, later) can still identify the
// offending endpoint without threading the raw string through separately.
type EndpointError struct {
	Raw    string
	Reason string
}

func (e *EndpointError) Error() string {
	return fmt.Sprintf("registrymanifest: endpoint %q: %s", e.Raw, e.Reason)
}

// ParseEndpoint parses the ADR-0045 endpoint string form, "unix://<path>"
// or "tcp://<host>:<port>". Neither scheme's host/port component is
// validated further here (e.g. a unix path is not checked for existence) --
// that is a later transport-probe concern, not a manifest-parsing one.
func ParseEndpoint(raw string) (Endpoint, error) {
	if raw == "" {
		return Endpoint{}, &EndpointError{Raw: raw, Reason: "empty"}
	}
	if path, ok := strings.CutPrefix(raw, "unix://"); ok {
		if path == "" {
			return Endpoint{}, &EndpointError{Raw: raw, Reason: "unix scheme has empty path"}
		}
		return NewUnixEndpoint(path), nil
	}
	if hostport, ok := strings.CutPrefix(raw, "tcp://"); ok {
		host, port, err := net.SplitHostPort(hostport)
		if err != nil || host == "" || port == "" {
			return Endpoint{}, &EndpointError{Raw: raw, Reason: "tcp scheme requires host:port"}
		}
		return NewTCPEndpoint(host, port), nil
	}
	return Endpoint{}, &EndpointError{Raw: raw, Reason: `unrecognized scheme, must be "unix://" or "tcp://"`}
}

// Route is one manifest route (ADR 0045): the prefix a Box-bound request
// arrives carrying, the upstream host it's rewritten toward, and the cargo
// registry names (if any) this route's CARGO_REGISTRIES_<NAME>_TOKEN
// placeholders are derived from. HostRooted carries registryproxy.Route's
// same-named field (ADR 0047, issue #3256) through to the cargo render,
// which needs it to tell a route serving its upstream host's real path
// layout (one local index URL per registry) from a legacy base-path route
// (one local index URL per route).
type Route struct {
	Prefix          string   `json:"prefix"`
	UpstreamHost    string   `json:"upstreamHost"`
	CargoRegistries []string `json:"cargoRegistries,omitempty"`
	HostRooted      bool     `json:"hostRooted,omitempty"`
}

// Manifest is the full REGISTRY_PROXY_MANIFEST payload (ADR 0045): the
// endpoint the proxy is reachable at, plus every route the Box needs.
type Manifest struct {
	Endpoint Endpoint `json:"endpoint"`
	Routes   []Route  `json:"routes"`
}

// ErrAbsent is returned by Parse for an empty string -- the distinct "no
// manifest" answer distinguishing REGISTRY_PROXY_MANIFEST unset/empty (the
// verb, later, stays silent) from a manifest present but malformed (the
// verb warns). Check with errors.Is, not equality, since Parse only ever
// returns this value directly, never wraps it.
var ErrAbsent = errors.New("registrymanifest: manifest absent")

// Encode renders m as the compact JSON string form REGISTRY_PROXY_MANIFEST
// carries.
func Encode(m Manifest) (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("registrymanifest: encoding: %w", err)
	}
	return string(b), nil
}

// validPrefixCharset reports whether prefix contains only [a-z0-9-] -- the
// exact charset the launcher's own minting side (registryproxy.isValidPrefix)
// already restricts a Prefix to. An empty prefix is not checked here; Parse
// treats "" as its own allowed, no-prefix case (existing callers, e.g.
// bindings mode's empty-prefix warn-and-skip, depend on it staying legal).
func validPrefixCharset(prefix string) bool {
	for i := 0; i < len(prefix); i++ {
		c := prefix[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// Parse decodes raw -- the REGISTRY_PROXY_MANIFEST value -- into a
// Manifest, validating its endpoint and every route's Prefix charset. raw ==
// "" returns ErrAbsent, checkable with errors.Is, so a caller can
// distinguish "no manifest" (silent) from every other error this returns
// (manifest present but malformed: bad JSON, an endpoint ParseEndpoint
// rejects, or a route Prefix outside [a-z0-9-]) -- the JSON and
// *EndpointError cases wrap the underlying error via %w, so errors.As still
// reaches it through this wrapper.
//
// The Prefix charset check is defense-in-depth, mirroring
// ecosystem.CargoRegistryPlaceholders' own belt-and-suspenders guard: the
// launcher only ever mints a Prefix from [a-z0-9-]
// (registryproxy.isValidPrefix), but the Box interpolates Prefix into a
// shell-sourced `export GOPROXY="…"` line and a Groovy double-quoted
// GString (the gradle init script), either of which would execute an
// unchecked character rather than merely misroute a request.
func Parse(raw string) (Manifest, error) {
	if raw == "" {
		return Manifest{}, ErrAbsent
	}
	var m Manifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return Manifest{}, fmt.Errorf("registrymanifest: parsing %s: %w", EnvVar, err)
	}
	for _, route := range m.Routes {
		if route.Prefix != "" && !validPrefixCharset(route.Prefix) {
			return Manifest{}, fmt.Errorf("registrymanifest: parsing %s: route prefix %q must contain only [a-z0-9-]", EnvVar, route.Prefix)
		}
	}
	return m, nil
}
