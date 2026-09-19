package ecosystem

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/url"
	"strings"

	"spindrift.dev/launcher/internal/registryvocab"
)

// decodeOneJSONObject decodes body as exactly one JSON object. UseNumber keeps
// numeric fields as the exact digits they arrived with, so re-serialization
// does not round-trip them through float64.
func decodeOneJSONObject(body []byte) (map[string]any, bool) {
	var obj map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&obj); err != nil {
		return nil, false
	}
	// Decode consumes only the first JSON value, so a body with trailing
	// content would decode fine here and then lose those bytes when the
	// caller re-serializes. Exactly io.EOF means nothing follows.
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		return nil, false
	}
	return obj, true
}

// repointRegistryURL re-points one absolute URL from a registry response body at
// the Forwarder, with the route's prefix re-inserted, so a later download built
// from that value round-trips back through the same route. A foreign host is
// declined rather than rewritten, returning ok=true with an empty To.
func repointRegistryURL(value string, rc registryvocab.RewriteContext) (registryvocab.RewriteEdit, bool) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return registryvocab.RewriteEdit{}, false
	}

	// Rewriting a value that names any host but the route's match-host would
	// make this proxy an open relay for that host. Both sides drop a default
	// port for the value's scheme first, so "host:443" still matches a bare
	// "host" over https.
	if !strings.EqualFold(normalizeHostPort(u.Host, u.Scheme), normalizeHostPort(rc.MatchHost, u.Scheme)) {
		return registryvocab.RewriteEdit{From: value}, true
	}

	// The route's upstream is a bare origin, so the value's path is already
	// route-relative; only the route's prefix goes in front.
	rest := u.Path
	rawRest := u.EscapedPath()

	newURL := &url.URL{
		Scheme:   rc.Forwarder.Scheme,
		Host:     rc.Forwarder.Host,
		Path:     "/" + rc.Prefix + rest,
		RawPath:  "/" + rc.Prefix + rawRest,
		RawQuery: u.RawQuery,
		Fragment: u.Fragment,
	}

	learnedPath := rest
	if learnedPath == "" {
		learnedPath = "/"
	}
	return registryvocab.RewriteEdit{From: value, To: newURL.String(), LearnedPath: learnedPath}, true
}

// normalizeHostPort lowercases hostport and strips a default port for scheme
// (443 for https, 80 for http), so a match-host written without a port compares
// equal to one that spells the default out. A hostport with no port at all is
// returned lowercased unchanged.
func normalizeHostPort(hostport, scheme string) string {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return strings.ToLower(hostport)
	}
	if (port == "443" && strings.EqualFold(scheme, "https")) || (port == "80" && strings.EqualFold(scheme, "http")) {
		return strings.ToLower(host)
	}
	return strings.ToLower(host + ":" + port)
}
