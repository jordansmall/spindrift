package registrydiscover

import (
	"io"
	"net/http"
	"strings"
	"time"
)

// defaultProbeTimeout keeps an unreachable or slow registry from hanging the
// CLI. A failed probe falls back to "bearer", so a short bound costs nothing.
const defaultProbeTimeout = 10 * time.Second

// DefaultProbeClient returns the client the CLI uses for HTTPProbe.
func DefaultProbeClient() *http.Client {
	return &http.Client{Timeout: defaultProbeTimeout}
}

// HTTPProbe is the production Probe: it reads the auth scheme from the
// registry's WWW-Authenticate response header. It returns "bearer" when the
// registry is unreachable, omits the header, or names a scheme discovery does
// not model.
func HTTPProbe(client *http.Client, upstreamBaseURL string) string {
	resp, err := client.Get(upstreamBaseURL)
	if err != nil {
		return "bearer"
	}
	// Drain before Close so http.Transport can pool the connection; an unread
	// body forces the transport to close it instead.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	defer resp.Body.Close()

	return probeAuthScheme(resp.Header.Get("WWW-Authenticate"))
}

// probeAuthScheme reads only the leading scheme token, never its params.
// Discovery proposes an AuthScheme that an operator reviews and
// normalizeAuthScheme re-validates, so realm and scope have no use here.
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
