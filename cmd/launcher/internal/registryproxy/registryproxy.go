// Package registryproxy implements a GET/HEAD-only pass-through reverse
// proxy served over a unix domain socket, forwarding to one of a table of
// upstream routes and optionally attaching a launcher-resolved credential to
// the outbound (proxy->registry) leg (ADR 0044, ADR 0045).
//
// httputil.ReverseProxy is single-hop: it relays whatever the upstream
// responds with -- including a 3xx redirect's status and Location header --
// straight back to the client without ever following it itself. So a
// redirect response is returned to the client as-is, which then fetches the
// target directly and unauthenticated, satisfying ADR 0044's requirement
// that the credential never cross a redirect hop.
package registryproxy

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
)

// Route is one resolved entry in the proxy's route table: MatchHost picks it
// for an inbound request, Upstream is where matching requests are forwarded,
// and Credential (already launcher-resolved to its final value, never a
// reference such as a file path or env var name) is attached per AuthScheme.
// Building this from a TOML routes file or from the legacy scalar knobs is
// the caller's job (ADR 0045) -- this package never resolves a credential or
// parses a routes file itself.
type Route struct {
	// MatchHost is the host[:port] this route serves; compared against the
	// inbound request's Host header with the port stripped from both sides.
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
}

// inlineAuthSchemes are the HTTP auth schemes a credential may name inline,
// each with its delimiting space (issue #3124). cargo sends a
// credentials.toml token verbatim as the Authorization header value rather
// than prepending a scheme of its own, so a registry documenting a cargo
// setup has to bake the scheme into the token -- Artifactory's own "Set Me
// Up" emits `token = "Bearer <jwt>"`, and the cargo-credentials value of
// REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT reads exactly that file. A
// credential arriving already schemed is the whole header value; prefixing a
// second "Bearer " produced "Bearer Bearer <jwt>" and a 401.
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
	matchHost     string // Route.MatchHost, host-only and lowercased; "" never matches
	upstreamURL   *url.URL
	upstreamQuery string
	headerName    string // "" when the route has no credential to attach
	headerValue   string
}

// hostOnly lowercases hostport and strips any ":port" suffix, so a route's
// MatchHost and an inbound request's Host header compare on the hostname
// alone. A hostport with no port (net.SplitHostPort's "missing port" error)
// also has a single enclosing "[" "]" bracket pair stripped, if present,
// before lowercasing -- otherwise "[::1]" (no port) and "[::1]:443" would
// normalize to different strings even though an inbound "Host: [::1]:443"
// itself normalizes to the bracket-free "::1".
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

// selectRoute picks the routeState whose matchHost equals inboundHost's
// hostname, falling back to the first route when none match -- which is
// every request when there is exactly one route, the case for the
// scalar-knob bridge route, which never populates MatchHost to equal a real
// Host header (issue #3139).
func selectRoute(states []routeState, inboundHost string) routeState {
	host := hostOnly(inboundHost)
	for _, rs := range states {
		// rs.matchHost == "" is excluded even though hostOnly("") == "": a
		// route table always names its MatchHost (registryroutes validates
		// this upstream of here), so an empty match is never a real route,
		// only ever the zero value of a Route built without one.
		if rs.matchHost != "" && rs.matchHost == host {
			return rs
		}
	}
	return states[0]
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

// New builds an http.Handler that forwards GET and HEAD requests to one of
// routes, preserving path and query string, and rejects every other method
// with 405 Method Not Allowed without forwarding it upstream. routes must be
// non-empty.
//
// Each request is dispatched to the route whose MatchHost equals the
// inbound request's Host header (host-only, case-insensitive; see
// selectRoute), falling back to routes[0] when none match.
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
// The returned handler also accumulates allowlist-miss logging state across
// requests (issue #3087); a caller must eventually call Close() on it
// (directly, or via Proxy.Close() when the handler is wrapped in a Proxy) to
// flush the final suppressed-miss summary, or that count is silently
// dropped.
func New(routes []Route) (http.Handler, error) {
	if len(routes) == 0 {
		return nil, errors.New("registryproxy: no routes configured")
	}

	states := make([]routeState, len(routes))
	for i, route := range routes {
		u, err := url.Parse(route.Upstream)
		if err != nil {
			return nil, fmt.Errorf("registryproxy: parse upstream URL %q: %w", route.Upstream, err)
		}
		if u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("registryproxy: upstream URL %q must be absolute", route.Upstream)
		}

		// Rendered once here rather than per request: a route's credential
		// is fixed for the proxy's lifetime.
		headerName, headerValue, err := authHeader(route.AuthScheme, route.Credential)
		if err != nil {
			return nil, fmt.Errorf("registryproxy: route %q: %w", route.MatchHost, err)
		}

		states[i] = routeState{
			matchHost:     hostOnly(route.MatchHost),
			upstreamURL:   u,
			upstreamQuery: u.RawQuery,
			headerName:    headerName,
			headerValue:   headerValue,
		}
	}

	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			rs := selectRoute(states, pr.In.Host)
			pr.SetURL(rs.upstreamURL)
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
			if rs.upstreamQuery == "" || inboundQuery == "" {
				pr.Out.URL.RawQuery = rs.upstreamQuery + inboundQuery
			} else {
				pr.Out.URL.RawQuery = rs.upstreamQuery + "&" + inboundQuery
			}
			if rs.headerValue != "" {
				pr.Out.Header.Set(rs.headerName, rs.headerValue)
			}
		},
	}

	h := &allowlistLogHandler{rp: rp}
	return h, nil
}

// allowlistLogHandler wraps the reverse proxy with allowlist-miss logging
// that tracks state across requests (issue #3087): a deployment whose paths
// never land inside the derived allowlist (the path-prefixed,
// Artifactory-style shape) would otherwise log every single request, so
// only the first miss logs in full and later misses are counted until (or
// unless) some request finally matches the allowlist, proving the
// deployment is root-served after all.
type allowlistLogHandler struct {
	rp *httputil.ReverseProxy

	mu               sync.Mutex
	everMatched      bool // true once any request has matched the allowlist
	firstMissLogged  bool // true once the first out-of-allowlist miss logged
	suppressedMisses int  // misses suppressed while everMatched is still false
}

func (h *allowlistLogHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "registry proxy is read-only: publishing is out of scope for the Agent", http.StatusMethodNotAllowed)
		return
	}
	// Log-only, not enforced (ADR 0044, issue #2852): the derived
	// allowlist covers each bound ecosystem's protocol-fixed path
	// shapes, but not every ecosystem's download/artifact path is
	// statically derivable -- cargo's, for one, is registry-specific
	// (named by each registry's own config.json "dl" field) rather than
	// a fixed shape. Promoting this to a reject would first require
	// deriving or learning any such per-registry path too -- e.g.
	// fetching and parsing each configured registry's config.json at
	// startup, or observing enough real traffic to prove the derived
	// set complete.
	h.mu.Lock()
	if isAllowedPath(r.URL.Path) {
		h.everMatched = true
		h.logSuppressedMissesLocked()
	} else if h.everMatched || !h.firstMissLogged {
		h.firstMissLogged = true
		log.Printf("registryproxy: path outside derived allowlist: %s %s", r.Method, r.URL.Path)
	} else {
		h.suppressedMisses++
	}
	h.mu.Unlock()
	h.rp.ServeHTTP(w, r)
}

// logSuppressedMissesLocked flushes any accumulated suppressed-miss count.
// h.mu must be held. The flush is best-effort at proxy teardown
// (Proxy.Close, deferred in cmd/launcher/internal/dispatch/box.go) -- a
// SIGTERM/SIGKILL of the launcher process before that defer runs loses
// whatever count hadn't yet flushed.
func (h *allowlistLogHandler) logSuppressedMissesLocked() {
	if n := h.suppressedMisses; n > 0 {
		h.suppressedMisses = 0
		log.Printf("registryproxy: suppressed %d further requests outside derived allowlist", n)
	}
}

// Close lets Proxy.Close flush any accumulated suppressed-miss count when the
// deployment's allowlist never matched a single request this run.
func (h *allowlistLogHandler) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.logSuppressedMissesLocked()
}

// closer is implemented by a Handler that needs to flush state when the
// proxy is torn down (currently only *allowlistLogHandler, via its
// suppressed-miss count).
type closer interface {
	Close()
}

// TCPSecretHeader is the HTTP header a Box must carry the per-run TCP
// secret in on every request when the registry proxy is served over
// loopback TCP (issue #3111) -- a unix socket needs no equivalent since
// its own filesystem permissions already gate access.
const TCPSecretHeader = "X-Spindrift-Registry-Proxy-Secret"

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
// present secret via TCPSecretHeader before it reaches Handler at all -- the
// check runs in front of Handler's own GET/HEAD gate and credential-attaching
// Rewrite hook, so a request missing or bearing the wrong secret never
// causes upstream to be dialed and never risks the real upstream credential
// touching anything. It returns once the listener is established; serving
// happens in a separate goroutine. Call Addr after a successful call to
// learn the bound address, which matters when addr names an ephemeral port.
//
// secret must be non-empty: an empty secret would make the gate below pass
// every request that omits TCPSecretHeader entirely (an absent header reads
// back as "", which would then equal an empty secret), so ListenAndServeTCP
// fails closed and refuses to listen at all rather than fall open.
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
		if subtle.ConstantTimeCompare([]byte(r.Header.Get(TCPSecretHeader)), []byte(secret)) != 1 {
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
