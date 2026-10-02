package registryproxy

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/registrymanifest"
)

const limitsTestSecret = "limits-test-secret"

// startLimitsProxy serves h over TCP on an ephemeral loopback port and returns
// the proxy plus a dialer whose connections close at test end.
func startLimitsProxy(t *testing.T, h http.Handler) (*Proxy, func() net.Conn) {
	t.Helper()
	p := &Proxy{Handler: h}
	if err := p.ListenAndServeTCP("127.0.0.1:0", limitsTestSecret); err != nil {
		t.Fatalf("ListenAndServeTCP: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	addr := p.Addr().String()
	return p, func() net.Conn {
		t.Helper()
		c, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
}

func limitsRequest(t *testing.T, c net.Conn, path string) {
	t.Helper()
	req := "GET " + path + " HTTP/1.1\r\nHost: x\r\n" + registrymanifest.TCPSecretHeader + ": " + limitsTestSecret + "\r\n\r\n"
	if _, err := io.WriteString(c, req); err != nil {
		t.Fatalf("write request: %v", err)
	}
}

// assertStalledConnClosed sends headers it never finishes and requires the
// server to close the conn (EOF), not leave it to the read deadline.
func assertStalledConnClosed(t *testing.T, c net.Conn) {
	t.Helper()
	if _, err := io.WriteString(c, "GET /x HTTP/1.1\r\nHost: x\r\n"); err != nil {
		t.Fatalf("write partial headers: %v", err)
	}
	if err := c.SetReadDeadline(time.Now().Add(readHeaderTimeout + 3*time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	// A server-side close reads as EOF; a deadline error is the regression.
	_, err := io.ReadAll(c)
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatalf("stalled conn still open after %v: %v", readHeaderTimeout+3*time.Second, err)
	}
	if err != nil {
		t.Fatalf("read: %v", err)
	}
}

func TestServe_ReadHeaderTimeoutClosesStalledConn(t *testing.T) {
	_, dial := startLimitsProxy(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	assertStalledConnClosed(t, dial())
}

func TestServe_ReadHeaderTimeoutClosesStalledUnixConn(t *testing.T) {
	// Not t.TempDir(): its path can overflow AF_UNIX sun_path on macOS.
	dir, err := os.MkdirTemp("", "rp")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "p.sock")
	p := &Proxy{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}
	if err := p.ListenAndServe(socketPath); err != nil {
		t.Fatalf("ListenAndServe: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	c, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	assertStalledConnClosed(t, c)
}

func TestServe_ConnectionCapQueuesOverCapConns(t *testing.T) {
	var entered sync.WaitGroup
	entered.Add(maxConns)
	hold := make(chan struct{})
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hold" {
			entered.Done()
			select {
			case <-hold:
			case <-done:
			}
		}
		_, _ = io.WriteString(w, "ok")
	})
	_, dial := startLimitsProxy(t, h)

	held := make([]net.Conn, maxConns)
	for i := range held {
		held[i] = dial()
		limitsRequest(t, held[i], "/hold")
	}
	waited := make(chan struct{})
	go func() { entered.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(10 * time.Second):
		t.Fatalf("only part of %d held requests reached the handler", maxConns)
	}

	extra := dial()
	limitsRequest(t, extra, "/extra")
	extraR := bufio.NewReader(extra)
	readStatus := func(wait time.Duration) (string, error) {
		t.Helper()
		if err := extra.SetReadDeadline(time.Now().Add(wait)); err != nil {
			t.Fatalf("set deadline: %v", err)
		}
		return extraR.ReadString('\n')
	}
	if line, err := readStatus(300 * time.Millisecond); err == nil {
		t.Fatalf("over-cap conn was served %q, want no response while the cap is held", line)
	}

	// Close held[0] before releasing every handler: a released conn still
	// open goes idle under keep-alive and keeps its slot, so only the
	// closed one frees room for extra.
	held[0].Close()
	close(hold)
	line, err := readStatus(5 * time.Second)
	if err != nil {
		t.Fatalf("over-cap conn after a release: %v", err)
	}
	if !strings.Contains(line, "200") {
		t.Fatalf("over-cap conn status after a release = %q, want 200", line)
	}
}

func TestClose_DropsInFlightConn(t *testing.T) {
	entered := make(chan struct{})
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	p, dial := startLimitsProxy(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(entered)
		<-done
	}))

	c := dial()
	limitsRequest(t, c, "/slow")
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request never reached the handler")
	}

	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := c.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if _, err := io.ReadAll(c); err != nil {
		t.Fatalf("in-flight conn not closed after Close: %v", err)
	}
}
