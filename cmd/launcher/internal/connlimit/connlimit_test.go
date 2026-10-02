package connlimit

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestListener_Cap holds cap connections open and shows the next one is
// not served until one is released. Every read carries a deadline, so a
// regression fails the test rather than hanging the suite.
func TestListener_Cap(t *testing.T) {
	const limit = 2
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})}
	defer srv.Close()
	go func() { _ = srv.Serve(Listener(raw, limit)) }()

	addr := raw.Addr().String()
	dial := func() net.Conn {
		t.Helper()
		c, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	request := func(c net.Conn) {
		t.Helper()
		if _, err := io.WriteString(c, "GET / HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
			t.Fatalf("write request: %v", err)
		}
	}
	// A short deadline: served responses arrive in microseconds over
	// loopback, so the wait only has to outlast scheduling jitter.
	readStatus := func(r *bufio.Reader, c net.Conn, wait time.Duration) (string, error) {
		t.Helper()
		if err := c.SetReadDeadline(time.Now().Add(wait)); err != nil {
			t.Fatalf("set deadline: %v", err)
		}
		return r.ReadString('\n')
	}

	held := make([]net.Conn, limit)
	for i := range held {
		held[i] = dial()
		request(held[i])
		line, err := readStatus(bufio.NewReader(held[i]), held[i], 5*time.Second)
		if err != nil {
			t.Fatalf("held conn %d: %v", i, err)
		}
		if !strings.Contains(line, "200") {
			t.Fatalf("held conn %d status = %q, want 200", i, line)
		}
	}

	extra := dial()
	request(extra)
	extraR := bufio.NewReader(extra)
	if line, err := readStatus(extraR, extra, 300*time.Millisecond); err == nil {
		t.Fatalf("over-cap conn was served %q, want no response while the cap is held", line)
	}

	held[0].Close()
	line, err := readStatus(extraR, extra, 5*time.Second)
	if err != nil {
		t.Fatalf("over-cap conn after a release: %v", err)
	}
	if !strings.Contains(line, "200") {
		t.Fatalf("over-cap conn status after a release = %q, want 200", line)
	}
}

// TestListener_DoubleCloseDoesNotOverRelease pins the sync.Once: a second
// Close releasing a second slot would let the cap drift upward forever.
func TestListener_DoubleCloseDoesNotOverRelease(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	lim := Listener(raw, 1)
	defer lim.Close()

	accepted := make(chan net.Conn, 3)
	accept := func() {
		t.Helper()
		go func() {
			c, err := lim.Accept()
			if err == nil {
				accepted <- c
			}
		}()
		c, err := net.DialTimeout("tcp", raw.Addr().String(), 5*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		t.Cleanup(func() { c.Close() })
	}
	take := func(wait time.Duration) net.Conn {
		t.Helper()
		select {
		case c := <-accepted:
			t.Cleanup(func() { c.Close() })
			return c
		case <-time.After(wait):
			return nil
		}
	}

	accept()
	first := take(5 * time.Second)
	if first == nil {
		t.Fatal("Accept never returned")
	}
	first.Close()
	first.Close()

	// One slot, one release: the second Accept must be the only one the
	// double Close admitted, and it holds the slot open.
	accept()
	if second := take(5 * time.Second); second == nil {
		t.Fatal("second Accept never returned after a release")
	}
	accept()
	if third := take(300 * time.Millisecond); third != nil {
		t.Fatal("a third Accept ran: the double Close over-released the cap")
	}
}

// countingListener reports each inner Accept on accepted, so a test sees the
// call as it happens instead of sampling a counter after a sleep.
type countingListener struct {
	net.Listener
	accepted chan struct{}
}

func (l *countingListener) Accept() (net.Conn, error) {
	l.accepted <- struct{}{}
	c, peer := net.Pipe()
	peer.Close()
	return c, nil
}

func (l *countingListener) Close() error { return nil }

// TestListener_AcceptTakesSlotBeforeInnerAccept pins the ordering that keeps
// over-cap connections in the kernel backlog: the inner Accept must not run
// until a slot is free, or they would consume launcher file descriptors.
func TestListener_AcceptTakesSlotBeforeInnerAccept(t *testing.T) {
	inner := &countingListener{accepted: make(chan struct{}, 2)}
	lim := Listener(inner, 1)
	defer lim.Close()

	first, err := lim.Accept()
	if err != nil {
		t.Fatalf("first Accept: %v", err)
	}
	t.Cleanup(func() { first.Close() })
	<-inner.accepted

	type result struct {
		c   net.Conn
		err error
	}
	second := make(chan result, 1)
	go func() {
		c, err := lim.Accept()
		second <- result{c, err}
	}()

	select {
	case <-inner.accepted:
		t.Fatal("inner Accept ran while the cap was held")
	case <-time.After(200 * time.Millisecond):
	}

	first.Close()
	select {
	case r := <-second:
		if r.err != nil {
			t.Fatalf("second Accept: %v", r.err)
		}
		t.Cleanup(func() { r.c.Close() })
	case <-time.After(5 * time.Second):
		t.Fatal("second Accept never returned after a release")
	}
	select {
	case <-inner.accepted:
	default:
		t.Fatal("second Accept returned without an inner Accept")
	}
}
