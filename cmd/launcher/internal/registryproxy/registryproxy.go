// Package registryproxy implements a GET/HEAD-only pass-through reverse
// proxy served over a unix domain socket, forwarding to one of a table of
// upstream routes selected by path prefix and optionally attaching a
// launcher-resolved credential to the outbound (proxy->registry) leg (ADR
// 0044, ADR 0045, issue #3142). An inbound request selects its route by the
// first segment of its path (e.g. "/r0/crates/foo" selects the route whose
// Prefix is "r0"); that segment is stripped before the remainder is joined
// onto the selected route's upstream URL. A request whose first segment
// names no configured route's Prefix is refused with 404 before any
// upstream is dialed.
//
// httputil.ReverseProxy is single-hop: it relays whatever the upstream
// responds with -- including a 3xx redirect's status and Location header --
// straight back to the client without ever following it itself. So a
// redirect response is returned to the client as-is, which then fetches the
// target directly and unauthenticated, satisfying ADR 0044's requirement
// that the credential never cross a redirect hop.
package registryproxy

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"spindrift.dev/launcher/internal/registrymanifest"
)

// Route is one resolved entry in the proxy's route table: an inbound request
// picks it by Prefix (derived from MatchHost, see AssignPrefixes), Upstream
// is where matching requests are forwarded, and Credential (already
// launcher-resolved to its final value, never a reference such as a file
// path or env var name) is attached per AuthScheme. Building this from a
// TOML routes file is the caller's job (ADR 0045) -- this package never
// resolves a credential or parses a routes file itself.
type Route struct {
	// MatchHost is the host[:port] this route was declared against in the
	// routes file (ADR 0045); this package no longer routes an inbound
	// request by its Host header, so MatchHost's only remaining role here is
	// as the input AssignPrefixes derives Prefix from.
	MatchHost string
	// Upstream is the absolute base URL requests on this route are forwarded
	// to. It may carry a base path; no trailing slash.
	Upstream string
	// AuthScheme selects how Credential is rendered onto the outbound
	// request: "bearer" (the default when empty), "basic", or
	// "header:<Name>". See authHeader.
	AuthScheme string
	// Credential is the resolved value to attach; empty means this route is
	// an unauthenticated pass-through regardless of AuthScheme.
	Credential string
	// Prefix is the stable path prefix (the first URL path segment) a
	// Forwarder-facing request names to select this route. It is derived
	// from MatchHost by AssignPrefixes at route-synthesis time and carried
	// unchanged into the manifest from then on -- it is never re-derived
	// mid-run (ADR 0045).
	Prefix string
	// CargoRegistries is carried metadata for the manifest (ADR 0045): the
	// Target repo's [registries.NAME] names this route serves. This package
	// never reads it -- routing is by Prefix only.
	CargoRegistries []string
	// EnforceAllowlist promotes the derived allowlist from log-only to
	// enforced for this route: a route-relative path outside it is refused
	// with 403 rather than merely logged and relayed (issue #3177). Default
	// false keeps the advisory posture every route had before this field
	// existed; this is a tightening-only knob, never a way to loosen it.
	EnforceAllowlist bool
	// HostRooted selects host-rooted serving (ADR 0047, issue #3256):
	// Upstream is a bare origin with no path (New rejects one that has a
	// path), and every request is checked against EnforcedPaths
	// unconditionally, rather than only when a knob like EnforceAllowlist
	// opts in -- a host-rooted route has no base path of its own to bound
	// what it might otherwise forward.
	HostRooted bool
	// EnforcedPaths is the path-set a host-rooted route's requests are
	// checked against; ignored when HostRooted is false. Entries arrive
	// already normalized by the caller -- leading "/", no trailing "/", and
	// "/" meaning the whole host -- matching what
	// registrypathset.HostPathSet.Admits derives; this package does not
	// import that one (see pathSetAdmits) and so does not re-derive or
	// re-normalize them. A host-rooted route with an empty EnforcedPaths
	// refuses every request rather than falling back to some default --
	// emptiness is a legitimately derived "nothing declared".
	EnforcedPaths []string
}

// inlineAuthSchemes are the HTTP auth schemes a credential may name inline,
// each with its delimiting space (issue #3124). cargo sends a
// credentials.toml token verbatim as the Authorization header value rather
// than prepending a scheme of its own, so a registry documenting a cargo
// setup has to bake the scheme into the token -- Artifactory's own "Set Me
// Up" emits `token = "Bearer <jwt>"`, and a route's cargo-credentials
// credential source (ADR 0045) reads exactly that file. A credential
// arriving already schemed is the whole header value; prefixing a second
// "Bearer " produced "Bearer Bearer <jwt>" and a 401.
var inlineAuthSchemes = []string{"Bearer ", "Basic ", "token "}

// authorizationHeaderValue renders credential into an Authorization header
// value: verbatim when it already names one of inlineAuthSchemes, otherwise
// prefixed with "Bearer " as it always was. Honouring an inline scheme is
// also what gives the proxy HTTP Basic support, which it could not otherwise
// express. Pure: does no I/O and touches no process state.
//
// Only a genuine prefix counts, and the scheme must be followed by a
// non-empty remainder -- a bare "Bearer", a "Bearer" with nothing after the
// space, and a token merely containing a scheme word later on are all
// ordinary opaque credentials that still get prefixed. The scheme word
// itself matches case-insensitively, since RFC 7235 auth schemes are
// case-insensitive and a registry's docs may spell it any way.
func authorizationHeaderValue(credential string) string {
	for _, scheme := range inlineAuthSchemes {
		if len(credential) > len(scheme) && strings.EqualFold(credential[:len(scheme)], scheme) {
			return credential
		}
	}
	return "Bearer " + credential
}

// routeState is a Route after New has parsed and pre-rendered it: the
// per-request Rewrite hook only ever reads this, never Route itself.
type routeState struct {
	prefix           string // Route.Prefix; selects this route by the request's first path segment
	matchHost        string // Route.MatchHost; the response-rewrite table compares a rewritten dl's host against this, not against upstreamURL.Host
	upstreamURL      *url.URL
	upstreamQuery    string
	headerName       string // "" when the route has no credential to attach
	headerValue      string
	enforceAllowlist bool     // Route.EnforceAllowlist; see ServeHTTP's enforcement check
	hostRooted       bool     // Route.HostRooted; see ServeHTTP's unconditional enforcement branch
	enforcedPaths    []string // Route.EnforcedPaths
}

// hostOnly lowercases hostport and strips any ":port" suffix, so AssignPrefixes
// derives a route's Prefix from the hostname alone, ignoring any port a
// MatchHost happens to carry. A hostport with no port (net.SplitHostPort's
// "missing port" error) also has a single enclosing "[" "]" bracket pair
// stripped, if present, before lowercasing -- otherwise "[::1]" (no port) and
// "[::1]:443" would normalize to different strings even though they name the
// same host.
func hostOnly(hostport string) string {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
		if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
			host = host[1 : len(host)-1]
		}
	}
	return strings.ToLower(host)
}

// selectedRoute is what selectRoute computes once per request (route +
// stripped remainder) and the Rewrite hook then joins onto the route's
// upstream URL.
type selectedRoute struct {
	rs      routeState
	path    string // "", together with rawPath == "", means "no remainder: forward the upstream URL verbatim"
	rawPath string
	// forwarder is the scheme+host the inbound request itself was addressed
	// to (r.Host, with scheme chosen by r.TLS) -- the address a rewritten dl
	// must name so a later crate-download request routes back through this
	// proxy. Set by ServeHTTP, not selectRoute (selectRoute never sees the
	// inbound *http.Request). nil when r.Host was empty (an HTTP/1.0 client
	// sent no Host header): the Forwarder address is then unknowable, and
	// ModifyResponse skips rewriting rather than guess one.
	forwarder *url.URL
}

// selectRoute picks the routeState whose Prefix equals the first segment of
// escapedPath (an inbound request's r.URL.EscapedPath()) and packages it as
// a selectedRoute: path and rawPath are both "" when escapedPath was exactly
// "/<prefix>" with nothing after it (the caller then forwards the selected
// route's upstream URL verbatim -- there is nothing to join); otherwise they
// are the remainder after the "/<prefix>" segment, still leading with "/",
// parsed into the same (decoded, escaped) pair a real request URL would
// carry. ok is false (selectedRoute is the zero value) when no route's
// Prefix matches the first segment, or the path is "/" or otherwise names no
// segment at all -- the caller must refuse the request before ReverseProxy
// (and any upstream dial) ever runs.
//
// The split happens on the escaped path, not the decoded Path field a
// *url.URL exposes: net/http has already decoded a percent-escaped slash
// (npm's %2f in a scoped package name) into a literal '/' in Path by the
// time a request reaches here, which would corrupt where the prefix
// segment ends if used for splitting. A percent-encoded "%2F" sequence
// stays those three literal characters in the escaped form, so splitting on
// a literal '/' byte there is safe.
func selectRoute(states []routeState, escapedPath string) (selectedRoute, bool) {
	if !strings.HasPrefix(escapedPath, "/") {
		return selectedRoute{}, false
	}
	rest := escapedPath[1:]
	segment, remainder := rest, ""
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		segment, remainder = rest[:i], rest[i:]
	}
	if segment == "" {
		return selectedRoute{}, false
	}
	for _, s := range states {
		if s.prefix != segment {
			continue
		}
		if remainder == "" {
			return selectedRoute{rs: s}, true
		}
		// remainder always starts with "/" here (the IndexByte split above
		// keeps it), so it is itself a valid HTTP request-target path.
		// ParseRequestURI, not the general-purpose Parse, is required: Parse
		// treats a leading "//" as a network-path (authority) reference
		// (e.g. a request of "/<prefix>//evil.example/x" would otherwise
		// have its remainder "//evil.example/x" misread as naming host
		// "evil.example" instead of the literal path it is). ParseRequestURI
		// matches how net/http itself parsed the inbound request's own
		// origin-form target, so a remainder it can't parse (essentially
		// unreachable, since it's a substring of the already-valid
		// escapedPath) is treated the same as no route matching.
		u, err := url.ParseRequestURI(remainder)
		if err != nil {
			return selectedRoute{}, false
		}
		return selectedRoute{rs: s, path: u.Path, rawPath: u.RawPath}, true
	}
	return selectedRoute{}, false
}

// authHeader renders scheme and credential into the header name and value
// New's Rewrite hook should set on the outbound request. An empty credential
// always renders to ("", ""): no header at all, whatever the scheme
// (unauthenticated pass-through). Otherwise:
//
//   - "" or "bearer" attach Authorization via authorizationHeaderValue,
//     unchanged from the single-upstream behaviour this replaces.
//   - "basic" attaches Authorization as HTTP Basic, honouring the same
//     inline-scheme rule as authorizationHeaderValue when credential already
//     names "Basic ", otherwise base64-encoding it (credential is expected
//     "user:password").
//   - "header:<Name>" attaches credential verbatim to the named header
//     instead of Authorization -- the JFrog X-JFrog-Art-Api pattern.
//
// Any other scheme is an error: defense in depth, since registryroutes
// validates the scheme name before it ever reaches here.
func authHeader(scheme, credential string) (headerName, headerValue string, err error) {
	if credential == "" {
		return "", "", nil
	}
	switch {
	case scheme == "" || strings.EqualFold(scheme, "bearer"):
		return "Authorization", authorizationHeaderValue(credential), nil
	case strings.EqualFold(scheme, "basic"):
		return "Authorization", basicHeaderValue(credential), nil
	case strings.HasPrefix(scheme, "header:"):
		name := strings.TrimPrefix(scheme, "header:")
		if name == "" {
			return "", "", fmt.Errorf("registryproxy: auth scheme %q names no header", scheme)
		}
		return name, credential, nil
	default:
		return "", "", fmt.Errorf("registryproxy: unknown auth scheme %q", scheme)
	}
}

// basicHeaderValue renders credential as an HTTP Basic Authorization header
// value: verbatim when it already names the "Basic " scheme (the same
// genuine-prefix rule as authorizationHeaderValue), otherwise base64-encoded
// per RFC 7617. credential is expected to be "user:password" in the latter
// case.
func basicHeaderValue(credential string) string {
	const prefix = "Basic "
	if len(credential) > len(prefix) && strings.EqualFold(credential[:len(prefix)], prefix) {
		return credential
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(credential))
}

// AssignPrefixes sets Prefix on each element of routes in place -- routes'
// own backing array is mutated, not copied, so a caller's slice is changed
// even if it ignores the return value -- deriving it from that route's
// MatchHost: hostOnly(MatchHost) with every character outside [a-z0-9]
// mapped to '-'.
func AssignPrefixes(routes []Route) []Route {
	used := make(map[string]bool, len(routes))  // every Prefix assigned so far, including generated "-N" ones
	counts := make(map[string]int, len(routes)) // base slug -> suffix count tried so far, for "-2", "-3", ...
	for i := range routes {
		base := slugify(hostOnly(routes[i].MatchHost))
		if base == "" {
			base = fmt.Sprintf("r%d", i)
		}
		candidate := base
		for used[candidate] {
			counts[base]++
			candidate = fmt.Sprintf("%s-%d", base, counts[base]+1)
		}
		used[candidate] = true
		routes[i].Prefix = candidate
	}
	return routes
}

// slugify maps every rune outside [a-z0-9] in s to '-'.
func slugify(s string) string {
	b := []byte(s)
	for i, c := range b {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			b[i] = '-'
		}
	}
	return string(b)
}

// isValidPrefix reports whether prefix contains only [a-z0-9-] -- the
// character set a Prefix must satisfy since it becomes the first URL path
// segment a Forwarder-facing request names to select its route.
func isValidPrefix(prefix string) bool {
	for i := 0; i < len(prefix); i++ {
		c := prefix[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// New builds an http.Handler that forwards GET and HEAD requests to one of
// routes and rejects every other method with 405 Method Not Allowed without
// forwarding it upstream. routes must be non-empty, and each route's Prefix
// must be non-empty, unique across routes, and composed only of [a-z0-9-]
// (see AssignPrefixes to derive one); New returns an error naming the
// offending route otherwise.
//
// Each request is dispatched by the first segment of its path: a path of
// "/<prefix>" or "/<prefix>/..." selects the route whose Prefix equals
// <prefix> (see selectRoute), that segment is stripped, and the remainder is
// joined onto the selected route's upstream URL -- so an upstream carrying
// its own base path (e.g. "https://host/artifactory") is forwarded to
// without a doubled slash or segment, and a request naming exactly
// "/<prefix>" with nothing after it forwards to that upstream URL verbatim.
// A request whose first path segment names no configured route's Prefix is
// refused with 404 before ReverseProxy runs, so no upstream is ever dialed
// for it.
//
// Each route's credential, when non-empty, is attached to every request
// forwarded on that route per its AuthScheme (ADR 0044, ADR 0045; see
// authHeader). An empty credential leaves that route an unauthenticated
// pass-through, unchanged from before. The rewrite also always sets the
// outbound Host header to the selected route's upstream host, regardless of
// what Host the inbound client request carried -- otherwise a
// client-controlled Host header would ride along with the credential to
// whatever vhost the client named. The credential is attached via
// ReverseProxy's Rewrite hook rather than its legacy Director, because
// Director runs before ReverseProxy strips hop-by-hop headers from the
// outbound request -- a client naming "Authorization" in its own Connection
// header would otherwise get the just-set Authorization header stripped
// right back out. Rewrite runs after that stripping, so what it sets
// survives untouched.
//
// The returned handler also accumulates allowlist-miss logging state (issue
// #3087) and upstream-failure logging state (issue #3125) across requests; a
// caller must eventually call Close() on it (directly, or via Proxy.Close()
// when the handler is wrapped in a Proxy) to flush the final suppressed-miss
// and suppressed-failure summaries, or those counts are silently dropped.
func New(routes []Route) (http.Handler, error) {
	if len(routes) == 0 {
		return nil, errors.New("registryproxy: no routes configured")
	}

	states := make([]routeState, len(routes))
	seenPrefixes := make(map[string]bool, len(routes))
	for i, route := range routes {
		if route.Prefix == "" {
			return nil, fmt.Errorf("registryproxy: route %q has no Prefix", route.MatchHost)
		}
		// Shape checked before uniqueness: an invalid Prefix that also
		// happens to duplicate another route's should report the more
		// specific "invalid characters" error, not "duplicate".
		if !isValidPrefix(route.Prefix) {
			return nil, fmt.Errorf("registryproxy: route %q: Prefix %q must contain only [a-z0-9-]", route.MatchHost, route.Prefix)
		}
		if seenPrefixes[route.Prefix] {
			return nil, fmt.Errorf("registryproxy: route %q: duplicate Prefix %q", route.MatchHost, route.Prefix)
		}
		seenPrefixes[route.Prefix] = true

		u, err := url.Parse(route.Upstream)
		if err != nil {
			return nil, fmt.Errorf("registryproxy: parse upstream URL %q: %w", route.Upstream, err)
		}
		if u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("registryproxy: upstream URL %q must be absolute", route.Upstream)
		}
		if route.HostRooted && u.Path != "" {
			// The Rewrite hook's join only forwards a host-rooted request's
			// verbatim remainder when Upstream contributes no path segment of
			// its own; checked here rather than relying on the join to no-op
			// correctly, since a non-empty path (even "/") would silently
			// prefix every forwarded request instead of failing loudly.
			return nil, fmt.Errorf("registryproxy: route %q: host-rooted Upstream %q must be a bare origin with no path", route.MatchHost, route.Upstream)
		}

		// Rendered once here rather than per request: a route's credential
		// is fixed for the proxy's lifetime.
		headerName, headerValue, err := authHeader(route.AuthScheme, route.Credential)
		if err != nil {
			return nil, fmt.Errorf("registryproxy: route %q: %w", route.MatchHost, err)
		}

		states[i] = routeState{
			prefix:           route.Prefix,
			matchHost:        route.MatchHost,
			upstreamURL:      u,
			upstreamQuery:    u.RawQuery,
			headerName:       headerName,
			headerValue:      headerValue,
			enforceAllowlist: route.EnforceAllowlist,
			hostRooted:       route.HostRooted,
			enforcedPaths:    route.EnforcedPaths,
		}
	}

	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			// The route and stripped remainder were already computed once,
			// by allowlistLogHandler.ServeHTTP before it ever calls into
			// this ReverseProxy -- both so a 404 for an unmatched prefix
			// never reaches here at all, and so the allowlist check and this
			// join agree on exactly the same stripped path rather than each
			// re-deriving it and risking drift.
			sel, ok := pr.In.Context().Value(selectedRouteContextKey{}).(selectedRoute)
			if !ok {
				// Unreachable in practice -- ServeHTTP always stashes a
				// selectedRoute before calling h.rp.ServeHTTP -- but Rewrite
				// has no ResponseWriter to answer with a real status, so
				// fail safe: leave pr.Out.URL as the untouched, relative
				// inbound URL rather than call pr.SetURL(nil) below (a
				// nil-pointer panic, sel.rs.upstreamURL being nil in the
				// zero value). RoundTrip then errors on the schemeless URL
				// and the ErrorHandler, finding no selectedRoute in context
				// either, returns a bare 502 without logging anything of its
				// own, instead of crashing the handler -- which makes the
				// line below this path's only cause line.
				log.Printf("registryproxy: Rewrite ran without a selected route in context")
				return
			}
			bareUpstream := sel.path == "" && sel.rawPath == ""
			if !bareUpstream {
				// Mutated before SetURL so its own path join (below) joins
				// the upstream URL with this stripped remainder rather than
				// the untouched, still-prefixed inbound path.
				pr.Out.URL.Path = sel.path
				pr.Out.URL.RawPath = sel.rawPath
			}
			pr.SetURL(sel.rs.upstreamURL)
			if bareUpstream {
				// A request naming exactly "/<prefix>" has no remainder to
				// join at all -- overwritten here rather than left to
				// SetURL's own join (singleJoiningSlash) because that would
				// always insert a separating "/", turning e.g. upstream
				// "https://host/artifactory" into ".../artifactory/" for a
				// request that named no trailing slash of its own.
				pr.Out.URL.Path = sel.rs.upstreamURL.Path
				pr.Out.URL.RawPath = sel.rs.upstreamURL.RawPath
			}
			pr.SetXForwarded()
			// ReverseProxy.ServeHTTP runs cleanQueryParams on the outbound
			// query before Rewrite is ever invoked, which silently rewrites
			// a semicolon-separated or malformed-escape query to "" (unlike
			// the legacy Director path). Recompute the outbound query from
			// the untouched inbound raw query and the selected route's own
			// raw query (captured once above, since SetURL resets
			// pr.Out.URL.RawQuery from the already-mangled value it
			// inherited), joining the two exactly like the legacy
			// NewSingleHostReverseProxy Director did -- upstream's query
			// first, then "&"-joined with the inbound query when both are
			// non-empty, so neither one clobbers the other.
			inboundQuery := pr.In.URL.RawQuery
			if sel.rs.upstreamQuery == "" || inboundQuery == "" {
				pr.Out.URL.RawQuery = sel.rs.upstreamQuery + inboundQuery
			} else {
				pr.Out.URL.RawQuery = sel.rs.upstreamQuery + "&" + inboundQuery
			}
			// Deleted unconditionally, before the credential attach below and
			// regardless of AuthScheme -- the inbound client's own
			// Authorization must never reach upstream on any leg, whether
			// this route is about to overwrite it with its own credential or
			// is an unauthenticated pass-through with nothing to put in its
			// place (issue #3256 AC 3, ADR 0047).
			pr.Out.Header.Del("Authorization")
			if sel.rs.headerValue != "" {
				pr.Out.Header.Set(sel.rs.headerName, sel.rs.headerValue)
			}
			// http.Transport only auto-decompresses a gzip response when
			// *it* added "Accept-Encoding: gzip" itself; a client-supplied
			// value (cargo sends one) is forwarded verbatim and the gzip
			// bytes arrive undecoded, failing modifyResponse's json.Decode
			// silently (issue #3175's blocking review finding). Force
			// "identity" only for a shape this proxy actually rewrites --
			// every other shape keeps the client's own Accept-Encoding, so
			// its response is still relayed byte-identical.
			if findResponseRewriteRow(pr.In.Method, sel.path) != nil {
				pr.Out.Header.Set("Accept-Encoding", "identity")
			}
		},
	}

	h := &allowlistLogHandler{
		states:        states,
		missStates:    make(map[string]*routeMissState, len(states)),
		failureStates: make(map[string]*routeFailureState, len(states)),
	}
	rp.ModifyResponse = func(resp *http.Response) error {
		h.logUpstreamStatus(resp)
		return modifyResponse(resp)
	}
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		h.logUpstreamTransportError(r, err)
		// Matches httputil.ReverseProxy's own defaultErrorHandler exactly
		// (net/http/httputil/reverseproxy.go), so replacing it here to add
		// route-aware logging leaves the response the client sees
		// byte-identical to before this hook existed.
		w.WriteHeader(http.StatusBadGateway)
	}
	h.rp = rp
	return h, nil
}

// maxRewriteBodyBytes caps how much of a response body ModifyResponse ever
// buffers in memory to run a responseRewriteTable row against. A cargo
// config.json is a few hundred bytes; a body over this cap is relayed
// untouched (see the "over cap" branch of modifyResponse) rather than
// rewritten or truncated, guarding against an upstream -- misconfigured or
// hostile -- serving something enormous under a shape that happens to match
// a row's method+path.
const maxRewriteBodyBytes = 1 << 20 // 1 MiB

// modifyResponse is the ReverseProxy ModifyResponse hook: it looks up the
// selectedRoute the Rewrite hook already stashed into the request context,
// finds the responseRewriteTable row (if any) matching this response's
// method and route-relative path, and rewrites the body when one matches. A
// HEAD never matches a row (every row names GET), so a HEAD response is
// passed through untouched by the same table lookup that gates a
// non-matching GET -- there is no separate HEAD special case to forget
// (issue #2854's HEAD-crash defect).
func modifyResponse(resp *http.Response) error {
	sel, ok := resp.Request.Context().Value(selectedRouteContextKey{}).(selectedRoute)
	if !ok || sel.forwarder == nil {
		return nil
	}
	// Only a successful response is a real config.json document; a 404 or
	// 500 body for the same path is an error page, not cargo config.
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	row := findResponseRewriteRow(resp.Request.Method, sel.path)
	if row == nil {
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRewriteBodyBytes+1))
	if err != nil {
		resp.Body.Close()
		// Returned, not swallowed: ReverseProxy's own ErrorHandler turns
		// this into a 502 rather than the handler crashing or hanging.
		return fmt.Errorf("registryproxy: read response body for rewrite: %w", err)
	}
	if len(body) > maxRewriteBodyBytes {
		// Over cap: relay untouched. Splice the bytes already read back in
		// front of whatever's left unread on resp.Body instead of buffering
		// the rest too -- the whole point of the cap is to never fully
		// materialize an oversized body in memory.
		resp.Body = &bodyWithClose{Reader: io.MultiReader(bytes.NewReader(body), resp.Body), closer: resp.Body}
		return nil
	}
	resp.Body.Close()

	result := row.rewrite(body, rewriteContext{
		matchHost:   sel.rs.matchHost,
		forwarder:   sel.forwarder,
		prefix:      sel.rs.prefix,
		upstreamURL: sel.rs.upstreamURL,
	})
	// Tested for the one outcome that rewrites rather than against the ones
	// that don't, so a rewriter that grows another skip outcome later relays
	// untouched by default instead of silently taking the rewrite path.
	if result.outcome != rewriteApplied {
		// Both skip outcomes below are deliberate, so each is worth its own
		// log line -- but only the dl value itself (a registry URL, never
		// the credential or the rest of the body) is named.
		switch result.outcome {
		case rewriteSkippedForeignHost:
			log.Printf("registryproxy: %s: dl %q names a host other than the route's match-host, left unchanged", row.name, result.from)
		case rewriteSkippedOutsideBasePath:
			log.Printf("registryproxy: %s: dl %q is not under the route's own upstream base path, left unchanged", row.name, result.from)
		default:
			// rewriteNone here means the request shape matched a row but the
			// body had nothing recognizable to rewrite -- e.g. not JSON, no
			// dl field, or (issue #3175's blocking review finding) still
			// compressed bytes the Rewrite hook's Accept-Encoding override
			// somehow didn't prevent. Previously silent, which made a
			// no-op rewrite against a real registry undiagnosable. Names
			// the row only -- never the body or the credential.
			log.Printf("registryproxy: %s: matched but body held no rewritable dl field, left unchanged", row.name)
		}
		// Byte-identical restore: every header, including Content-Length,
		// is left exactly as the upstream sent it.
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return nil
	}
	// from/to are the registry's dl and the rewritten Forwarder dl -- URLs,
	// never the credential (already stripped off the request/response by
	// the time this hook runs) or the rest of the body.
	log.Printf("registryproxy: %s: rewrote dl %s -> %s", row.name, result.from, result.to)
	resp.Body = io.NopCloser(bytes.NewReader(result.body))
	resp.ContentLength = int64(len(result.body))
	if resp.Header.Get("Content-Length") != "" {
		resp.Header.Set("Content-Length", strconv.Itoa(len(result.body)))
	}
	return nil
}

// bodyWithClose pairs a Reader (typically an io.MultiReader splicing bytes
// already buffered by modifyResponse's cap check back onto the unread
// remainder of the real response body) with that real body's Close, so the
// over-cap relay path in modifyResponse still closes the real upstream
// connection once ReverseProxy is done relaying it.
type bodyWithClose struct {
	io.Reader
	closer io.Closer
}

func (b *bodyWithClose) Close() error { return b.closer.Close() }

// selectedRouteContextKey is the context key allowlistLogHandler.ServeHTTP
// stashes a selectedRoute under, for the ReverseProxy Rewrite hook to read
// back -- an unexported empty-struct type so no other package can collide
// with (or forge) this key.
type selectedRouteContextKey struct{}

// allowlistLogHandler wraps the reverse proxy with allowlist-miss logging
// that tracks state across requests (issue #3087): a deployment whose paths
// never land inside the derived allowlist (the path-prefixed,
// Artifactory-style shape) would otherwise log every single request, so
// only the first miss logs in full and later misses accumulate into a set
// of distinct paths until (or unless) some request finally matches the
// allowlist, proving the deployment is root-served after all. This state
// is per route (issue #3176): each route is served by a distinct upstream
// that may or may not be root-served, so one route matching the allowlist
// must not flush or un-suppress another route's still-suppressing misses.
type allowlistLogHandler struct {
	rp     *httputil.ReverseProxy
	states []routeState // the route table Rewrite selects from, keyed by path prefix

	mu            sync.Mutex
	missStates    map[string]*routeMissState    // route Prefix -> that route's miss state; allocated lazily on first request
	failureStates map[string]*routeFailureState // route Prefix -> that route's upstream-failure state; allocated lazily on first request
}

// routeMissState is one route's allowlist-miss suppression state -- see
// allowlistLogHandler.
type routeMissState struct {
	everMatched     bool // true once any request on this route has matched the allowlist
	firstMissLogged bool // true once this route's first out-of-allowlist miss logged
	// The path from that first, fully-logged miss -- a later repeat of it
	// must not also land in suppressedPaths, or the summary counts a path
	// already reported in full as a second "further distinct path" (issue
	// #3176 review finding).
	firstMissPath string
	// A set, not a count, so a build looping over a handful of
	// un-allowlisted paths summarises as those paths rather than once per
	// request (issue #3176).
	suppressedPaths map[string]struct{} // suppressed while everMatched is still false
}

// routeFailureState is one route's upstream-failure suppression state,
// covering both an error status and a transport failure -- see
// allowlistLogHandler. Unlike routeMissState, this has no everMatched gate:
// a route that alternates 200s and 4xx/5xx (an npm client probing several
// package names, most missing) would otherwise re-log in full on every
// failure-after-success, which is exactly the flood suppression exists to
// prevent.
type routeFailureState struct {
	firstFailureLogged bool
	// The key of that first, fully-logged failure -- a later repeat of it
	// must not also land in suppressedFailures, mirroring firstMissPath's
	// double-count guard (issue #3176 review finding). Only meaningful once
	// firstFailureLogged is set: the zero failureKey is a value no real
	// failure produces, but firstFailureLogged, not that emptiness, is what
	// distinguishes "no first failure yet" from one already recorded.
	firstFailureKey    failureKey
	suppressedFailures map[failureKey]struct{}
}

// failureKey identifies one distinct upstream failure for the dedup in
// noteFailureLocked. status is the upstream's error status, or 0 for a
// transport failure that never got a status at all -- encoding the
// transport-vs-status distinction in the type rather than in a magic word
// inside a formatted string.
type failureKey struct {
	method string
	path   string
	status int
}

func (h *allowlistLogHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "registry proxy is read-only: publishing is out of scope for the Agent", http.StatusMethodNotAllowed)
		return
	}

	// Selected here, before h.rp (ReverseProxy) is ever invoked, so a path
	// naming no configured route's prefix is refused without dialing any
	// upstream at all -- ReverseProxy's own Rewrite hook only runs once it
	// has already committed to forwarding the request.
	sel, ok := selectRoute(h.states, r.URL.EscapedPath())
	if !ok {
		http.Error(w, "registry proxy: no route for this path", http.StatusNotFound)
		return
	}

	// The allowlist's patterns are all anchored at "^/...", so the
	// no-remainder case (a request naming exactly "/<prefix>") maps to "/"
	// here -- the route's own root -- rather than the empty string no
	// pattern could ever match anyway.
	strippedPath := sel.path
	if strippedPath == "" {
		strippedPath = "/"
	}

	// A host-rooted route enforces EnforcedPaths unconditionally, in place of
	// the legacy advisory/enforce-allowlist policy below (issue #3256): it
	// has no base path of its own to bound what it might otherwise forward,
	// so there is no log-only posture for it to opt out of. The legacy
	// block is skipped entirely for such a route, not merely short-circuited
	// by it, since allowlist.go's policy stays the legacy base-path route's.
	if sel.rs.hostRooted {
		if !pathSetAdmits(sel.rs.enforcedPaths, strippedPath) {
			http.Error(w, fmt.Sprintf(
				"registry proxy: host-rooted enforcement refused %s %s: not in the derived path-set (%s)",
				r.Method, strippedPath, strings.Join(sel.rs.enforcedPaths, ", "),
			), http.StatusForbidden)
			return
		}
	} else {
		// Log-only by default (ADR 0044, issue #2852): the derived allowlist
		// covers each bound ecosystem's protocol-fixed path shapes, but not
		// every ecosystem's download/artifact path is statically derivable --
		// cargo's, for one, is registry-specific (named by each registry's own
		// config.json "dl" field) rather than a fixed shape. That's why the
		// default stays advisory; a route can opt into promoting a miss to a
		// 403 reject via enforce-allowlist below (issue #3177).
		//
		// Checked against strippedPath, not the raw inbound path: the
		// allowlist's patterns describe each ecosystem's protocol shape
		// relative to a registry's own root, which is what the path looks like
		// only after the route-selecting prefix segment has been stripped off
		// (issue #3142).
		h.mu.Lock()
		prefix := sel.rs.prefix
		ms := h.missStates[prefix]
		if ms == nil {
			ms = &routeMissState{}
			h.missStates[prefix] = ms
		}
		allowed := isAllowedPath(strippedPath)
		if allowed {
			ms.everMatched = true
			h.logSuppressedMissesLocked(prefix, ms)
		} else if ms.everMatched || !ms.firstMissLogged {
			ms.firstMissLogged = true
			ms.firstMissPath = strippedPath
			// sel.rs.enforceAllowlist is decided below, but the wording here
			// must match the outcome: an enforcing route answers this miss with
			// a 403, so the line must say refused rather than imply relay.
			refusedSuffix := ""
			if sel.rs.enforceAllowlist {
				refusedSuffix = " (refused: enforce-allowlist)"
			}
			log.Printf("registryproxy: %s: path outside derived allowlist: %s %s%s", prefix, r.Method, strippedPath, refusedSuffix)
		} else if strippedPath != ms.firstMissPath {
			if ms.suppressedPaths == nil {
				ms.suppressedPaths = make(map[string]struct{})
			}
			ms.suppressedPaths[strippedPath] = struct{}{}
		}
		h.mu.Unlock()

		// Opt-in per route (issue #3177): a route that hasn't set
		// EnforceAllowlist keeps the log-only posture above unchanged. The body
		// names both the knob and the pattern set it was checked against, so it
		// reads as a route's declared policy to an Agent, not as a registry
		// that's merely broken.
		if sel.rs.enforceAllowlist && !allowed {
			http.Error(w, fmt.Sprintf(
				"registry proxy: enforce-allowlist policy refused %s %s: not in the derived allowlist (%s)",
				r.Method, strippedPath, allowlistedEcosystemNames,
			), http.StatusForbidden)
			return
		}
	}

	// The Forwarder address is the inbound request's own scheme+host -- the
	// address the client actually used to reach this proxy -- never
	// anything derived from the route (route.Upstream names the real
	// registry, not this proxy). Left nil when r.Host is empty (an
	// HTTP/1.0 client sent no Host header): modifyResponse then skips
	// rewriting rather than guess an address.
	if r.Host != "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		sel.forwarder = &url.URL{Scheme: scheme, Host: r.Host}
	}

	ctx := context.WithValue(r.Context(), selectedRouteContextKey{}, sel)
	h.rp.ServeHTTP(w, r.WithContext(ctx))
}

// logSuppressedMissesLocked flushes ms's accumulated set of suppressed-miss
// paths, naming prefix (ms's route) in the log line. h.mu must be held. The
// flush is best-effort at proxy teardown (Proxy.Close, deferred in
// cmd/launcher/internal/dispatch/box.go) -- a SIGTERM/SIGKILL of the launcher
// process before that defer runs loses whatever paths hadn't yet flushed.
func (h *allowlistLogHandler) logSuppressedMissesLocked(prefix string, ms *routeMissState) {
	if n := len(ms.suppressedPaths); n > 0 {
		ms.suppressedPaths = nil
		noun := "paths"
		if n == 1 {
			noun = "path"
		}
		log.Printf("registryproxy: %s: suppressed %d further distinct %s outside derived allowlist", prefix, n, noun)
	}
}

// logUpstreamStatus is the ModifyResponse-adjacent hook (called by New's
// closure before it delegates to modifyResponse) that logs a >=400 upstream
// response, with the same first-log-then-suppress dedup as allowlist
// misses. It never mutates resp -- observation only.
func (h *allowlistLogHandler) logUpstreamStatus(resp *http.Response) {
	if resp.StatusCode < 400 {
		return
	}
	prefix, method, path, ok := failureLogFields(resp.Request)
	if !ok {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.noteFailureLocked(prefix, failureKey{method: method, path: path, status: resp.StatusCode}, func() {
		log.Printf("registryproxy: %s: upstream error status: %s %s %d", prefix, method, path, resp.StatusCode)
	})
}

// logUpstreamTransportError is the ReverseProxy ErrorHandler hook: it logs a
// request that never got an HTTP response at all -- connection refused, TLS
// failure, DNS failure, or (via modifyResponse's own error return) a read
// failure on a response already received -- distinguishably from
// logUpstreamStatus's line, since no status code applies. r is
// ReverseProxy's outbound (proxy->upstream) request: it is a Clone of the
// inbound request ReverseProxy made internally before dialing, carrying the
// same context, so the selectedRoute ServeHTTP stashed there is still
// readable here. err comes from http.Transport.RoundTrip (or
// modifyResponse), neither of which echoes request headers back into an
// error, so the credential never appears here even via %v.
func (h *allowlistLogHandler) logUpstreamTransportError(r *http.Request, err error) {
	// This proxy sets no per-request deadline, so the only context that can
	// cancel this request is the inbound client's own: context.Canceled here
	// means the Box client hung up (routine under ecosystem-client
	// parallelism and timeouts), not that anything upstream failed. Dropping
	// it before noteFailureLocked keeps the route's single full-detail
	// failure slot free for a genuine failure -- the 401 the client abort
	// would otherwise demote to an anonymous suppressed count -- and avoids
	// mislabelling a client disconnect as a failure reaching upstream.
	if errors.Is(err, context.Canceled) {
		return
	}
	prefix, method, path, ok := failureLogFields(r)
	if !ok {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.noteFailureLocked(prefix, failureKey{method: method, path: path}, func() {
		log.Printf("registryproxy: %s: upstream request failed: %s %s: %v", prefix, method, path, err)
	})
}

// failureLogFields reads the route prefix, method, and route-relative path
// that both upstream-failure hooks name in their log line and key their
// dedup by, out of the selectedRoute ServeHTTP stashed in r's context. ok is
// false when no selectedRoute is there -- the caller's signal to log
// nothing, having no route to attribute the failure to.
func failureLogFields(r *http.Request) (prefix, method, path string, ok bool) {
	sel, ok := r.Context().Value(selectedRouteContextKey{}).(selectedRoute)
	if !ok {
		return "", "", "", false
	}
	path = sel.path
	// The no-remainder case (a request naming exactly "/<prefix>") maps to
	// the route's own root, matching ServeHTTP's own strippedPath.
	if path == "" {
		path = "/"
	}
	return sel.rs.prefix, r.Method, path, true
}

// noteFailureLocked is the first-log-then-suppress dedup shared by
// logUpstreamStatus and logUpstreamTransportError: prefix's
// routeFailureState logs the first distinct key in full via logLine, a later
// distinct key accumulates into the suppressed set, and a repeat of the
// first key is dropped. h.mu must be held.
func (h *allowlistLogHandler) noteFailureLocked(prefix string, key failureKey, logLine func()) {
	fs := h.failureStates[prefix]
	if fs == nil {
		fs = &routeFailureState{}
		h.failureStates[prefix] = fs
	}
	if !fs.firstFailureLogged {
		fs.firstFailureLogged = true
		fs.firstFailureKey = key
		logLine()
	} else if key != fs.firstFailureKey {
		if fs.suppressedFailures == nil {
			fs.suppressedFailures = make(map[failureKey]struct{})
		}
		fs.suppressedFailures[key] = struct{}{}
	}
}

// logSuppressedFailuresLocked flushes fs's accumulated set of
// suppressed-failure keys, naming prefix (fs's route) in the log line. h.mu
// must be held. See logSuppressedMissesLocked for the flush's best-effort
// teardown timing.
func (h *allowlistLogHandler) logSuppressedFailuresLocked(prefix string, fs *routeFailureState) {
	if n := len(fs.suppressedFailures); n > 0 {
		fs.suppressedFailures = nil
		noun := "failures"
		if n == 1 {
			noun = "failure"
		}
		log.Printf("registryproxy: %s: suppressed %d further distinct upstream %s", prefix, n, noun)
	}
}

// Close lets Proxy.Close flush every route's accumulated sets of
// suppressed-miss paths (when that route's allowlist never matched a single
// request this run) and suppressed upstream failures.
// Iterates h.states (the ordered route table), not the state maps directly,
// so a multi-route teardown emits its summaries in a stable, route-table
// order rather than Go's randomized map iteration order.
func (h *allowlistLogHandler) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, rs := range h.states {
		if ms := h.missStates[rs.prefix]; ms != nil {
			h.logSuppressedMissesLocked(rs.prefix, ms)
		}
		if fs := h.failureStates[rs.prefix]; fs != nil {
			h.logSuppressedFailuresLocked(rs.prefix, fs)
		}
	}
}

// closer is implemented by a Handler that needs to flush state when the
// proxy is torn down (currently only *allowlistLogHandler, via its set of
// suppressed-miss paths).
type closer interface {
	Close()
}

// Proxy serves an http.Handler over a unix domain socket or, via
// ListenAndServeTCP, a secret-gated loopback TCP port.
type Proxy struct {
	// Handler is the http.Handler to serve, typically built with New.
	Handler http.Handler

	listener net.Listener
}

// sunPathCap returns the sockaddr_un.sun_path fixed-size byte array's
// capacity for goos (issue #3077). Linux is the only platform besides Darwin
// spindrift targets, so it's the default case rather than a "linux" match --
// this also makes the function testable for both branches without depending
// on which OS the test binary actually runs on.
func sunPathCap(goos string) int {
	if goos == "darwin" {
		return 104
	}
	return 108
}

// TooLongForUnixSocket reports whether path is too long to bind as a unix
// domain socket on this OS: sockaddr_un.sun_path is a fixed-size byte array
// capped at 104 bytes on Darwin and 108 bytes on Linux (issue #3077), and the
// kernel needs the last byte for its own NUL terminator, so a path must be
// strictly shorter than that cap.
func TooLongForUnixSocket(path string) bool {
	return len(path) >= sunPathCap(runtime.GOOS)
}

// ListenAndServe removes any stale file at socketPath, listens on a unix
// domain socket there, and serves Handler on it in the background. It
// returns once the listener is established; serving happens in a separate
// goroutine.
func (p *Proxy) ListenAndServe(socketPath string) error {
	// Checked before touching the filesystem at all: net.Listen would fail
	// on a too-long path anyway, but only with a bare EINVAL "invalid
	// argument" that names neither the platform cap nor the actual path
	// length (issue #3077).
	if sunPathLimit := sunPathCap(runtime.GOOS); len(socketPath) >= sunPathLimit {
		return fmt.Errorf("registryproxy: socket path is %d bytes, at or over the %d-byte AF_UNIX sun_path limit on %s: %s", len(socketPath), sunPathLimit, runtime.GOOS, socketPath)
	}

	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("registryproxy: remove stale socket %q: %w", socketPath, err)
	}

	l, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("registryproxy: listen on %q: %w", socketPath, err)
	}
	p.listener = l

	go func() {
		_ = http.Serve(l, p.Handler)
	}()

	return nil
}

// ListenAndServeTCP listens on addr (e.g. "127.0.0.1:0" for an ephemeral
// port) and serves Handler on it in the background, gated by secret: unlike
// ListenAndServe's unix socket, a loopback TCP port has no filesystem
// permissions of its own to restrict who can connect, so every request must
// present secret via registrymanifest.TCPSecretHeader before it reaches
// Handler at all -- the check runs in front of Handler's own GET/HEAD gate
// and credential-attaching Rewrite hook, so a request missing or bearing the
// wrong secret never causes upstream to be dialed and never risks the real
// upstream credential touching anything. It returns once the listener is
// established; serving happens in a separate goroutine. Call Addr after a
// successful call to learn the bound address, which matters when addr names
// an ephemeral port.
//
// secret must be non-empty: an empty secret would make the gate below pass
// every request that omits registrymanifest.TCPSecretHeader entirely (an
// absent header reads back as "", which would then equal an empty secret),
// so ListenAndServeTCP fails closed and refuses to listen at all rather
// than fall open.
func (p *Proxy) ListenAndServeTCP(addr, secret string) error {
	if secret == "" {
		return errors.New("registryproxy: refusing to listen on TCP with an empty secret")
	}

	l, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("registryproxy: listen on %q: %w", addr, err)
	}
	p.listener = l

	gated := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// subtle.ConstantTimeCompare, not ==: this header check is the sole
		// gate on a loopback TCP port reachable by any local process (see
		// package doc), so a byte-at-a-time timing side channel from a
		// short-circuiting == would let a local attacker recover secret
		// byte-by-byte. ConstantTimeCompare returns 0 immediately when
		// lengths differ, but that only leaks len(secret), not any of its
		// bytes, so it's not a comparable oracle.
		if subtle.ConstantTimeCompare([]byte(r.Header.Get(registrymanifest.TCPSecretHeader)), []byte(secret)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		p.Handler.ServeHTTP(w, r)
	})

	go func() {
		_ = http.Serve(l, gated)
	}()

	return nil
}

// Addr returns the address the proxy's listener is bound to, established by
// whichever of ListenAndServe or ListenAndServeTCP was called. It returns nil
// if neither has been called yet.
func (p *Proxy) Addr() net.Addr {
	if p.listener == nil {
		return nil
	}
	return p.listener.Addr()
}

// Close stops the proxy from accepting further connections.
func (p *Proxy) Close() error {
	if c, ok := p.Handler.(closer); ok {
		c.Close()
	}
	if p.listener == nil {
		return nil
	}
	return p.listener.Close()
}
