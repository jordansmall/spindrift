package signalsocket

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"time"

	"spindrift.dev/launcher/internal/connlimit"
	"spindrift.dev/launcher/internal/panicguard"
	"spindrift.dev/launcher/internal/unixsocket"
)

const (
	// readTimeout stops a hostile Box parking a half-sent body forever; a
	// signalwire.MaxRequestBytes body needs milliseconds, so no legitimate
	// signal trips it.
	readTimeout = 30 * time.Second

	// readHeaderTimeout bounds the header phase alone: a slowloris drip
	// never reaches the body, so readTimeout would not fire on it.
	readHeaderTimeout = 5 * time.Second

	// maxConns keeps a connection flood from exhausting launcher file
	// descriptors; one Box posts only a handful of signals per run.
	maxConns = 8
)

// NewSecret mints a fresh per-run secret gating the signal socket's TCP
// fallback. It must never share a value with the per-run nonce (issue #1937)
// or the registry proxy's TCP secret: each has a different security role.
func NewSecret() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("signalsocket: mint tcp secret: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// Listener serves an http.Handler over a unix domain socket or a secret-gated
// TCP port. It is not a route on the registry proxy, which is GET/HEAD-only,
// host-rooted (ADR 0047) and starts only when routes are configured (ADR 0052).
type Listener struct {
	// Handler is the Handler to serve, built with NewHandler for the unix
	// transport or NewGatedHandler for the TCP fallback.
	Handler *Handler

	listener net.Listener
	server   *http.Server
}

// ListenAndServe replaces any stale file at socketPath with a unix socket and
// serves Handler in the background. The socket file's permissions are the
// gate. A nil Handler fails closed rather than panicking once per request.
func (l *Listener) ListenAndServe(socketPath string) error {
	if l.Handler == nil {
		return errors.New("signalsocket: refusing to listen on a unix socket with a nil handler")
	}

	// Checked up front: net.Listen would fail on a too-long path with a bare
	// EINVAL naming neither the cap nor the length (issue #3077).
	if unixsocket.TooLong(socketPath) {
		return fmt.Errorf("signalsocket: socket path is %d bytes, at or over the %d-byte AF_UNIX sun_path limit on %s: %s", len(socketPath), unixsocket.Cap(), runtime.GOOS, socketPath)
	}

	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("signalsocket: remove stale socket %q: %w", socketPath, err)
	}

	sock, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("signalsocket: listen on %q: %w", socketPath, err)
	}
	l.serve(sock, l.Handler)
	return nil
}

// ListenAndServeTCP serves Handler on addr in the background. The launcher
// binds every interface because a Box on a docker bridge reaches the host at
// the bridge IP, not loopback. A TCP port has no file permissions, so an
// ungated Handler fails closed. Addr reports an ephemeral port.
func (l *Listener) ListenAndServeTCP(addr string) error {
	if l.Handler == nil || !l.Handler.gated {
		return errors.New("signalsocket: refusing to listen on TCP with an ungated handler")
	}

	sock, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("signalsocket: listen on %q: %w", addr, err)
	}

	l.serve(sock, l.Handler)
	return nil
}

// serve starts the background server. Addr reports sock's address, not the
// capped wrapper's, so an ephemeral port is still discoverable.
func (l *Listener) serve(sock net.Listener, h http.Handler) {
	l.listener = sock
	l.server = &http.Server{
		Handler:           h,
		ReadTimeout:       readTimeout,
		ReadHeaderTimeout: readHeaderTimeout,
	}
	capped := connlimit.Listener(sock, maxConns)
	panicguard.Go(func() {
		_ = l.server.Serve(capped)
	})
}

// Addr returns the address the listener is bound to, or nil when neither
// ListenAndServe nor ListenAndServeTCP has successfully been called.
func (l *Listener) Addr() net.Addr {
	if l.listener == nil {
		return nil
	}
	return l.listener.Addr()
}

// Close stops the listener from accepting further connections.
// The returned error is the server's; the raw listener's close error is not.
func (l *Listener) Close() error {
	if l.server == nil {
		return nil
	}
	// Server.Close closes every connection it is holding, so an in-flight
	// request cannot outlive the run.
	err := l.server.Close()
	if l.listener != nil {
		// Server.Close only closes the listener once Serve has tracked it, and
		// Close can win that race. A second-close error is expected.
		_ = l.listener.Close()
	}
	return err
}
