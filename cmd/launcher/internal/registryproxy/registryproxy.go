// Package registryproxy implements a credential-free, GET/HEAD-only
// pass-through reverse proxy served over a unix domain socket.
//
// This is the tracer-bullet slice of ADR 0044: no credential is attached
// here and no redirect handling beyond Go's default client behavior is
// implemented. That belongs to a follow-up ticket.
package registryproxy

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
)

// New builds an http.Handler that forwards GET and HEAD requests to
// upstream, preserving path and query string, and rejects every other
// method with 405 Method Not Allowed without forwarding it upstream.
func New(upstream string) (http.Handler, error) {
	u, err := url.Parse(upstream)
	if err != nil {
		return nil, fmt.Errorf("registryproxy: parse upstream URL %q: %w", upstream, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("registryproxy: upstream URL %q must be absolute", upstream)
	}

	rp := httputil.NewSingleHostReverseProxy(u)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
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
