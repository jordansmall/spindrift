// Package connlimit caps concurrent connections on a net.Listener. The
// launcher's Box-facing listeners (the signal socket and the registry proxy)
// share it as a file-descriptor guard against a connection flood, so neither
// has to import the other for it.
package connlimit

import (
	"net"
	"sync"
)

// Listener wraps inner so that at most n connections are live at once.
// golang.org/x/net/netutil does the same thing, but is not a dependency of
// this module and is not worth becoming one for a semaphore.
func Listener(inner net.Listener, n int) net.Listener {
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
