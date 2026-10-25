package registrydiscover

import (
	"io"
	"net/http"
	"strings"
	"time"
)

// defaultProbeTimeout keeps an unreachable or slow registry from hanging the
// CLI. A failed probe reports no usable challenge rather than an error, so a
// short bound costs nothing.
const defaultProbeTimeout = 10 * time.Second

// DefaultProbeClient returns the client the CLI uses for HTTPProbe.
func DefaultProbeClient() *http.Client {
	return &http.Client{Timeout: defaultProbeTimeout}
}

// HTTPProbe is the production Probe: it reads the registry's response with
// ReadProbeResponse. It returns the zero ProbeResult when the registry is
// unreachable.
func HTTPProbe(client *http.Client, upstreamBaseURL string) ProbeResult {
	resp, err := client.Get(upstreamBaseURL)
	if err != nil {
		return ProbeResult{}
	}
	// Drain before Close so http.Transport can pool the connection; an unread
	// body forces the transport to close it instead.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	defer resp.Body.Close()

	return ReadProbeResponse(resp.Header)
}

// ReadProbeResponse reads a probe result from one registry response's headers.
// HTTPProbe and the corpus harness both call it, so a fixture exercises the
// live parsing.
func ReadProbeResponse(header http.Header) ProbeResult {
	return ProbeResult{
		Scheme: probeAuthScheme(header.Get("WWW-Authenticate")),
		Header: header,
	}
}

// probeAuthScheme reads only the leading scheme token, never its params, and
// answers "" for an absent or unmodelled scheme so callers can tell no usable
// challenge from a real bearer one. Discovery proposes an AuthScheme that an
// operator reviews and normalizeAuthScheme re-validates, so realm and scope
// have no use here.
func probeAuthScheme(wwwAuthenticate string) string {
	scheme, _, _ := strings.Cut(strings.TrimSpace(wwwAuthenticate), " ")
	switch strings.ToLower(scheme) {
	case "bearer":
		return "bearer"
	case "basic":
		return "basic"
	default:
		return ""
	}
}
