// Package registryproxy is a GET/HEAD-only reverse proxy over a unix domain
// socket, forwarding to one of a table of upstream routes selected by the
// first path segment and optionally attaching a launcher-resolved credential
// (ADR 0044, ADR 0045, issue #3142). httputil.ReverseProxy is single-hop, so
// a 3xx is relayed rather than followed: no credential crosses a redirect.
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
	"spindrift.dev/launcher/internal/registryvocab"
	"spindrift.dev/launcher/internal/unixsocket"
)

// Route is one resolved entry in the proxy's route table. Credential is
// already resolved to its final value, never a reference such as a file path
// or an env var name: the caller builds a Route from a TOML routes file (ADR
// 0045), and this package never resolves a credential or parses that file.
type Route struct {
	// MatchHost is the host[:port] this route was declared against (ADR
	// 0045). Routing never reads it; AssignPrefixes derives Prefix from it.
	MatchHost string
	// Upstream is the absolute origin (scheme://host[:port]) requests are
	// forwarded to. A route serves a whole host, so New rejects an Upstream
	// that carries a path.
	Upstream string
	// AuthScheme is "bearer" (the default when empty), "basic", or
	// "header:<Name>". See authHeader.
	AuthScheme string
	// Credential is the resolved value to attach; empty means this route is
	// an unauthenticated pass-through regardless of AuthScheme.
	Credential string
	// Prefix is the first URL path segment a Forwarder-facing request names
	// to select this route. AssignPrefixes derives it at route-synthesis
	// time, and it is never re-derived mid-run (ADR 0045).
	Prefix string
	// Ecosystems is the route's [routes.ecosystems.<name>] declaration block
	// (issue #3403), carried metadata for the manifest and for the launcher's
	// applyHostPathSet. This package never reads it: routing is by Prefix.
	Ecosystems registryvocab.RouteEcosystems
	// UpstreamOrigin is the operator-declared origin from the routes file
	// (ADR 0047, issue #3261). Carried metadata here: forwarding reads
	// Upstream, which the launcher's resolveHostRootedUpstreams fills in from
	// this origin, or from the Target repo's committed config when the route
	// declares none.
	UpstreamOrigin string
	// EnforcedPaths is checked against every request on this route,
	// unconditionally (ADR 0047, issue #3261). The caller supplies entries
	// already normalized (leading "/", no trailing "/", "/" meaning the whole
	// host); this package never re-derives or re-normalizes them. An empty
	// set refuses every request rather than falling back to a default.
	EnforcedPaths []string
	// Allow is carried metadata (issue #3258): routes-file path patterns the
	// launcher's applyHostPathSet already folded into EnforcedPaths before
	// this Route was built, so enforcement here reads EnforcedPaths alone.
	Allow []string
	// EnforcedSubtrees is EnforcedPaths tagged with the ecosystem that
	// declared each subtree (issue #3259). New groups them into
	// routeState.basesByEcosystem so a RewriteRow matches only its own
	// ecosystem's paths (issue #3400). Allow-derived paths never appear here,
	// so an operator's allow entry cannot widen what a rewrite row matches.
	EnforcedSubtrees []registryvocab.Subtree
}

// inlineAuthSchemes are the HTTP auth schemes a credential may name inline,
// each with its delimiting space (issue #3124). cargo sends a credentials.toml
// token verbatim as the Authorization value, so a registry bakes the scheme
// into the token (Artifactory emits `token = "Bearer <jwt>"`); prefixing a
// second "Bearer " produced "Bearer Bearer <jwt>" and a 401.
var inlineAuthSchemes = []string{"Bearer ", "Basic ", "token "}

// authorizationHeaderValue renders credential into an Authorization header
// value: verbatim when it already names one of inlineAuthSchemes, otherwise
// prefixed with "Bearer ". Only a genuine prefix followed by a non-empty
// remainder counts, and the scheme word matches case-insensitively, since RFC
// 7235 auth schemes are case-insensitive however a registry spells them.
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
	prefix        string // selects this route by the request's first path segment
	matchHost     string // a RewriteRow compares a rewritten edit's host against this, not against upstreamURL.Host
	upstreamURL   *url.URL
	upstreamQuery string
	headerName    string // "" when the route has no credential to attach
	headerValue   string
	enforcedPaths []string
	// basesByEcosystem is Route.EnforcedSubtrees regrouped by Ecosystem tag,
	// preserving each ecosystem's subtree order, so a RewriteRow matches a
	// request's path against only its own ecosystem's bases (see
	// findResponseRewriteRow).
	basesByEcosystem map[string][]string
}

// selectedRoute is the route and stripped remainder selectRoute computes once
// per request, which the Rewrite hook then joins onto the upstream URL.
type selectedRoute struct {
	rs      routeState
	path    string // "", together with rawPath == "", means "no remainder: forward the upstream URL verbatim"
	rawPath string
	// forwarder is the scheme+host the inbound request was addressed to, the
	// address a rewritten dl must name so a later crate download routes back
	// through this proxy. ServeHTTP sets it, since selectRoute never sees the
	// inbound *http.Request. nil when r.Host was empty (an HTTP/1.0 client
	// sent no Host header), and modifyResponse then skips rewriting.
	forwarder *url.URL
}

// selectRoute picks the routeState whose Prefix equals the first segment of
// escapedPath (an inbound r.URL.EscapedPath()) and returns the remainder after
// it. A path and rawPath both "" mean escapedPath was exactly "/<prefix>", so
// the caller forwards the upstream URL verbatim. ok is false when nothing
// matches, and the caller must refuse before any upstream is dialed.
func selectRoute(states []routeState, escapedPath string) (selectedRoute, bool) {
	if !strings.HasPrefix(escapedPath, "/") {
		return selectedRoute{}, false
	}
	// Split the escaped path, never *url.URL's decoded Path: net/http has
	// already turned a percent-escaped slash there (npm's %2f in a scoped
	// package name) into a literal '/', which would move where the prefix
	// segment ends. "%2F" stays three literal characters in the escaped form.
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
		// ParseRequestURI, not Parse: Parse reads a leading "//" as an
		// authority, so the remainder of "/<prefix>//evil.example/x" would
		// be misread as naming that host rather than the literal path it is.
		// A remainder it cannot parse is treated as no route matching.
		u, err := url.ParseRequestURI(remainder)
		if err != nil {
			return selectedRoute{}, false
		}
		return selectedRoute{rs: s, path: u.Path, rawPath: u.RawPath}, true
	}
	return selectedRoute{}, false
}

// authHeader renders scheme and credential into the header name and value the
// Rewrite hook sets on the outbound request: "" or "bearer" and "basic" both
// attach Authorization, and "header:<Name>" attaches credential verbatim to
// that header instead (the JFrog X-JFrog-Art-Api pattern). An empty credential
// attaches no header at all, whatever the scheme.
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

// basicHeaderValue renders credential as an HTTP Basic Authorization value:
// verbatim when it already names "Basic ", otherwise base64-encoded per RFC
// 7617, where credential is expected to be "user:password".
func basicHeaderValue(credential string) string {
	const prefix = "Basic "
	if len(credential) > len(prefix) && strings.EqualFold(credential[:len(prefix)], prefix) {
		return credential
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(credential))
}

// AssignPrefixes sets Prefix on each element of routes in place, mutating the
// caller's backing array even if it ignores the return value. A Prefix is
// registryvocab.HostKey(MatchHost) with every character outside [a-z0-9]
// mapped to '-', plus a "-N" suffix when that collides with an earlier route.
func AssignPrefixes(routes []Route) []Route {
	used := make(map[string]bool, len(routes))  // every Prefix assigned so far, generated "-N" ones included
	counts := make(map[string]int, len(routes)) // base slug to the suffix count tried so far
	for i := range routes {
		base := slugify(registryvocab.HostKey(routes[i].MatchHost))
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

// isValidPrefix reports whether prefix contains only [a-z0-9-], the character
// set required of the first URL path segment that selects a route.
func isValidPrefix(prefix string) bool {
	for i := 0; i < len(prefix); i++ {
		c := prefix[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// New builds an http.Handler that forwards GET and HEAD to one of routes and
// rejects every other method with 405. Each route's Prefix must be non-empty,
// unique, and only [a-z0-9-] (see AssignPrefixes). The caller must eventually
// Close() the returned handler, directly or via Proxy.Close, or its final
// suppressed-failure summaries are dropped (issue #3125).
func New(routes []Route, rewriteRows []registryvocab.RewriteRow) (http.Handler, error) {
	if len(routes) == 0 {
		return nil, errors.New("registryproxy: no routes configured")
	}

	states := make([]routeState, len(routes))
	seenPrefixes := make(map[string]bool, len(routes))
	for i, route := range routes {
		if route.Prefix == "" {
			return nil, fmt.Errorf("registryproxy: route %q has no Prefix", route.MatchHost)
		}
		// Shape before uniqueness: an invalid Prefix that also duplicates
		// another route's should report the more specific error.
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
		if u.Path != "" {
			// Checked here rather than left to the Rewrite hook's join to
			// no-op: a non-empty path, even "/", would silently prefix
			// every forwarded request instead of failing loudly.
			return nil, fmt.Errorf("registryproxy: route %q: Upstream %q must be a bare origin with no path", route.MatchHost, route.Upstream)
		}

		// Rendered once, not per request: a route's credential is fixed for
		// the proxy's lifetime.
		headerName, headerValue, err := authHeader(route.AuthScheme, route.Credential)
		if err != nil {
			return nil, fmt.Errorf("registryproxy: route %q: %w", route.MatchHost, err)
		}

		// Grouped by Ecosystem tag so a RewriteRow (findResponseRewriteRow)
		// matches only its own ecosystem's bases, never another's and never
		// an Allow-derived path, which never reaches EnforcedSubtrees.
		var basesByEcosystem map[string][]string
		if len(route.EnforcedSubtrees) > 0 {
			basesByEcosystem = make(map[string][]string, len(route.EnforcedSubtrees))
			for _, sub := range route.EnforcedSubtrees {
				basesByEcosystem[sub.Ecosystem] = append(basesByEcosystem[sub.Ecosystem], sub.Path)
			}
		}

		states[i] = routeState{
			prefix:           route.Prefix,
			matchHost:        route.MatchHost,
			upstreamURL:      u,
			upstreamQuery:    u.RawQuery,
			headerName:       headerName,
			headerValue:      headerValue,
			enforcedPaths:    route.EnforcedPaths,
			basesByEcosystem: basesByEcosystem,
		}
	}

	rp := &httputil.ReverseProxy{
		// Rewrite, not the legacy Director: Director runs before ReverseProxy
		// strips hop-by-hop headers, so a client naming "Authorization" in its
		// own Connection header would have the just-set credential stripped
		// right back out. Rewrite runs after that stripping.
		Rewrite: func(pr *httputil.ProxyRequest) {
			// routeLogHandler.ServeHTTP computed the route and stripped
			// remainder before calling in, so a 404 for an unmatched prefix
			// never reaches here and the enforcement check and this join
			// cannot drift apart over the same path.
			sel, ok := pr.In.Context().Value(selectedRouteContextKey{}).(selectedRoute)
			if !ok {
				// Unreachable: ServeHTTP always stashes a selectedRoute.
				// Rewrite has no ResponseWriter to answer with, so leave
				// pr.Out.URL untouched rather than call SetURL on the zero
				// value's nil upstreamURL and panic. RoundTrip then errors
				// on the schemeless URL and ErrorHandler returns a bare 502.
				log.Printf("registryproxy: Rewrite ran without a selected route in context")
				return
			}
			// Mutated before SetURL so its join uses this stripped remainder
			// rather than the still-prefixed inbound path. The origin
			// carries no path of its own (New rejects one that does), so the
			// join is the remainder verbatim.
			pr.Out.URL.Path = sel.path
			pr.Out.URL.RawPath = sel.rawPath
			// SetURL also points the outbound Host at the upstream, so a
			// client-controlled Host never rides along with the credential.
			pr.SetURL(sel.rs.upstreamURL)
			pr.SetXForwarded()
			// ReverseProxy.ServeHTTP runs cleanQueryParams before Rewrite,
			// silently rewriting a semicolon-separated or malformed-escape
			// query to "". Recompute from the untouched inbound raw query
			// and the route's own (SetURL reset RawQuery from the mangled
			// value), joining them as the legacy Director did.
			inboundQuery := pr.In.URL.RawQuery
			if sel.rs.upstreamQuery == "" || inboundQuery == "" {
				pr.Out.URL.RawQuery = sel.rs.upstreamQuery + inboundQuery
			} else {
				pr.Out.URL.RawQuery = sel.rs.upstreamQuery + "&" + inboundQuery
			}
			// Deleted unconditionally, whatever the scheme: the inbound
			// client's own Authorization must never reach upstream, an
			// unauthenticated pass-through included (issue #3256 AC 3, ADR
			// 0047).
			pr.Out.Header.Del("Authorization")
			if sel.rs.headerValue != "" {
				pr.Out.Header.Set(sel.rs.headerName, sel.rs.headerValue)
			}
			// http.Transport only auto-decompresses gzip it asked for itself;
			// a client-supplied Accept-Encoding (cargo sends one) leaves the
			// bytes encoded and modifyResponse's json.Decode fails silently
			// (issue #3175). Forced only for a shape this proxy rewrites, so
			// every other response is still relayed byte-identical.
			if row, _ := findResponseRewriteRow(pr.In.Method, sel.path, sel.rs, rewriteRows); row != nil {
				pr.Out.Header.Set("Accept-Encoding", "identity")
			}
		},
	}

	h := &routeLogHandler{
		states:        states,
		rewriteRows:   rewriteRows,
		failureStates: make(map[string]*routeFailureState, len(states)),
		learnedPaths:  make(map[string][]string, len(states)),
	}
	rp.ModifyResponse = func(resp *http.Response) error {
		h.logUpstreamStatus(resp)
		return h.modifyResponse(resp)
	}
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		h.logUpstreamTransportError(r, err)
		// Matches httputil.ReverseProxy's own defaultErrorHandler, so adding
		// route-aware logging leaves the client's response byte-identical.
		w.WriteHeader(http.StatusBadGateway)
	}
	h.rp = rp
	return h, nil
}

// maxRewriteBodyBytes caps how much of a response body modifyResponse buffers
// to run a registryvocab.RewriteRow against. A cargo config.json is a few
// hundred bytes; a body over the cap is relayed untouched rather than
// rewritten or truncated, so a hostile upstream cannot make this proxy hold
// something enormous in memory.
const maxRewriteBodyBytes = 1 << 20 // 1 MiB

// foreignHostSkipLogFormat is shared by the two sites that report a declined
// value so both read identically in the log; fakerewriterow_test.go greps for
// this exact text.
const foreignHostSkipLogFormat = "registryproxy: %s: %q names a host other than the route's match-host, left unchanged"

// modifyResponse rewrites a response body when a caller-supplied row matches
// the response's method and route-relative path. A matching row's Rewrite runs
// unconditionally, so every row must name a body-bearing method: a row naming
// HEAD would have its return spliced onto an empty HEAD body. None does, so a
// HEAD response relays by the unrewritable-GET path (issue #2854).
func (h *routeLogHandler) modifyResponse(resp *http.Response) error {
	sel, ok := resp.Request.Context().Value(selectedRouteContextKey{}).(selectedRoute)
	if !ok || sel.forwarder == nil {
		return nil
	}
	// Only a successful response is a real document; a 404 or 500 body for the
	// same path is an error page.
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	row, _ := findResponseRewriteRow(resp.Request.Method, sel.path, sel.rs, h.rewriteRows)
	if row == nil {
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRewriteBodyBytes+1))
	if err != nil {
		resp.Body.Close()
		// Returned, not swallowed: ReverseProxy's ErrorHandler turns this
		// into a 502 rather than the handler crashing or hanging.
		return fmt.Errorf("registryproxy: read response body for rewrite: %w", err)
	}
	if len(body) > maxRewriteBodyBytes {
		// Over cap: relay untouched, splicing the bytes already read in front
		// of the unread remainder instead of buffering the rest too. The cap
		// exists so an oversized body is never fully held in memory.
		resp.Body = &bodyWithClose{Reader: io.MultiReader(bytes.NewReader(body), resp.Body), closer: resp.Body}
		return nil
	}
	resp.Body.Close()

	result := row.Rewrite(body, registryvocab.RewriteContext{
		MatchHost: sel.rs.matchHost,
		Forwarder: sel.forwarder,
		Prefix:    sel.rs.prefix,
	})
	// Tested for the one outcome that rewrites, not against the ones that do
	// not, so a rewriter growing another skip outcome later relays untouched
	// instead of silently taking the rewrite path.
	if result.Outcome != registryvocab.RewriteApplied {
		// Keyed on the outcome first, so a deliberate skip is never reported
		// as a no-match just because the row named no value to blame.
		switch result.Outcome {
		case registryvocab.RewriteSkippedForeignHost:
			// Every declined value gets its own line, naming only the edited
			// value, never the credential or the rest of the body.
			if len(result.Edits) == 0 {
				log.Printf("registryproxy: %s: skipped without naming a value, left unchanged", row.Name)
			} else {
				for _, edit := range result.Edits {
					log.Printf(foreignHostSkipLogFormat, row.Name, edit.From)
				}
			}
		default:
			// The shape matched a row but the body held nothing rewritable:
			// not JSON, no matching field, or still-compressed bytes (issue
			// #3175). Names the row only, never the body or the credential.
			log.Printf("registryproxy: %s: matched but body held nothing rewritable, left unchanged", row.Name)
		}
		// Byte-identical restore: every header, Content-Length included, is
		// left exactly as the upstream sent it.
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return nil
	}
	// From and To are URLs, never the credential, which is off the request and
	// response by the time this hook runs.
	for _, edit := range result.Edits {
		// An empty To declines this one value (a packument tarball naming a
		// CDN, say) alongside others the row did rewrite. Never learned from,
		// or its unset LearnedPath would normalize to "/" and admit the whole
		// host (issue #3401).
		if edit.To == "" {
			log.Printf(foreignHostSkipLogFormat, row.Name, edit.From)
			continue
		}
		log.Printf("registryproxy: %s: rewrote %s -> %s", row.Name, edit.From, edit.To)
		h.learnRewriteBase(sel.rs.prefix, edit.LearnedPath)
	}
	resp.Body = io.NopCloser(bytes.NewReader(result.Body))
	resp.ContentLength = int64(len(result.Body))
	if resp.Header.Get("Content-Length") != "" {
		resp.Header.Set("Content-Length", strconv.Itoa(len(result.Body)))
	}
	return nil
}

// bodyWithClose pairs a Reader with the real response body's Close, so
// modifyResponse's over-cap relay path still closes the upstream connection
// once ReverseProxy has finished relaying it.
type bodyWithClose struct {
	io.Reader
	closer io.Closer
}

func (b *bodyWithClose) Close() error { return b.closer.Close() }

// selectedRouteContextKey keys the selectedRoute ServeHTTP stashes for the
// Rewrite hook. Unexported and empty so no other package can collide with it
// or forge one.
type selectedRouteContextKey struct{}

// routeLogHandler wraps the reverse proxy with the per-route enforcement check
// and with upstream-failure logging that tracks state across requests (issue
// #3087). That state is per route (issue #3176): one route's failures must not
// flush or un-suppress another route's still-suppressing ones.
type routeLogHandler struct {
	rp          *httputil.ReverseProxy
	states      []routeState
	rewriteRows []registryvocab.RewriteRow

	mu            sync.Mutex
	failureStates map[string]*routeFailureState // route Prefix to that route's upstream-failure state
	learnedPaths  map[string][]string           // route Prefix to subtrees learned from that route's rewrite edits (ADR 0047)
}

// routeFailureState is one route's upstream-failure suppression state, for an
// error status and a transport failure alike. There is no ever-succeeded gate
// on it: a route alternating 200s and 4xx/5xx (an npm client probing package
// names, most missing) would otherwise re-log in full on every failure after a
// success, which is the flood suppression exists to prevent.
type routeFailureState struct {
	firstFailureLogged bool
	// The first, fully-logged failure's key: a later repeat of it must not
	// also land in suppressedFailures (issue #3176 review finding). Only
	// meaningful once firstFailureLogged is set, and that flag, not the zero
	// key, is what distinguishes "no first failure yet" from one recorded.
	firstFailureKey    failureKey
	suppressedFailures map[failureKey]struct{}
}

// failureKey identifies one distinct upstream failure for noteFailureLocked's
// dedup. status is the upstream's error status, or 0 for a transport failure
// that never got one.
type failureKey struct {
	method string
	path   string
	status int
}

// learnRewriteBase records an edit's LearnedPath as a subtree learned for
// prefix's route (ADR 0047), deduping so a repeat fetch of the same shape
// never grows the set unbounded. The path is caller-supplied, so the empty
// spelling of registryvocab.RewriteEdit's "/" sentinel is normalized here
// rather than trusted.
func (h *routeLogHandler) learnRewriteBase(prefix, path string) {
	if path == "" {
		path = "/"
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, p := range h.learnedPaths[prefix] {
		if p == path {
			return
		}
	}
	h.learnedPaths[prefix] = append(h.learnedPaths[prefix], path)
}

// learnedAdmits reports whether path falls inside any subtree learned so far
// for prefix's route, by the same registryvocab.PathSet.Admits rule the
// route's static enforced set uses.
func (h *routeLogHandler) learnedAdmits(prefix, path string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return registryvocab.PathSet(h.learnedPaths[prefix]).Admits(path)
}

func (h *routeLogHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "registry proxy is read-only: publishing is out of scope for the Agent", http.StatusMethodNotAllowed)
		return
	}

	// Selected before h.rp runs, so a path naming no configured route is
	// refused without dialing any upstream; ReverseProxy's own Rewrite hook
	// only runs once it has committed to forwarding.
	sel, ok := selectRoute(h.states, r.URL.EscapedPath())
	if !ok {
		http.Error(w, "registry proxy: no route for this path", http.StatusNotFound)
		return
	}

	// A request naming exactly "/<prefix>" maps to the route's own root:
	// registryvocab.PathSet.Admits cannot judge the empty string as a path.
	strippedPath := sel.path
	if strippedPath == "" {
		strippedPath = "/"
	}

	// Checked against strippedPath, not the raw inbound path: the enforced set
	// describes subtrees relative to the upstream host's own root, which the
	// path only resembles once the route-selecting segment is stripped (issue
	// #3142). The static set is checked first, unlocked, since it is the cheap
	// common case; h.mu is taken for the learned set (ADR 0047) on a miss.
	if !registryvocab.PathSet(sel.rs.enforcedPaths).Admits(strippedPath) && !h.learnedAdmits(sel.rs.prefix, strippedPath) {
		http.Error(w, fmt.Sprintf(
			"registry proxy: enforcement refused %s %s: not in the derived path-set (%s)",
			r.Method, strippedPath, strings.Join(sel.rs.enforcedPaths, ", "),
		), http.StatusForbidden)
		return
	}

	// The Forwarder address is the address the client actually used to reach
	// this proxy, never anything derived from the route, whose Upstream names
	// the real registry. Left nil when r.Host is empty (an HTTP/1.0 client
	// sent no Host header), and modifyResponse then skips rewriting.
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

// logUpstreamStatus logs a >=400 upstream response, with a
// first-log-then-suppress dedup per route. It never mutates resp.
func (h *routeLogHandler) logUpstreamStatus(resp *http.Response) {
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

// logUpstreamTransportError is the ReverseProxy ErrorHandler hook: no response
// arrived, or modifyResponse failed reading one. r is the outbound clone,
// which carries the inbound request's context, so the selectedRoute ServeHTTP
// stashed is still readable. err comes from RoundTrip or modifyResponse,
// neither of which echoes request headers, so no credential appears via %v.
func (h *routeLogHandler) logUpstreamTransportError(r *http.Request, err error) {
	// This proxy sets no per-request deadline, so context.Canceled means the
	// Box client hung up, not that anything upstream failed. Dropping it keeps
	// the route's single full-detail failure slot free for a genuine failure,
	// which a client abort would otherwise demote to a suppressed count.
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

// failureLogFields reads the route prefix, method, and route-relative path out
// of the selectedRoute ServeHTTP stashed in r's context. ok is false when none
// is there, and the caller then logs nothing, having no route to blame.
func failureLogFields(r *http.Request) (prefix, method, path string, ok bool) {
	sel, ok := r.Context().Value(selectedRouteContextKey{}).(selectedRoute)
	if !ok {
		return "", "", "", false
	}
	path = sel.path
	// A request naming exactly "/<prefix>" maps to the route's own root,
	// matching ServeHTTP's strippedPath.
	if path == "" {
		path = "/"
	}
	return sel.rs.prefix, r.Method, path, true
}

// noteFailureLocked is the first-log-then-suppress dedup: the first distinct
// key for prefix's route is logged in full via logLine, a later distinct key
// accumulates into the suppressed set, and a repeat of the first is dropped.
// h.mu must be held.
func (h *routeLogHandler) noteFailureLocked(prefix string, key failureKey, logLine func()) {
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

// logSuppressedFailuresLocked flushes fs's suppressed-failure keys, naming
// prefix in the log line. h.mu must be held. The flush is best-effort at
// teardown: a SIGTERM or SIGKILL of the launcher before Proxy.Close's defer
// runs loses whatever had not flushed.
func (h *routeLogHandler) logSuppressedFailuresLocked(prefix string, fs *routeFailureState) {
	if n := len(fs.suppressedFailures); n > 0 {
		fs.suppressedFailures = nil
		noun := "failures"
		if n == 1 {
			noun = "failure"
		}
		log.Printf("registryproxy: %s: suppressed %d further distinct upstream %s", prefix, n, noun)
	}
}

// Close flushes every route's suppressed upstream failures. It iterates
// h.states, not the state map, so a multi-route teardown emits summaries in
// route-table order rather than Go's randomized map order.
func (h *routeLogHandler) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, rs := range h.states {
		if fs := h.failureStates[rs.prefix]; fs != nil {
			h.logSuppressedFailuresLocked(rs.prefix, fs)
		}
	}
}

// closer is implemented by a Handler that must flush state at proxy teardown.
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

// ListenAndServe removes any stale file at socketPath, listens on a unix
// domain socket there, and serves Handler in the background. It returns once
// the listener is established.
func (p *Proxy) ListenAndServe(socketPath string) error {
	// Checked before touching the filesystem: net.Listen would fail on a
	// too-long path anyway, but with a bare EINVAL naming neither the
	// platform cap nor the path length (issue #3077).
	if unixsocket.TooLong(socketPath) {
		return fmt.Errorf("registryproxy: socket path is %d bytes, at or over the %d-byte AF_UNIX sun_path limit on %s: %s", len(socketPath), unixsocket.Cap(), runtime.GOOS, socketPath)
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

// ListenAndServeTCP serves Handler on addr in the background, gated by secret:
// a loopback TCP port has no filesystem permissions of its own, so every
// request must present secret via registrymanifest.TCPSecretHeader before it
// reaches Handler at all. An empty secret would match an absent header, so it
// fails closed. Call Addr to learn an ephemeral port's bound address.
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
		// subtle.ConstantTimeCompare, not ==: this header is the sole gate on
		// a port any local process can reach, so a short-circuiting == would
		// leak secret a byte at a time. The early return on differing lengths
		// leaks only len(secret), which is not a comparable oracle.
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

// Addr returns the address the proxy's listener is bound to, or nil when
// neither ListenAndServe nor ListenAndServeTCP has been called.
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
