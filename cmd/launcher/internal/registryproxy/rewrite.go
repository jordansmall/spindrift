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
// body -- specifically, it lets the caller tell the deliberate skip,
// rewriteSkippedForeignHost (dl names a host other than the route's own
// match-host), apart from rewriteNone (nothing recognizable to rewrite at
// all: not JSON, no dl field, dl not a string, or malformed). Both
// non-applied outcomes are logged by the caller (modifyResponse) --
// rewriteNone's line names only the row, since there's no dl value to name
// (issue #3175's blocking review finding: a matched-but-unrewritten body was
// previously undiagnosable).
type rewriteOutcome int

const (
	rewriteNone rewriteOutcome = iota
	rewriteSkippedForeignHost
	rewriteApplied
)

// rewriteContext bundles the per-route facts a responseRewriteRow's
// rewriter needs about the request that produced the response it's
// rewriting: the route's own match-host (to decide whether a dl is
// rewritable at all), the Forwarder address to point a rewritten dl at, and
// the route's prefix to re-insert ahead of the dl's path.
type rewriteContext struct {
	matchHost string
	forwarder *url.URL
	prefix    string
}

// rewriteResult is what a responseRewriteRow's rewriter reports back: the
// (possibly untouched) body, the dl's before/after values for the caller's
// log line, the outcome that decides which of those two apply, and (only
// on rewriteApplied) the route-relative subtree the dl was found under, for
// modifyResponse to learn into the route's enforced set (ADR 0047).
type rewriteResult struct {
	body    []byte
	from    string
	to      string
	outcome rewriteOutcome
	// set only on rewriteApplied; "/" is used for "no base segment" instead
	// of "" because registryvocab.PathSet.Admits' HasPrefix(cleaned,
	// sub+"/") branch would treat "" as admit-everything too -- "/" is the
	// normalized, self-documenting sentinel, not an unwidened one.
	learnedPath string
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
// method and path, plus the cargo index base the match was found under, or
// (nil, "") when no row matches.
//
// A row matches iff path equals base+row.path for some base in
// rs.cargoIndexBases, checked by membership rather than by stripping a
// suffix off path -- ADR 0047 keys the row on the exact derived index bases
// the host-side derivation already enumerates, never by guessing from the
// request path itself. base == "/" is the sentinel for "no base segment at
// all" (mirrors registrypathset's own root-subtree convention), so it
// combines with row.path as row.path itself, not "//config.json".
func findResponseRewriteRow(method, path string, rs routeState) (*responseRewriteRow, string) {
	for i := range responseRewriteTable {
		row := &responseRewriteTable[i]
		if row.method != method {
			continue
		}
		for _, base := range rs.cargoIndexBases {
			want := row.path
			if base != "/" {
				want = base + row.path
			}
			if path == want {
				return row, base
			}
		}
	}
	return nil, ""
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

	// The route's upstream is a bare origin, so dl's path is already
	// route-relative as the registry rendered it; only the route's prefix
	// goes on in front, and the Rewrite hook forwards that remainder
	// verbatim on the round trip.
	rest := dlURL.Path
	rawRest := dlURL.EscapedPath()

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

	learnedPath := rest
	if learnedPath == "" {
		learnedPath = "/"
	}
	return rewriteResult{body: newBody, from: from, to: to, outcome: rewriteApplied, learnedPath: learnedPath}
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
