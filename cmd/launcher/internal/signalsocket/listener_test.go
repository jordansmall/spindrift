package signalsocket

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/signalwire"
	"spindrift.dev/launcher/internal/unixsocket"
)

// mirroredOps decodes log as a stream of claude.Event lines and returns the
// spindrift_op payloads, failing the test on anything else. Package-local
// twin of handler_test.go's helper of the same name: that one lives in
// signalsocket_test and this file is package signalsocket, so the two are
// not the same identifier despite the shared name.
func mirroredOps(t *testing.T, log string) []claude.SpindriftOp {
	t.Helper()
	var ops []claude.SpindriftOp
	for _, line := range strings.Split(strings.TrimSuffix(log, "\n"), "\n") {
		if line == "" {
			continue
		}
		var ev claude.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("mirror line %q: %v", line, err)
		}
		if ev.Type != "spindrift_op" || ev.SpindriftOp == nil {
			t.Fatalf("mirror line %q is not a spindrift_op event", line)
		}
		ops = append(ops, *ev.SpindriftOp)
	}
	return ops
}

// testSocketDir prefers t.TempDir() but falls back to a fresh dir directly
// under /tmp when that path would already overflow AF_UNIX's sun_path cap
// once a filename is joined onto it (issue #3077). A nix build sandbox's own
// working directory can nest deep enough to trigger this, unlike an ordinary
// `go test` invocation from a shell.
func testSocketDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if !unixsocket.TooLong(filepath.Join(dir, "signal.sock")) {
		return dir
	}
	fallback, err := os.MkdirTemp("/tmp", "spindrift-signal-test-*")
	if err != nil {
		t.Fatalf("mktemp under /tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(fallback) })
	return fallback
}

// newTestListener serves a real Buffer behind a real NewHandler: this slice's
// claim is that the transport carries the handler unchanged, so a stub
// handler would test nothing.
func newTestListener(t *testing.T) (*Listener, *Buffer, *bytes.Buffer) {
	t.Helper()
	buf := New(Config{Consumes: []signalwire.Kind{signalwire.KindComment, signalwire.KindPRIntent, signalwire.KindIssueIntent}})
	logw := &bytes.Buffer{}
	l := &Listener{Handler: NewHandler(buf, logw)}
	t.Cleanup(func() { _ = l.Close() })
	return l, buf, logw
}

// newGatedTestListener serves a real Buffer behind a real NewGatedHandler,
// mirroring newTestListener's unix counterpart for the TCP tests below.
func newGatedTestListener(t *testing.T, secret string) (*Listener, *Buffer, *bytes.Buffer) {
	t.Helper()
	buf := New(Config{Consumes: []signalwire.Kind{signalwire.KindComment, signalwire.KindPRIntent, signalwire.KindIssueIntent}})
	logw := &bytes.Buffer{}
	gh, err := NewGatedHandler(buf, logw, secret)
	if err != nil {
		t.Fatalf("NewGatedHandler: %v", err)
	}
	l := &Listener{Handler: gh}
	t.Cleanup(func() { _ = l.Close() })
	return l, buf, logw
}

func unixClient(path string) *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", path)
			},
		},
	}
}

func post(t *testing.T, c *http.Client, url, body string, header map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestListener_UnixRoundTrip(t *testing.T) {
	l, _, logw := newTestListener(t)
	path := filepath.Join(testSocketDir(t), "signal.sock")
	if err := l.ListenAndServe(path); err != nil {
		t.Fatalf("ListenAndServe: %v", err)
	}
	c := unixClient(path)

	resp := post(t, c, "http://signal/comment", `{"body":"hello from the box"}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /comment status = %d, want 200", resp.StatusCode)
	}
	var rec signalwire.Receipt
	if err := json.NewDecoder(resp.Body).Decode(&rec); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	if rec.Kind != signalwire.KindComment || rec.Bytes == 0 || rec.Hash == "" {
		t.Fatalf("receipt = %+v, want a comment receipt with size and hash", rec)
	}

	sresp, err := c.Get("http://signal/status")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer sresp.Body.Close()
	var st signalwire.Status
	if err := json.NewDecoder(sresp.Body).Decode(&st); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if st.Comment == nil || st.Comment.Hash != rec.Hash {
		t.Fatalf("status = %+v, want the accepted comment's receipt", st)
	}
	if !strings.Contains(logw.String(), `"spindrift_op"`) {
		t.Fatalf("mirror = %q, want a spindrift_op event", logw.String())
	}
}

func TestListener_UnixRemovesStaleSocket(t *testing.T) {
	l, _, _ := newTestListener(t)
	path := filepath.Join(testSocketDir(t), "signal.sock")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write stale socket: %v", err)
	}
	if err := l.ListenAndServe(path); err != nil {
		t.Fatalf("ListenAndServe over stale socket: %v", err)
	}
}

func TestListener_UnixPathTooLong(t *testing.T) {
	l, _, _ := newTestListener(t)
	path := "/tmp/" + strings.Repeat("x", unixsocket.Cap())
	err := l.ListenAndServe(path)
	if err == nil {
		t.Fatal("ListenAndServe over an oversize path = nil, want an error")
	}
	if !strings.Contains(err.Error(), "sun_path") {
		t.Fatalf("error = %v, want it to name the sun_path limit", err)
	}
	if l.Addr() != nil {
		t.Fatalf("Addr() = %v after a rejected path, want nil", l.Addr())
	}
}

func TestListener_TCPWithSecret(t *testing.T) {
	secret, err := NewSecret()
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	l, buf, _ := newGatedTestListener(t, secret)
	if err := l.ListenAndServeTCP("127.0.0.1:0"); err != nil {
		t.Fatalf("ListenAndServeTCP: %v", err)
	}
	c := &http.Client{Timeout: 10 * time.Second}
	url := "http://" + l.Addr().String() + "/comment"
	resp := post(t, c, url, `{"body":"hello"}`, map[string]string{signalwire.SecretHeader: secret})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if _, ok := buf.Comment(); !ok {
		t.Fatal("buffer holds no comment, want the accepted one")
	}
}

// TestListener_TCPRejectsBadSecret pins the review finding fixed by this
// slice (issue #3724): the gate now runs inside the mirror, so a wrong or
// missing secret still emits exactly one spindrift_op reject event -- it no
// longer disappears before the one emission point the way a wrapper-around
// gate would.
func TestListener_TCPRejectsBadSecret(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header map[string]string
	}{
		{"missing", nil},
		{"wrong", map[string]string{signalwire.SecretHeader: "0123456789abcdef0123456789abcdef"}},
		{"empty", map[string]string{signalwire.SecretHeader: ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			secret, err := NewSecret()
			if err != nil {
				t.Fatalf("NewSecret: %v", err)
			}
			l, buf, logw := newGatedTestListener(t, secret)
			if err := l.ListenAndServeTCP("127.0.0.1:0"); err != nil {
				t.Fatalf("ListenAndServeTCP: %v", err)
			}
			c := &http.Client{Timeout: 10 * time.Second}
			url := "http://" + l.Addr().String() + "/comment"
			resp := post(t, c, url, `{"body":"hello"}`, tc.header)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", resp.StatusCode)
			}
			if _, ok := buf.Comment(); ok {
				t.Fatal("buffer holds a comment: the request reached the handler")
			}

			ops := mirroredOps(t, logw.String())
			if len(ops) != 1 {
				t.Fatalf("mirror = %+v, want exactly one event", ops)
			}
			op := ops[0]
			if op.Kind != string(signalwire.KindComment) {
				t.Fatalf("kind = %q, want %q", op.Kind, signalwire.KindComment)
			}
			if op.Decision != "reject" {
				t.Fatalf("decision = %q, want reject", op.Decision)
			}
			if op.Reason == "" {
				t.Fatal("reason is empty, want a one-line reason")
			}
			if op.Size != 0 || op.Hash != "" {
				t.Fatalf("size/hash = %d/%q, want 0/\"\": the gate must not read the body", op.Size, op.Hash)
			}

			body, _ := io.ReadAll(resp.Body)
			if strings.Contains(string(body), secret) {
				t.Fatal("reply echoes the secret")
			}
			var rej signalwire.Reject
			if err := json.Unmarshal(body, &rej); err != nil {
				t.Fatalf("decode reply as {status, reason}: %v (body %q)", err, body)
			}
			if rej.Status != "unauthorized" {
				t.Fatalf("status = %q, want unauthorized", rej.Status)
			}
			if rej.Reason == "" {
				t.Fatal("reason is empty")
			}
		})
	}
}

// TestListener_TCPEmptySecretBindsNothing moved from ListenAndServeTCP's own
// guard (deleted in this slice) to NewGatedHandler's: an empty secret now
// fails at handler construction, before there is a listener to bind at all.
func TestListener_TCPEmptySecretBindsNothing(t *testing.T) {
	buf := New(Config{Consumes: []signalwire.Kind{signalwire.KindComment}})
	if _, err := NewGatedHandler(buf, nil, ""); err == nil {
		t.Fatal("NewGatedHandler with an empty secret = nil, want an error")
	}
}

// TestListener_TCPRefusesUngatedHandler pins ListenAndServeTCP's fail-closed
// guard: serving TCP with a Handler built by NewHandler (unix, no gate) must
// be refused rather than silently exposing the four routes with no secret.
func TestListener_TCPRefusesUngatedHandler(t *testing.T) {
	l, _, _ := newTestListener(t)
	if err := l.ListenAndServeTCP("127.0.0.1:0"); err == nil {
		t.Fatal("ListenAndServeTCP with an ungated handler = nil, want an error")
	}
	if l.Addr() != nil {
		t.Fatalf("Addr() = %v, want nil: nothing should have bound", l.Addr())
	}
}

// TestListener_UnixRefusesNilHandler pins ListenAndServe's fail-closed guard,
// the unix-socket twin of TestListener_TCPRefusesUngatedHandler above: a nil
// Handler must be rejected before the socket is bound, rather than binding
// and then panicking per request inside net/http's own recover.
func TestListener_UnixRefusesNilHandler(t *testing.T) {
	l := &Listener{}
	path := filepath.Join(testSocketDir(t), "signal.sock")
	if err := l.ListenAndServe(path); err == nil {
		t.Fatal("ListenAndServe with a nil handler = nil, want an error")
	}
	if l.Addr() != nil {
		t.Fatalf("Addr() = %v, want nil: nothing should have bound", l.Addr())
	}
}

func TestNewSecret(t *testing.T) {
	a, err := NewSecret()
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	if len(a) != 32 {
		t.Fatalf("len = %d, want 32 hex chars", len(a))
	}
	if strings.Trim(a, "0123456789abcdef") != "" {
		t.Fatalf("secret = %q, want lower-case hex", a)
	}
	b, err := NewSecret()
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	if a == b {
		t.Fatal("two mints produced the same secret")
	}
}

// TestSecretHeader_DistinctFromRegistryProxy pins ADR 0052's separate-listener,
// separate-credential rule against a later refactor that collapses the two.
func TestSecretHeader_DistinctFromRegistryProxy(t *testing.T) {
	if signalwire.SecretHeader == registrymanifest.TCPSecretHeader {
		t.Fatalf("signal and registry-proxy headers are both %q", signalwire.SecretHeader)
	}
}

func TestListener_AddrNilBeforeListen_CloseSafe(t *testing.T) {
	l := &Listener{}
	if l.Addr() != nil {
		t.Fatalf("Addr() = %v before listening, want nil", l.Addr())
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close() before listening = %v, want nil", err)
	}
}

// TestLimitListener_Cap holds cap connections open and shows the next one is
// not served until one is released. Every read carries a deadline, so a
// regression fails the test rather than hanging the suite.
func TestLimitListener_Cap(t *testing.T) {
	const limit = 2
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})}
	defer srv.Close()
	go func() { _ = srv.Serve(limitListener(raw, limit)) }()

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
		if _, err := io.WriteString(c, "GET / HTTP/1.1\r\nHost: signal\r\n\r\n"); err != nil {
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

// TestLimitListener_DoubleCloseDoesNotOverRelease pins the sync.Once: a second
// Close releasing a second slot would let the cap drift upward forever.
func TestLimitListener_DoubleCloseDoesNotOverRelease(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	lim := limitListener(raw, 1)
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
