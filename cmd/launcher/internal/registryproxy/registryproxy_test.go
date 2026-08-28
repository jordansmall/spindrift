// Package registryproxy implements a credential-free, GET/HEAD-only
// pass-through reverse proxy served over a unix domain socket (issue #2849).
package registryproxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// TestNew_ForwardsGET verifies a GET request through the proxy returns the
// upstream's response body and status verbatim.
func TestNew_ForwardsGET(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/crates/foo" {
			t.Errorf("upstream got path %q, want /crates/foo", r.URL.Path)
		}
		w.Header().Set("X-Test", "yes")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello from upstream"))
	}))
	defer upstream.Close()

	p, err := New(upstream.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/crates/foo", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Header().Get("X-Test"); got != "yes" {
		t.Errorf("X-Test header = %q, want %q", got, "yes")
	}
	body, err := io.ReadAll(rr.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(body) != "hello from upstream" {
		t.Errorf("body = %q, want %q", string(body), "hello from upstream")
	}
}

// TestNew_ForwardsHEAD verifies a HEAD request is forwarded like GET: status
// and headers come through, with no body.
func TestNew_ForwardsHEAD(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("upstream got method %q, want HEAD", r.Method)
		}
		w.Header().Set("X-Test", "yes")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(upstream.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "/crates/foo", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Header().Get("X-Test"); got != "yes" {
		t.Errorf("X-Test header = %q, want %q", got, "yes")
	}
	body, err := io.ReadAll(rr.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(body) != 0 {
		t.Errorf("body = %q, want empty", string(body))
	}
}

// TestServe_UnixSocket verifies the proxy can be served over a unix domain
// socket, forwards a GET to a real upstream, and stops accepting
// connections once closed.
func TestServe_UnixSocket(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("via socket"))
	}))
	defer upstream.Close()

	handler, err := New(upstream.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	socketPath := filepath.Join(t.TempDir(), "proxy.sock")
	p := &Proxy{Handler: handler}
	if err := p.ListenAndServe(socketPath); err != nil {
		t.Fatalf("ListenAndServe: %v", err)
	}

	client := &http.Client{
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}

	resp, err := client.Get("http://unix/anything")
	if err != nil {
		t.Fatalf("client.Get: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(body) != "via socket" {
		t.Errorf("body = %q, want %q", string(body), "via socket")
	}

	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := client.Do(func() *http.Request {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/anything", nil)
		return req
	}()); err == nil {
		t.Errorf("expected error dialing after Close, got nil")
	}
}

// TestServe_RemovesStaleSocket verifies ListenAndServe removes a leftover
// socket file from a prior run instead of failing with "address already in
// use".
func TestServe_RemovesStaleSocket(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "proxy.sock")

	stale, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	stale.Close()

	handler, err := New("http://127.0.0.1:0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p := &Proxy{Handler: handler}
	if err := p.ListenAndServe(socketPath); err != nil {
		t.Fatalf("ListenAndServe: %v", err)
	}
	defer p.Close()
}

// TestNew_MalformedUpstream verifies a malformed upstream URL returns an
// error, not a panic.
func TestNew_MalformedUpstream(t *testing.T) {
	cases := []string{
		"://not-a-url",
		"not-even-a-url no scheme",
		"",
		"/just/a/path",
	}
	for _, upstream := range cases {
		t.Run(upstream, func(t *testing.T) {
			if _, err := New(upstream); err == nil {
				t.Errorf("New(%q) = nil error, want error", upstream)
			}
		})
	}
}

// TestNew_RejectsNonGetHead verifies a POST/PUT request is rejected with 405
// and never reaches the upstream.
func TestNew_RejectsNonGetHead(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		t.Run(method, func(t *testing.T) {
			var hits int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&hits, 1)
				w.WriteHeader(http.StatusOK)
			}))
			defer upstream.Close()

			p, err := New(upstream.URL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			rr := httptest.NewRecorder()
			req := httptest.NewRequest(method, "/crates/foo", nil)
			p.ServeHTTP(rr, req)

			if rr.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
			}
			if got := atomic.LoadInt32(&hits); got != 0 {
				t.Errorf("upstream got %d requests, want 0", got)
			}
		})
	}
}
