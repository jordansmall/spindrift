// Package registrymanifest is the shared handoff type for the registry
// proxy's Box-facing contract (ADR 0045). The launcher mints a Manifest and
// encodes it into one environment variable, the bind-registry verb parses it
// back, and both import this package so the shape cannot drift.
package registrymanifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"

	"spindrift.dev/launcher/internal/registryvocab"
)

// EnvVar names the environment variable carrying the encoded manifest (ADR
// 0045). Both sides import it, so a rename cannot split the handoff.
const EnvVar = "REGISTRY_PROXY_MANIFEST"

// TCPSecretHeader carries the per-run TCP secret on every Box request when
// the proxy is served over loopback TCP (issue #3111). A unix socket needs
// none: its file permissions already gate access.
const TCPSecretHeader = "X-Spindrift-Registry-Proxy-Secret"

// endpointScheme is one of the two ADR-0045 forms, "unix://<path>" or
// "tcp://<host>:<port>". The transport probe (issue #3111) always picks one,
// so the zero value exists only before a constructor has run.
type endpointScheme string

const (
	schemeUnix endpointScheme = "unix"
	schemeTCP  endpointScheme = "tcp"
)

// Endpoint is the manifest's "endpoint" field (ADR 0045): a unix socket path
// or a TCP host:port pair. The fields stay unexported so a caller cannot
// build an incoherent value; use the constructors or ParseEndpoint.
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

// NewTCPEndpoint builds a TCP Endpoint from a host and port. The TCP secret
// is deliberately not a field: ADR 0045 keeps it in a separate env var.
func NewTCPEndpoint(host, port string) Endpoint {
	return Endpoint{scheme: schemeTCP, host: host, port: port}
}

// IsUnix reports whether e is a unix-domain-socket endpoint.
func (e Endpoint) IsUnix() bool { return e.scheme == schemeUnix }

// IsTCP reports whether e is a TCP endpoint.
func (e Endpoint) IsTCP() bool { return e.scheme == schemeTCP }

// SocketPath returns the socket path for a unix endpoint, or "" for any other endpoint.
func (e Endpoint) SocketPath() string { return e.path }

// Host returns the host for a TCP endpoint, or "" for any other endpoint.
func (e Endpoint) Host() string { return e.host }

// Port returns the port for a TCP endpoint, or "" for any other endpoint.
func (e Endpoint) Port() string { return e.port }

// String renders e in the ADR-0045 string form, the inverse of ParseEndpoint.
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

// MarshalJSON renders e as its ADR-0045 string form: the manifest's
// "endpoint" field is a string on the wire, not an object.
func (e Endpoint) MarshalJSON() ([]byte, error) {
	return json.Marshal(e.String())
}

// UnmarshalJSON parses the "endpoint" field through ParseEndpoint, so a bad
// endpoint fails at decode time with an *EndpointError, not at first use.
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

// EndpointError reports why Raw could not be parsed as an Endpoint. Error()
// always names Raw, so a caller holding only the error can identify it.
type EndpointError struct {
	Raw    string
	Reason string
}

func (e *EndpointError) Error() string {
	return fmt.Sprintf("registrymanifest: endpoint %q: %s", e.Raw, e.Reason)
}

// ParseEndpoint parses "unix://<path>" or "tcp://<host>:<port>" (ADR 0045).
// It checks no further: a unix path's existence is a transport-probe concern.
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

// Route is one manifest route (ADR 0045): the prefix a request arrives
// carrying, the upstream host it is rewritten toward, and its declarations.
type Route struct {
	Prefix       string `json:"prefix"`
	UpstreamHost string `json:"upstreamHost"`
	// EnforcedPaths copies registryproxy.Route's EnforcedSubtrees (issue
	// #3259), tagged by declaring ecosystem so a client-side binding renderer
	// can pick out its own paths before a Box has a checkout to re-derive
	// them from.
	EnforcedPaths []registryvocab.Subtree `json:"enforcedPaths,omitempty"`
	// Ecosystems carries the route's [routes.ecosystems.<name>] blocks (issue
	// #3403), which each Box-side binding renderer reads its own entry out of
	// (issue #3404). A route declaring none omits the field, never emits it
	// empty.
	Ecosystems registryvocab.RouteEcosystems `json:"ecosystems,omitempty"`
}

// Manifest is the full REGISTRY_PROXY_MANIFEST payload (ADR 0045): the
// endpoint the proxy is reachable at, plus every route the Box needs.
type Manifest struct {
	Endpoint Endpoint `json:"endpoint"`
	Routes   []Route  `json:"routes"`
}

// ErrAbsent is Parse's distinct "no manifest" answer for an empty string, so
// the verb can stay silent when REGISTRY_PROXY_MANIFEST is unset and warn
// when one is present but malformed. Check it with errors.Is.
var ErrAbsent = errors.New("registrymanifest: manifest absent")

// Encode renders m as the compact JSON string REGISTRY_PROXY_MANIFEST carries.
func Encode(m Manifest) (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("registrymanifest: encoding: %w", err)
	}
	return string(b), nil
}

// validPrefixCharset reports whether prefix contains only [a-z0-9-], the
// charset registryproxy.isValidPrefix already mints within. An empty prefix
// passes: Parse treats "" as its own legal no-prefix case, which callers
// such as bindings mode's warn-and-skip depend on.
func validPrefixCharset(prefix string) bool {
	for i := 0; i < len(prefix); i++ {
		c := prefix[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// Parse decodes the REGISTRY_PROXY_MANIFEST value into a Manifest. An empty
// raw returns ErrAbsent; other failures wrap with %w, so errors.As still
// reaches an *EndpointError. The Prefix charset check is defense in depth: the
// Box interpolates Prefix into a shell-sourced `export GOPROXY="…"` line and
// a Groovy double-quoted GString, where an unchecked character would execute.
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
