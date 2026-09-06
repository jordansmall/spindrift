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

// decodeOneJSONObject decodes body as exactly one JSON object, reporting
// ok=false for anything else -- not an object, or an object followed by
// trailing content -- so a rewriter can leave such a body untouched.
//
// body is decoded with json.Decoder's UseNumber(), not plain
// json.Unmarshal, so a numeric field elsewhere in the object survives
// re-serialization as the exact digits it arrived with instead of
// round-tripping through float64.
func decodeOneJSONObject(body []byte) (map[string]any, bool) {
	var obj map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&obj); err != nil {
		return nil, false
	}
	// Decode only consumes the first JSON value off the stream, so a body
	// with trailing content after that object (e.g. a second concatenated
	// object) would otherwise decode "successfully" and then silently drop
	// the trailing bytes on re-serialization by the caller. A second Decode
	// call into a throwaway value must fail with exactly io.EOF -- anything
	// else (nil, meaning another value follows; or a non-EOF error) means
	// body isn't exactly one JSON object.
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		return nil, false
	}
	return obj, true
}

// repointRegistryURL re-points one absolute URL drawn from a registry
// response body at the Forwarder (this proxy) instead of the real registry
// -- route-relative, with the route's prefix re-inserted ahead of it -- so
// a later download request built from that value round-trips back through
// the same route rather than straight at the upstream.
//
// ok is false when value isn't an absolute URL at all, leaving it to the
// caller to decide what an unusable value means for the body as a whole.
// Otherwise the returned edit is either the applied rewrite (From, To and
// LearnedPath all set) or, for a foreign host, the row declining that value
// (From alone, empty To -- see RewriteEdit's doc comment).
func repointRegistryURL(value string, rc registryvocab.RewriteContext) (registryvocab.RewriteEdit, bool) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return registryvocab.RewriteEdit{}, false
	}

	// A value naming any host other than the route's own match-host -- a
	// CDN, a mirror -- is left exactly alone: rewriting it would turn this
	// proxy into an open relay for whatever host that value happens to
	// name. Normalized by dropping a default port for the value's own
	// scheme on both sides before comparing, so "host:443" still matches a
	// bare "host" over https, and vice versa.
	if !strings.EqualFold(normalizeHostPort(u.Host, u.Scheme), normalizeHostPort(rc.MatchHost, u.Scheme)) {
		return registryvocab.RewriteEdit{From: value}, true
	}

	// The route's upstream is a bare origin, so the value's path is already
	// route-relative as the registry rendered it; only the route's prefix
	// goes on in front, and the Rewrite hook forwards that remainder
	// verbatim on the round trip.
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

// normalizeHostPort lowercases hostport and, when its port is the default
// port for scheme (443 for https, 80 for http), strips the port -- so a
// match-host written without an explicit port still compares equal to a
// rewritten value's host that spells the same default out explicitly, and
// vice versa. hostport with no port at all (net.SplitHostPort's "missing
// port" error) is returned lowercased unchanged.
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
