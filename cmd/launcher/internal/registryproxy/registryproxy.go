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
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
)

// New builds an http.Handler that forwards GET and HEAD requests to
// upstream, preserving path and query string, and rejects every other
// method with 405 Method Not Allowed without forwarding it upstream.
//
// When credential is non-empty, every request forwarded to upstream carries
// it as "Authorization: Bearer <credential>" (ADR 0044). When credential is
// empty, the proxy is an unauthenticated pass-through, unchanged from
// before. The rewrite also always sets the outbound Host header to
// upstream's host, regardless of what Host the inbound client request
// carried -- otherwise a client-controlled Host header would ride along
// with the credential to whatever vhost the client named. The credential is
// attached via ReverseProxy's Rewrite hook rather than its legacy Director,
// because Director runs before ReverseProxy strips hop-by-hop headers from
// the outbound request -- a client naming "Authorization" in its own
// Connection header would otherwise get the just-set Authorization header
// stripped right back out. Rewrite runs after that stripping, so what it
// sets survives untouched.
func New(upstream, credential string) (http.Handler, error) {
	u, err := url.Parse(upstream)
	if err != nil {
		return nil, fmt.Errorf("registryproxy: parse upstream URL %q: %w", upstream, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("registryproxy: upstream URL %q must be absolute", upstream)
	}

	upstreamQuery := u.RawQuery

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
			if credential != "" {
				pr.Out.Header.Set("Authorization", "Bearer "+credential)
			}
		},
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
		if !isAllowedPath(r.URL.Path) {
			log.Printf("registryproxy: path outside derived allowlist: %s %s", r.Method, r.URL.Path)
		}
		rp.ServeHTTP(w, r)
	}), nil
}

// Proxy serves an http.Handler over a unix domain socket.
type Proxy struct {
	// Handler is the http.Handler to serve, typically built with New.
	Handler http.Handler

	listener net.Listener
}

// ListenAndServe removes any stale file at socketPath, listens on a unix
// domain socket there, and serves Handler on it in the background. It
// returns once the listener is established; serving happens in a separate
// goroutine.
func (p *Proxy) ListenAndServe(socketPath string) error {
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

// Close stops the proxy from accepting further connections.
func (p *Proxy) Close() error {
	if p.listener == nil {
		return nil
	}
	return p.listener.Close()
}
