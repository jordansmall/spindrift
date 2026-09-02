package registrydiscover

import (
	"io"
	"net/http"
	"strings"
	"time"
)

// defaultProbeTimeout bounds HTTPProbe's GET -- discovery must not hang the
// CLI on an unreachable or slow registry; a probe result defaults to
// "bearer" either way (see HTTPProbe), so a short bound costs nothing.
const defaultProbeTimeout = 10 * time.Second

// DefaultProbeClient returns the client the CLI uses for HTTPProbe: a short,
// bounded timeout so an unreachable registry falls back to "bearer" quickly
// instead of hanging discovery.
func DefaultProbeClient() *http.Client {
	return &http.Client{Timeout: defaultProbeTimeout}
}

// HTTPProbe is the production Probe (see Discover): it issues a GET to the
// upstream base URL and reads the auth scheme from the registry's
// WWW-Authenticate answer, defaulting to "bearer" when the registry is
// unreachable, answers without the header, or names a scheme discovery does
// not model.
func HTTPProbe(client *http.Client, upstreamBaseURL string) string {
	resp, err := client.Get(upstreamBaseURL)
	if err != nil {
		return "bearer"
	}
	// Drain (bounded) before Close so the underlying connection is eligible
	// for reuse by http.Transport's connection pool -- an unread body forces
	// the transport to close the connection instead.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	defer resp.Body.Close()

	return probeAuthScheme(resp.Header.Get("WWW-Authenticate"))
}

// probeAuthScheme reads the leading scheme token off a WWW-Authenticate
// header value (case-insensitive), never its params -- discovery only
// proposes AuthScheme, an operator-reviewed guess (normalizeAuthScheme
// re-validates it), so there is no reason to parse realm/scope/etc here.
func probeAuthScheme(wwwAuthenticate string) string {
	scheme, _, _ := strings.Cut(strings.TrimSpace(wwwAuthenticate), " ")
	switch strings.ToLower(scheme) {
	case "bearer":
		return "bearer"
	case "basic":
		return "basic"
	default:
		return "bearer"
	}
}
