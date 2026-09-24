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
	"sync"
	"time"

	"spindrift.dev/launcher/internal/unixsocket"
)

const (
	// readTimeout bounds how long one connection may take to deliver a whole
	// request. A hostile Box gets to tie up a slot on its own launcher and
	// only briefly, rather than parking a half-sent body forever. It is
	// generous next to the milliseconds a signalwire.MaxRequestBytes body
	// needs over loopback or a unix socket, so no legitimate signal can trip
	// it.
	readTimeout = 30 * time.Second

	// readHeaderTimeout bounds the header phase alone: a slowloris drip
	// never reaches the body, so readTimeout would not fire on it.
	readHeaderTimeout = 5 * time.Second

	// maxConns caps concurrent connections. The only client is one Box's
	// agent posting a handful of signals per run, so the cap exists to keep
	// a connection flood from exhausting launcher file descriptors, not to
	// meter legitimate traffic.
	maxConns = 8
)

// NewSecret mints a fresh per-run secret gating the signal socket's
// every-interface TCP fallback (signalwire.SecretHeader): 16 crypto/rand
// bytes, hex-encoded. It must never share a value with the per-run nonce
// (issue #1937) or with the registry proxy's TCP secret, each of which has a
// different security role. rand.Read fails only on a broken entropy source.
func NewSecret() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("signalsocket: mint tcp secret: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// Listener serves an http.Handler over a unix domain socket or, via
// ListenAndServeTCP, a secret-gated TCP port bound on every interface. It is
// a listener of its own rather than a route on the registry proxy: that proxy
// is GET/HEAD-only, host-rooted (ADR 0047) and starts only when routes are
// configured, none of which holds here (ADR 0052).
type Listener struct {
	// Handler is the Handler to serve, built with NewHandler for the unix
	// transport or NewGatedHandler for the TCP fallback.
	Handler *Handler

	listener net.Listener
	server   *http.Server
}

// ListenAndServe removes any stale file at socketPath, listens on a unix
// domain socket there, and serves Handler in the background. It returns once
// the listener is established. The transport carries no secret: the socket
// file's permissions are the gate. A nil Handler fails closed here, as it
// does on ListenAndServeTCP, rather than binding and then panicking once per
// request inside net/http's recover.
func (l *Listener) ListenAndServe(socketPath string) error {
	if l.Handler == nil {
		return errors.New("signalsocket: refusing to listen on a unix socket with a nil handler")
	}

	// Checked before touching the filesystem: net.Listen would fail on a
	// too-long path anyway, but with a bare EINVAL naming neither the
	// platform cap nor the path length (issue #3077).
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

// ListenAndServeTCP serves Handler on addr in the background. The caller owns
// addr and nothing here narrows it: loopback ("127.0.0.1:0") is the safe
// default, but the launcher deliberately binds every interface ("0.0.0.0:0")
// because a Box on a docker bridge reaches the host at the bridge IP, not at
// loopback. A TCP port has no filesystem permissions of its own,
// so Handler must already be gated (built with NewGatedHandler) -- serving an
// ungated Handler here would let any local process reach the four routes with
// no secret at all, so this fails closed instead. Call Addr to learn an
// ephemeral port's bound address.
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
	capped := limitListener(sock, maxConns)
	go func() {
		_ = l.server.Serve(capped)
	}()
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
func (l *Listener) Close() error {
	if l.server == nil {
		return nil
	}
	// Server.Close closes the listener it was handed and every connection it
	// is holding, so an in-flight request cannot outlive the run.
	return l.server.Close()
}

// limitListener wraps inner so that at most n connections are live at once.
// golang.org/x/net/netutil does the same thing, but is not a dependency of
// this module and is not worth becoming one for a semaphore.
func limitListener(inner net.Listener, n int) net.Listener {
	return &limitedListener{Listener: inner, sem: make(chan struct{}, n), done: make(chan struct{})}
}

type limitedListener struct {
	net.Listener
	sem       chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

// Accept takes a slot before accepting, so an over-cap connection waits in
// the kernel's backlog rather than being accepted and then stalled. The wait
// aborts on Close: otherwise a full cap would strand this goroutine on the
// semaphore, and http.Server.Close -- which waits for Serve to return --
// would deadlock behind it.
func (l *limitedListener) Accept() (net.Conn, error) {
	acquired := false
	select {
	case <-l.done:
	case l.sem <- struct{}{}:
		acquired = true
	}
	c, err := l.Listener.Accept()
	if err != nil {
		if acquired {
			<-l.sem
		}
		return nil, err
	}
	return &limitedConn{Conn: c, release: func() { <-l.sem }}, nil
}

func (l *limitedListener) Close() error {
	err := l.Listener.Close()
	l.closeOnce.Do(func() { close(l.done) })
	return err
}

type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

// Close releases the slot exactly once: http.Server can close a connection
// more than once, and a second release would raise the effective cap.
func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
