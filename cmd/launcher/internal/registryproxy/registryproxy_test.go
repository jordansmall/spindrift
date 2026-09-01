// Package registryproxy implements a credential-free, GET/HEAD-only
// pass-through reverse proxy served over a unix domain socket (issue #2849).
package registryproxy

import (
	"bytes"
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

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

	p, err := New(upstream.URL, "")
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

func TestNew_ForwardsHEAD(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("upstream got method %q, want HEAD", r.Method)
		}
		w.Header().Set("X-Test", "yes")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(upstream.URL, "")
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

// TestNew_ForwardsQueryString verifies the proxy forwards the inbound
// request's raw query string to upstream verbatim, including query strings
// that httputil.ReverseProxy's Rewrite path would otherwise silently mangle
// to empty via cleanQueryParams: a semicolon-separated query and a query
// with a malformed percent-escape.
func TestNew_ForwardsQueryString(t *testing.T) {
	cases := []struct {
		name  string
		query string
	}{
		{name: "normal", query: "foo=bar&baz=qux"},
		{name: "semicolon separated", query: "a=1;b=2"},
		{name: "malformed percent escape", query: "a=%zz"},
		{name: "empty", query: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotRawQuery string
			var sawRawQuery bool
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotRawQuery, sawRawQuery = r.URL.RawQuery, true
				w.WriteHeader(http.StatusOK)
			}))
			defer upstream.Close()

			p, err := New(upstream.URL, "")
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			target := "/crates/foo"
			if tc.query != "" {
				target += "?" + tc.query
			}

			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, target, nil)
			p.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
			}
			if !sawRawQuery {
				t.Fatalf("upstream never received a request")
			}
			if gotRawQuery != tc.query {
				t.Errorf("upstream got RawQuery %q, want %q", gotRawQuery, tc.query)
			}
		})
	}
}

// TestNew_CombinesUpstreamAndInboundQueryStrings verifies that when the
// upstream URL passed to New itself carries a query string, the proxy
// combines it with the inbound request's own query string rather than
// letting either clobber the other -- matching what
// httputil.NewSingleHostReverseProxy's legacy Director did (join with "&"
// when both are non-empty, otherwise just whichever one is non-empty).
func TestNew_CombinesUpstreamAndInboundQueryStrings(t *testing.T) {
	cases := []struct {
		name          string
		upstreamQuery string
		inboundQuery  string
		wantRawQuery  string
	}{
		{name: "both present", upstreamQuery: "tok=UPSTREAMTOKEN", inboundQuery: "a=1", wantRawQuery: "tok=UPSTREAMTOKEN&a=1"},
		{name: "upstream only", upstreamQuery: "tok=UPSTREAMTOKEN", inboundQuery: "", wantRawQuery: "tok=UPSTREAMTOKEN"},
		{name: "inbound only", upstreamQuery: "", inboundQuery: "a=1", wantRawQuery: "a=1"},
		{name: "neither", upstreamQuery: "", inboundQuery: "", wantRawQuery: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotRawQuery string
			var sawRawQuery bool
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotRawQuery, sawRawQuery = r.URL.RawQuery, true
				w.WriteHeader(http.StatusOK)
			}))
			defer upstream.Close()

			upstreamURL := upstream.URL + "/base"
			if tc.upstreamQuery != "" {
				upstreamURL += "?" + tc.upstreamQuery
			}

			p, err := New(upstreamURL, "")
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			target := "/crates/foo"
			if tc.inboundQuery != "" {
				target += "?" + tc.inboundQuery
			}

			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, target, nil)
			p.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
			}
			if !sawRawQuery {
				t.Fatalf("upstream never received a request")
			}
			if gotRawQuery != tc.wantRawQuery {
				t.Errorf("upstream got RawQuery %q, want %q", gotRawQuery, tc.wantRawQuery)
			}
		})
	}
}

// TestNew_SetsXForwardedForHeader verifies the proxy sets a non-empty
// X-Forwarded-For header on the outbound request reflecting the client's
// address, matching what httputil.NewSingleHostReverseProxy's legacy
// Director-based implementation did on main.
func TestNew_SetsXForwardedForHeader(t *testing.T) {
	var gotXFF string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotXFF = r.Header.Get("X-Forwarded-For")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(upstream.URL, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/crates/foo", nil)
	req.RemoteAddr = "203.0.113.7:12345"
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if gotXFF == "" {
		t.Errorf("upstream got empty X-Forwarded-For, want it set")
	}
	if !strings.Contains(gotXFF, "203.0.113.7") {
		t.Errorf("X-Forwarded-For = %q, want it to contain client IP %q", gotXFF, "203.0.113.7")
	}
}

func TestServe_UnixSocket(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("via socket"))
	}))
	defer upstream.Close()

	handler, err := New(upstream.URL, "")
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

	handler, err := New("http://127.0.0.1:0", "")
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
			if _, err := New(upstream, ""); err == nil {
				t.Errorf("New(%q) = nil error, want error", upstream)
			}
		})
	}
}

// TestNew_RejectsNonGetHead verifies a POST/PUT request is rejected with 405
// and never reaches the upstream, whether or not a credential is configured.
func TestNew_RejectsNonGetHead(t *testing.T) {
	for _, credential := range []string{"", "s3kr1t"} {
		for _, method := range []string{http.MethodPost, http.MethodPut} {
			t.Run(credential+"/"+method, func(t *testing.T) {
				var hits int32
				var gotAuth string
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					atomic.AddInt32(&hits, 1)
					gotAuth = r.Header.Get("Authorization")
					w.WriteHeader(http.StatusOK)
				}))
				defer upstream.Close()

				p, err := New(upstream.URL, credential)
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
				if gotAuth != "" {
					t.Errorf("upstream got Authorization %q, want none (request never forwarded)", gotAuth)
				}
			})
		}
	}
}

// TestNew_AttachesCredentialToOutboundRequest verifies that when New is
// given a non-empty credential, every request the proxy forwards upstream
// carries it as "Authorization: Bearer <credential>" (ADR 0044).
func TestNew_AttachesCredentialToOutboundRequest(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(upstream.URL, "s3kr1t")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/crates/foo", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if want := "Bearer s3kr1t"; gotAuth != want {
		t.Errorf("upstream got Authorization %q, want %q", gotAuth, want)
	}
}

// TestNew_AttachesCredentialEvenWithConnectionHeaderTrick verifies the
// credential survives even when the inbound client request names
// "Authorization" in its own Connection header, an ad-hoc hop-by-hop-header
// trick a Box-controlled client could otherwise use to make
// httputil.ReverseProxy strip the Authorization header the proxy just set,
// defeating credential injection entirely (ADR 0044).
func TestNew_AttachesCredentialEvenWithConnectionHeaderTrick(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(upstream.URL, "s3kr1t")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/crates/foo", nil)
	req.Header.Set("Connection", "Authorization")
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if want := "Bearer s3kr1t"; gotAuth != want {
		t.Errorf("upstream got Authorization %q, want %q (Connection header trick must not strip credential)", gotAuth, want)
	}
}

// TestNew_RewritesHostHeaderToUpstream verifies the proxy always sends the
// upstream's own Host header on the outbound leg, even when the inbound
// client request supplies an arbitrary Host header. httputil.
// NewSingleHostReverseProxy's base director rewrites req.URL.Host but not
// req.Host, so without an explicit fix a Box-controlled client could steer a
// configured credential to a different vhost/tenant sharing the upstream's
// IP/certificate by simply setting its own Host header.
func TestNew_RewritesHostHeaderToUpstream(t *testing.T) {
	var gotHost string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(upstream.URL, "s3kr1t")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", upstream.URL, err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/crates/foo", nil)
	req.Host = "evil.example"
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if gotHost != upstreamURL.Host {
		t.Errorf("upstream got Host %q, want %q (must not leak client-supplied Host)", gotHost, upstreamURL.Host)
	}
}

func TestNew_EmptyCredentialAttachesNoAuthorizationHeader(t *testing.T) {
	var gotAuth string
	var sawHeader bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, sawHeader = r.Header.Get("Authorization"), r.Header.Get("Authorization") != ""
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(upstream.URL, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/crates/foo", nil)
	p.ServeHTTP(rr, req)

	if sawHeader {
		t.Errorf("upstream got Authorization %q, want none", gotAuth)
	}
}

// TestNew_DoesNotFollowRedirect verifies that when upstream responds with a
// 3xx redirect, the proxy relays the bare redirect (status + Location) back
// to the client without itself following it -- the upstream sees exactly one
// request, never a second hop to the redirect target, so a configured
// credential never crosses the redirect (ADR 0044).
func TestNew_DoesNotFollowRedirect(t *testing.T) {
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Location", "https://elsewhere.example/target")
		w.WriteHeader(http.StatusFound)
	}))
	defer upstream.Close()

	p, err := New(upstream.URL, "s3kr1t")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/crates/foo", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusFound)
	}
	if got := rr.Header().Get("Location"); got != "https://elsewhere.example/target" {
		t.Errorf("Location = %q, want %q", got, "https://elsewhere.example/target")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("upstream got %d requests, want exactly 1 (no second hop)", got)
	}
}

// TestNew_VerifiesUpstreamTLSCertificate guards the "upstream TLS
// certificate is verified normally" acceptance criterion: New builds its
// ReverseProxy with no custom Transport, so it inherits
// http.DefaultTransport's normal certificate verification. Pointing it at an
// httptest.NewTLSServer -- whose self-signed certificate is not trusted by
// the default system cert pool -- must make the outbound TLS handshake fail,
// which httputil.ReverseProxy's default error handler surfaces as a 502 Bad
// Gateway. A regression that swapped in a Transport with
// InsecureSkipVerify: true would instead complete the handshake and return
// the upstream's 200, so this test would catch it.
func TestNew_VerifiesUpstreamTLSCertificate(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("should never be seen"))
	}))
	defer upstream.Close()

	p, err := New(upstream.URL, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Silence the ReverseProxy default error handler's log line for the
	// expected handshake failure, matching the log-suppression pattern used
	// by TestNew_NeverLogsCredential above.
	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/crates/foo", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d (untrusted upstream certificate must fail the handshake)", rr.Code, http.StatusBadGateway)
	}
}

// TestNew_NeverLogsCredential drives a real request/response cycle through
// the proxy with a credential configured and asserts the credential
// substring never appears in whatever the standard logger emits during that
// cycle, guarding against a future stray log line leaking it.
func TestNew_NeverLogsCredential(t *testing.T) {
	const credential = "s3kr1t-do-not-log-me"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	p, err := New(upstream.URL, credential)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/crates/foo", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if strings.Contains(logBuf.String(), credential) {
		t.Errorf("log output contained the credential: %q", logBuf.String())
	}
}

// TestNew_ServesPathOutsideAllowlist verifies a request whose path falls
// outside the derived allowlist (e.g. cargo's download endpoint, which
// isAllowedPath deliberately excludes) is still served normally -- the
// allowlist is log-only in v1, never enforced (issue #2852).
func TestNew_ServesPathOutsideAllowlist(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("download body"))
	}))
	defer upstream.Close()

	p, err := New(upstream.URL, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/crates/foo/1.0.0/download", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (out-of-allowlist path must still be served)", rr.Code, http.StatusOK)
	}
	body, err := io.ReadAll(rr.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(body) != "download body" {
		t.Errorf("body = %q, want %q", string(body), "download body")
	}
}

func TestNew_LogsPathOutsideAllowlist(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(upstream.URL, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/crates/foo/1.0.0/download", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	logged := logBuf.String()
	if !strings.Contains(logged, "registryproxy: path outside derived allowlist:") {
		t.Errorf("log output = %q, want it to contain the distinguishing marker", logged)
	}
	if !strings.Contains(logged, http.MethodGet) {
		t.Errorf("log output = %q, want it to contain the method %q", logged, http.MethodGet)
	}
	if !strings.Contains(logged, "/api/v1/crates/foo/1.0.0/download") {
		t.Errorf("log output = %q, want it to contain the path", logged)
	}
}

func TestNew_NoLogForAllowlistedPath(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(upstream.URL, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/config.json", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if strings.Contains(logBuf.String(), "registryproxy: path outside derived allowlist:") {
		t.Errorf("log output = %q, want no allowlist-miss log line for an allowlisted path", logBuf.String())
	}
}

// TestNew_NeverLogsCredentialForOutOfAllowlistPath verifies that the new
// allowlist-miss log line, specifically, never carries a configured
// credential -- mirroring TestNew_NeverLogsCredential but exercising the
// out-of-allowlist code path directly, since that test's own path
// ("/crates/foo") happens to also fall outside the allowlist but was written
// before this log line existed and doesn't assert against it.
func TestNew_NeverLogsCredentialForOutOfAllowlistPath(t *testing.T) {
	const credential = "sekret-token"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(upstream.URL, credential)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/crates/foo/1.0.0/download", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if strings.Contains(logBuf.String(), credential) {
		t.Errorf("log output contained the credential: %q", logBuf.String())
	}
}
