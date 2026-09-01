// Package registryproxy implements a GET/HEAD-only pass-through reverse
// proxy served over a unix domain socket, optionally attaching a launcher-
// resolved credential to the outbound (proxy->registry) leg (ADR 0044).
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

// New builds an http.Handler that forwards GET and HEAD requests to
// upstream, preserving path and query string, and rejects every other
// method with 405 Method Not Allowed without forwarding it upstream.
//
// When credential is non-empty, every request forwarded to upstream carries
// it in an Authorization header (ADR 0044), rendered by
// authorizationHeaderValue: verbatim when the credential already names its
// own auth scheme (issue #3124), otherwise as "Bearer <credential>". When
// credential is empty, the proxy is an unauthenticated pass-through,
// unchanged from before. The rewrite also always sets the outbound Host
// header to upstream's host, regardless of what Host the inbound client request
// carried -- otherwise a client-controlled Host header would ride along
// with the credential to whatever vhost the client named. The credential is
// attached via ReverseProxy's Rewrite hook rather than its legacy Director,
// because Director runs before ReverseProxy strips hop-by-hop headers from
// the outbound request -- a client naming "Authorization" in its own
// Connection header would otherwise get the just-set Authorization header
// stripped right back out. Rewrite runs after that stripping, so what it
// sets survives untouched.
//
// The returned handler also accumulates allowlist-miss logging state across
// requests (issue #3087); a caller must eventually call Close() on it
// (directly, or via Proxy.Close() when the handler is wrapped in a Proxy) to
// flush the final suppressed-miss summary, or that count is silently
// dropped.
func New(upstream, credential string) (http.Handler, error) {
	u, err := url.Parse(upstream)
	if err != nil {
		return nil, fmt.Errorf("registryproxy: parse upstream URL %q: %w", upstream, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("registryproxy: upstream URL %q must be absolute", upstream)
	}

	upstreamQuery := u.RawQuery

	// Rendered once here rather than per request: credential is fixed for
	// the proxy's lifetime. Empty stays empty, which is the documented
	// unauthenticated pass-through.
	var authValue string
	if credential != "" {
		authValue = authorizationHeaderValue(credential)
	}

	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(u)
			pr.SetXForwarded()
			// ReverseProxy.ServeHTTP runs cleanQueryParams on the outbound
			// query before Rewrite is ever invoked, which silently rewrites
			// a semicolon-separated or malformed-escape query to "" (unlike
			// the legacy Director path). Recompute the outbound query from
			// the untouched inbound raw query and upstream's own raw query
			// (captured once above, since SetURL resets pr.Out.URL.RawQuery
			// from the already-mangled value it inherited), joining the two
			// exactly like the legacy NewSingleHostReverseProxy Director did
			// -- upstream's query first, then "&"-joined with the inbound
			// query when both are non-empty, so neither one clobbers the
			// other.
			inboundQuery := pr.In.URL.RawQuery
			if upstreamQuery == "" || inboundQuery == "" {
				pr.Out.URL.RawQuery = upstreamQuery + inboundQuery
			} else {
				pr.Out.URL.RawQuery = upstreamQuery + "&" + inboundQuery
			}
			if authValue != "" {
				pr.Out.Header.Set("Authorization", authValue)
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
