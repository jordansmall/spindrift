package registryproxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// rewriteOutcome distinguishes why rewriteCargoDL did or didn't rewrite a
// body -- specifically, it lets the caller tell the two deliberate-skip
// outcomes -- rewriteSkippedForeignHost (dl names a host other than the
// route's own match-host) and rewriteSkippedOutsideBasePath (dl's host
// matches, but its path isn't under the route's own upstream base path, so
// it can't be expressed as a route-relative URL at all) -- apart from
// rewriteNone (nothing recognizable to rewrite at all: not JSON, no dl
// field, dl not a string, or malformed). All three non-applied outcomes are
// logged by the caller (modifyResponse) -- rewriteNone's line names only the
// row, since there's no dl value to name (issue #3175's blocking review
// finding: a matched-but-unrewritten body was previously undiagnosable).
type rewriteOutcome int

const (
	rewriteNone rewriteOutcome = iota
	rewriteSkippedForeignHost
	rewriteSkippedOutsideBasePath
	rewriteApplied
)

// rewriteContext bundles the per-route facts a responseRewriteRow's
// rewriter needs about the request that produced the response it's
// rewriting: the route's own match-host and upstream base path (to decide
// whether a dl is rewritable at all and, if so, its route-relative
// remainder), the Forwarder address to point a rewritten dl at, and the
// route's prefix to re-insert ahead of that remainder.
type rewriteContext struct {
	matchHost   string
	forwarder   *url.URL
	prefix      string
	upstreamURL *url.URL // the route's configured upstream base URL; only its Path/EscapedPath are consulted
}

// rewriteResult is what a responseRewriteRow's rewriter reports back: the
// (possibly untouched) body, the dl's before/after values for the caller's
// log line, and the outcome that decides which of those two apply.
type rewriteResult struct {
	body    []byte
	from    string
	to      string
	outcome rewriteOutcome
}

// responseRewriteRow binds one exact request shape -- method and
// route-relative (prefix-stripped, decoded) path -- to a response-body
// rewriter. The table below is matched by exact equality only: a shape with
// no row here is never inspected at all, not even to sniff its Content-Type
// -- issue #2854's wrong-media-type defect (content-sniffing to detect JSON)
// is excluded by construction, not by a runtime guard.
type responseRewriteRow struct {
	name    string // shape name, used only in log lines (e.g. "cargo config.json")
	method  string // e.g. http.MethodGet; no row names HEAD, so a HEAD response never matches any row
	path    string // matched against the decoded, route-relative selectedRoute.path
	rewrite func(body []byte, rc rewriteContext) rewriteResult
}

// responseRewriteTable is the package's full v1 rewrite table: one row, for
// cargo's sparse-index config.json "dl" field (ADR 0045, issue #3175).
var responseRewriteTable = []responseRewriteRow{
	{name: "cargo config.json", method: http.MethodGet, path: "/config.json", rewrite: rewriteCargoDL},
}

// findResponseRewriteRow returns the responseRewriteTable row matching
// method and path exactly, or nil when no row does.
func findResponseRewriteRow(method, path string) *responseRewriteRow {
	for i := range responseRewriteTable {
		if responseRewriteTable[i].method == method && responseRewriteTable[i].path == path {
			return &responseRewriteTable[i]
		}
	}
	return nil
}

// rewriteCargoDL rewrites a cargo sparse-index config.json body's "dl" field
// so it points at the Forwarder (this proxy) instead of the real registry --
// route-relative, with the route's prefix re-inserted ahead of it -- so a
// later crate-download request (which cargo builds by joining dl with a
// crate path) round-trips back through the same route rather than straight
// at the upstream. Pure: no I/O, no logging -- the caller logs
// from(before)/to(after) keyed off outcome.
//
// body is decoded with json.Decoder's UseNumber(), not plain
// json.Unmarshal, so a numeric field elsewhere in the object survives
// re-serialization as the exact digits it arrived with instead of
// round-tripping through float64.
func rewriteCargoDL(body []byte, rc rewriteContext) rewriteResult {
	var obj map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&obj); err != nil {
		return rewriteResult{body: body, outcome: rewriteNone}
	}
	// Decode only consumes the first JSON value off the stream, so a body
	// with trailing content after that object (e.g. a second concatenated
	// object) would otherwise decode "successfully" and then silently drop
	// the trailing bytes on re-serialization below. A second Decode call
	// into a throwaway value must fail with exactly io.EOF -- anything else
	// (nil, meaning another value follows; or a non-EOF error) means body
	// isn't exactly one JSON object, so it's left untouched.
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		return rewriteResult{body: body, outcome: rewriteNone}
	}

	dlRaw, ok := obj["dl"]
	if !ok {
		return rewriteResult{body: body, outcome: rewriteNone}
	}
	dlStr, ok := dlRaw.(string)
	if !ok {
		return rewriteResult{body: body, outcome: rewriteNone}
	}

	dlURL, err := url.Parse(dlStr)
	if err != nil || dlURL.Scheme == "" || dlURL.Host == "" {
		return rewriteResult{body: body, outcome: rewriteNone}
	}

	// A dl naming any host other than the route's own match-host -- a CDN,
	// a mirror -- is left exactly alone: rewriting it would turn this
	// proxy into an open relay for whatever host a dl happens to name.
	// Normalized by dropping a default port for the dl's own scheme on
	// both sides before comparing, so "host:443" (dl) still matches a
	// bare "host" (matchHost) over https, and vice versa.
	if !strings.EqualFold(normalizeHostPort(dlURL.Host, dlURL.Scheme), normalizeHostPort(rc.matchHost, dlURL.Scheme)) {
		return rewriteResult{body: body, from: dlStr, outcome: rewriteSkippedForeignHost}
	}

	// The rewritten dl must be route-relative: the route's own upstream
	// base path is stripped from dl's path before the prefix goes back on,
	// so a later request built from this dl re-enters the proxy carrying
	// only the remainder -- letting the Rewrite hook join that remainder
	// onto upstreamURL exactly once. Without this, the base path -- already
	// present in dl because the real registry rendered it there -- would
	// get joined a second time on the round trip (issue #3175's blocking
	// finding, one hop past #2854's original path-double-join defect).
	rest, ok := stripBasePath(dlURL.Path, rc.upstreamURL.Path)
	rawRest, rawOK := stripBasePath(dlURL.EscapedPath(), rc.upstreamURL.EscapedPath())
	if !ok || !rawOK {
		// dl's path isn't under this route's upstream base path at all --
		// there's no route-relative URL that expresses it, and rewriting it
		// anyway would send the round-trip request to the wrong upstream
		// path. Left exactly alone, like a foreign host.
		return rewriteResult{body: body, from: dlStr, outcome: rewriteSkippedOutsideBasePath}
	}

	newURL := &url.URL{
		Scheme:   rc.forwarder.Scheme,
		Host:     rc.forwarder.Host,
		Path:     "/" + rc.prefix + rest,
		RawPath:  "/" + rc.prefix + rawRest,
		RawQuery: dlURL.RawQuery,
		Fragment: dlURL.Fragment,
	}
	to := newURL.String()
	from := dlStr

	obj["dl"] = to

	newBody, err := json.Marshal(obj)
	if err != nil {
		// Unreachable in practice: obj came from a successful json.Decode
		// above, so every value in it is already representable as JSON.
		return rewriteResult{body: body, outcome: rewriteNone}
	}

	return rewriteResult{body: newBody, from: from, to: to, outcome: rewriteApplied}
}

// stripBasePath removes base from the front of path at a segment boundary
// and reports whether it did: path must equal base exactly, or continue
// immediately with "/" right after it. A base of "" or "/" means the route
// has no base path at all, so every path is already route-relative and is
// returned unchanged. Matching only a segment boundary -- never a bare
// strings.HasPrefix -- is what stops an upstream base path of "/repo" from
// wrongly stripping off the front of "/repository/x", which merely happens
// to start with the same characters (issue #3175's blocking finding).
func stripBasePath(path, base string) (rest string, ok bool) {
	if base == "" || base == "/" {
		return path, true
	}
	base = strings.TrimSuffix(base, "/")
	if path == base {
		return "", true
	}
	if strings.HasPrefix(path, base+"/") {
		return path[len(base):], true
	}
	return "", false
}

// normalizeHostPort lowercases hostport and, when its port is the default
// port for scheme (443 for https, 80 for http), strips the port -- so a
// match-host written without an explicit port still compares equal to a dl
// host that spells the same default out explicitly, and vice versa.
// hostport with no port at all (net.SplitHostPort's "missing port" error)
// is returned lowercased unchanged.
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
