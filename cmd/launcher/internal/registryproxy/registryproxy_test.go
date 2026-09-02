// Package registryproxy implements a credential-free, GET/HEAD-only
// pass-through reverse proxy served over a unix domain socket (issue #2849).
package registryproxy

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
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

	p, err := New([]Route{{Upstream: upstream.URL, Credential: ""}})
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

	p, err := New([]Route{{Upstream: upstream.URL, Credential: ""}})
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

			p, err := New([]Route{{Upstream: upstream.URL, Credential: ""}})
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

			p, err := New([]Route{{Upstream: upstreamURL, Credential: ""}})
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

	p, err := New([]Route{{Upstream: upstream.URL, Credential: ""}})
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

// TestServe_UnixSocket verifies the proxy can be served over a unix domain
// socket, forwards a GET to a real upstream, and stops accepting
// connections once closed.
func TestServe_UnixSocket(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("via socket"))
	}))
	defer upstream.Close()

	handler, err := New([]Route{{Upstream: upstream.URL, Credential: ""}})
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

	handler, err := New([]Route{{Upstream: "http://127.0.0.1:0", Credential: ""}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p := &Proxy{Handler: handler}
	if err := p.ListenAndServe(socketPath); err != nil {
		t.Fatalf("ListenAndServe: %v", err)
	}
	defer p.Close()
}

// TestNew_RejectsEmptyRoutes verifies New refuses an empty route table with
// an error rather than building a handler that would panic on its first
// request (selectRoute indexing routes[0] of an empty slice).
func TestNew_RejectsEmptyRoutes(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("New(nil) = nil error, want error")
	}
	if _, err := New([]Route{}); err == nil {
		t.Fatal("New([]Route{}) = nil error, want error")
	}
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
			if _, err := New([]Route{{Upstream: upstream, Credential: ""}}); err == nil {
				t.Errorf("New(%q) = nil error, want error", upstream)
			}
		})
	}
}

// TestNew_SelectsRouteByHostHeader verifies a multi-route table forwards each
// request to the route whose MatchHost equals the inbound Host header,
// comparing hostnames only -- a port on either side is stripped, the
// comparison is case-insensitive, and a Host matching no route falls back to
// routes[0] (issue #3139 slice 2).
func TestNew_SelectsRouteByHostHeader(t *testing.T) {
	var gotA, gotB string
	upstreamA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotA = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("from A"))
	}))
	defer upstreamA.Close()
	upstreamB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotB = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("from B"))
	}))
	defer upstreamB.Close()

	p, err := New([]Route{
		{MatchHost: "registry-a.example", Upstream: upstreamA.URL},
		{MatchHost: "registry-b.example", Upstream: upstreamB.URL},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	do := func(host string) string {
		gotA, gotB = "", ""
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/pkg", nil)
		req.Host = host
		p.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("Host %q: status = %d, want %d", host, rr.Code, http.StatusOK)
		}
		body, err := io.ReadAll(rr.Body)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		return string(body)
	}

	if got := do("registry-a.example"); got != "from A" {
		t.Errorf("Host registry-a.example forwarded to %q, want %q", got, "from A")
	}
	if gotA != "/pkg" {
		t.Errorf("upstream A got path %q, want /pkg", gotA)
	}

	if got := do("registry-b.example"); got != "from B" {
		t.Errorf("Host registry-b.example forwarded to %q, want %q", got, "from B")
	}
	if gotB != "/pkg" {
		t.Errorf("upstream B got path %q, want /pkg", gotB)
	}

	// Port on the inbound Host is stripped before matching.
	if got := do("registry-b.example:8443"); got != "from B" {
		t.Errorf("Host registry-b.example:8443 forwarded to %q, want %q (port must be stripped)", got, "from B")
	}

	// The comparison is case-insensitive.
	if got := do("REGISTRY-B.EXAMPLE"); got != "from B" {
		t.Errorf("Host REGISTRY-B.EXAMPLE forwarded to %q, want %q (match must be case-insensitive)", got, "from B")
	}

	// A Host matching no route falls back to routes[0].
	if got := do("nowhere.example"); got != "from A" {
		t.Errorf("Host nowhere.example forwarded to %q, want %q (must fall back to routes[0])", got, "from A")
	}
}

// TestNew_SelectsRouteByHostHeader_BracketedIPv6 verifies a match-host of
// "[::1]" (no port) matches an inbound Host of "[::1]:443": net.SplitHostPort
// strips the port and yields the bracket-free "::1", so a match-host that
// still carries its own brackets must have them stripped too before the two
// sides compare, or a bracketed literal IPv6 match-host with no port would
// never match any real request.
func TestNew_SelectsRouteByHostHeader_BracketedIPv6(t *testing.T) {
	// Two routes, so a failed match falls back to the wrong one (routes[0])
	// instead of accidentally serving the right upstream anyway.
	upstreamWrong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("from wrong"))
	}))
	defer upstreamWrong.Close()
	var got string
	upstreamIPv6 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("from ipv6"))
	}))
	defer upstreamIPv6.Close()

	p, err := New([]Route{
		{MatchHost: "registry-a.example", Upstream: upstreamWrong.URL},
		{MatchHost: "[::1]", Upstream: upstreamIPv6.URL},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/pkg", nil)
	req.Host = "[::1]:443"
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body, err := io.ReadAll(rr.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(body) != "from ipv6" {
		t.Errorf("body = %q, want %q", body, "from ipv6")
	}
	if got != "/pkg" {
		t.Errorf("upstream got path %q, want /pkg", got)
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

				p, err := New([]Route{{Upstream: upstream.URL, Credential: credential}})
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
				if got := rr.Header().Get("Allow"); got != "GET, HEAD" {
					t.Errorf("Allow header = %q, want %q", got, "GET, HEAD")
				}
				if want := "registry proxy is read-only: publishing is out of scope for the Agent\n"; rr.Body.String() != want {
					t.Errorf("body = %q, want %q", rr.Body.String(), want)
				}
			})
		}
	}
}

// countingListener wraps a net.Listener and counts only accepts that
// returned a non-nil connection (successful accepts), so a test can observe
// whether upstream was ever dialed at the TCP level -- a stronger signal
// than "the upstream HTTP handler never ran" (TestNew_RejectsNonGetHead's
// hits counter), which only proves a full request/response cycle never
// completed.
type countingListener struct {
	net.Listener
	accepts int32
}

func (c *countingListener) Accept() (net.Conn, error) {
	conn, err := c.Listener.Accept()
	if err == nil {
		atomic.AddInt32(&c.accepts, 1)
	}
	return conn, err
}

// TestNew_RejectsNonGetHead_NeverDialsUpstream verifies a rejected write
// never causes upstream to be dialed at the TCP level at all. This is a
// stronger, more direct signal than TestNew_RejectsNonGetHead's "upstream
// handler never ran + no Authorization" check, since it catches a dial
// attempt even if the connection were later dropped or its response
// discarded before reaching the client.
func TestNew_RejectsNonGetHead_NeverDialsUpstream(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	cl := &countingListener{Listener: inner}

	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	upstream.Listener.Close()
	upstream.Listener = cl
	upstream.Start()
	defer upstream.Close()

	p, err := New([]Route{{Upstream: upstream.URL, Credential: "s3kr1t"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/crates/foo", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
	if got := atomic.LoadInt32(&cl.accepts); got != 0 {
		t.Errorf("upstream listener accepted %d connections, want 0", got)
	}
}

// TestNew_UnknownAuthSchemeErrors verifies New rejects a route naming an
// AuthScheme it doesn't recognise, rather than silently misrendering the
// credential -- defense in depth, since registryroutes already validates
// scheme names before a route ever reaches here.
func TestNew_UnknownAuthSchemeErrors(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	_, err := New([]Route{{Upstream: upstream.URL, AuthScheme: "made-up-scheme", Credential: "s3kr1t"}})
	if err == nil {
		t.Fatal("New with an unknown AuthScheme = nil error, want error")
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

	p, err := New([]Route{{Upstream: upstream.URL, Credential: "s3kr1t"}})
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

// TestAuthorizationHeaderValue_HonoursAnInlineScheme covers issue #3124: a
// credential that already names its own auth scheme is the whole header
// value, and must not be prefixed with a second one. cargo sends a
// credentials.toml token verbatim as the Authorization header value rather
// than prepending a scheme itself, so registries documenting a cargo setup
// bake the scheme into the token -- Artifactory's own emits
// `token = "Bearer <jwt>"`. A credential naming no scheme keeps the
// pre-#3124 behaviour and is still sent as Bearer.
func TestAuthorizationHeaderValue_HonoursAnInlineScheme(t *testing.T) {
	for _, tc := range []struct {
		name       string
		credential string
		want       string
	}{
		{"bare token gets one Bearer", "s3kr1t", "Bearer s3kr1t"},
		{"Bearer-prefixed is not doubled", "Bearer eyJhbGc", "Bearer eyJhbGc"},
		{"Basic-prefixed passes through", "Basic dXNlcjpwdw==", "Basic dXNlcjpwdw=="},
		{"token-scheme passes through", "token ghp_abc", "token ghp_abc"},
		// The scheme match is case-insensitive on the scheme word only:
		// HTTP auth schemes are case-insensitive per RFC 7235, and a
		// registry's docs may spell it in any case.
		{"lowercase bearer passes through", "bearer eyJhbGc", "bearer eyJhbGc"},
		{"mixed-case Basic passes through", "bAsIc dXNlcjpwdw==", "bAsIc dXNlcjpwdw=="},
		// Only a genuine scheme *prefix* counts. A token that merely
		// contains a scheme word, or that starts with one without the
		// delimiting space, is an ordinary opaque credential.
		{"scheme word later in value is not a prefix", "abc Bearer def", "Bearer abc Bearer def"},
		{"scheme word with no space is not a scheme", "Bearertoken", "Bearer Bearertoken"},
		{"scheme word alone is not a scheme", "Bearer", "Bearer Bearer"},
		{"scheme with empty remainder is not a scheme", "Bearer ", "Bearer Bearer "},
		{"unrecognised scheme is still prefixed", "Negotiate abc", "Bearer Negotiate abc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := authorizationHeaderValue(tc.credential); got != tc.want {
				t.Errorf("authorizationHeaderValue(%q) = %q, want %q", tc.credential, got, tc.want)
			}
		})
	}
}

// TestNew_CredentialWithInlineSchemeIsNotDoublePrefixed is the end-to-end
// regression for issue #3124: before the fix a `Bearer `-prefixed credential
// reached upstream as "Bearer Bearer <jwt>", which Artifactory rejected with
// a 401 naming a token type it could not resolve.
func TestNew_CredentialWithInlineSchemeIsNotDoublePrefixed(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New([]Route{{Upstream: upstream.URL, Credential: "Bearer eyJhbGciOiJSUzI1NiJ9"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/config.json", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if want := "Bearer eyJhbGciOiJSUzI1NiJ9"; gotAuth != want {
		t.Errorf("upstream got Authorization %q, want %q (credential must not be double-prefixed)", gotAuth, want)
	}
}

// TestNew_BasicAuthScheme verifies AuthScheme "basic" attaches Authorization
// as HTTP Basic: a plain "user:password" credential is base64-encoded, while
// a credential already naming its own "Basic " scheme passes through
// verbatim -- the same genuine-prefix rule authorizationHeaderValue applies
// for bearer (issue #3139 slice 2).
func TestNew_BasicAuthScheme(t *testing.T) {
	for _, tc := range []struct {
		name       string
		credential string
		want       string
	}{
		{"plain user:password is base64-encoded", "alice:hunter2", "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:hunter2"))},
		{"already-Basic credential passes through verbatim", "Basic dXNlcjpwdw==", "Basic dXNlcjpwdw=="},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotAuth string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth = r.Header.Get("Authorization")
				w.WriteHeader(http.StatusOK)
			}))
			defer upstream.Close()

			p, err := New([]Route{{Upstream: upstream.URL, AuthScheme: "basic", Credential: tc.credential}})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/config.json", nil)
			p.ServeHTTP(rr, req)

			if gotAuth != tc.want {
				t.Errorf("upstream got Authorization %q, want %q", gotAuth, tc.want)
			}
		})
	}
}

// TestNew_BasicCredentialReachesUpstreamUnchanged proves the inline-scheme
// pass-through also gives the proxy HTTP Basic support, which it had no way
// to express before issue #3124.
func TestNew_BasicCredentialReachesUpstreamUnchanged(t *testing.T) {
	var gotUser, gotPass string
	var gotOK bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, gotOK = r.BasicAuth()
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New([]Route{{Upstream: upstream.URL, Credential: "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:hunter2"))}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/config.json", nil)
	p.ServeHTTP(rr, req)

	if !gotOK {
		t.Fatalf("upstream could not parse the request as HTTP Basic auth")
	}
	if gotUser != "alice" || gotPass != "hunter2" {
		t.Errorf("upstream got Basic auth %q/%q, want %q/%q", gotUser, gotPass, "alice", "hunter2")
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

	p, err := New([]Route{{Upstream: upstream.URL, Credential: "s3kr1t"}})
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

	p, err := New([]Route{{Upstream: upstream.URL, Credential: "s3kr1t"}})
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

// TestNew_HeaderAuthScheme verifies AuthScheme "header:<Name>" attaches
// credential verbatim to the named header instead of Authorization -- the
// JFrog X-JFrog-Art-Api pattern (issue #3139 slice 2, ADR 0045).
func TestNew_HeaderAuthScheme(t *testing.T) {
	var gotNamed, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotNamed = r.Header.Get("X-JFrog-Art-Api")
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New([]Route{{Upstream: upstream.URL, AuthScheme: "header:X-JFrog-Art-Api", Credential: "s3kr1t-api-key"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/config.json", nil)
	p.ServeHTTP(rr, req)

	if gotNamed != "s3kr1t-api-key" {
		t.Errorf("upstream got X-JFrog-Art-Api %q, want %q", gotNamed, "s3kr1t-api-key")
	}
	if gotAuth != "" {
		t.Errorf("upstream got Authorization %q, want none (credential must go to the named header only)", gotAuth)
	}
}

// TestNew_EmptyCredentialSkipsHeaderRegardlessOfScheme verifies an empty
// credential attaches no header at all, whatever AuthScheme names -- the
// unauthenticated pass-through policy holds for every scheme, not just the
// bearer default.
func TestNew_EmptyCredentialSkipsHeaderRegardlessOfScheme(t *testing.T) {
	for _, scheme := range []string{"", "bearer", "basic", "header:X-JFrog-Art-Api"} {
		t.Run(scheme, func(t *testing.T) {
			headers := http.Header{}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				headers = r.Header.Clone()
				w.WriteHeader(http.StatusOK)
			}))
			defer upstream.Close()

			p, err := New([]Route{{Upstream: upstream.URL, AuthScheme: scheme, Credential: ""}})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/config.json", nil)
			p.ServeHTTP(rr, req)

			if got := headers.Get("Authorization"); got != "" {
				t.Errorf("upstream got Authorization %q, want none", got)
			}
			if got := headers.Get("X-JFrog-Art-Api"); got != "" {
				t.Errorf("upstream got X-JFrog-Art-Api %q, want none", got)
			}
		})
	}
}

// TestNew_EmptyCredentialAttachesNoAuthorizationHeader verifies the existing
// unauthenticated pass-through policy is unchanged when credential is empty.
func TestNew_EmptyCredentialAttachesNoAuthorizationHeader(t *testing.T) {
	var gotAuth string
	var sawHeader bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, sawHeader = r.Header.Get("Authorization"), r.Header.Get("Authorization") != ""
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New([]Route{{Upstream: upstream.URL, Credential: ""}})
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

	p, err := New([]Route{{Upstream: upstream.URL, Credential: "s3kr1t"}})
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

	p, err := New([]Route{{Upstream: upstream.URL, Credential: ""}})
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

	p, err := New([]Route{{Upstream: upstream.URL, Credential: credential}})
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

	p, err := New([]Route{{Upstream: upstream.URL, Credential: ""}})
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

// TestNew_LogsPathOutsideAllowlist verifies a request whose path falls
// outside the derived allowlist produces a distinguishable log line naming
// the method and path.
func TestNew_LogsPathOutsideAllowlist(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New([]Route{{Upstream: upstream.URL, Credential: ""}})
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

// TestNew_NoLogForAllowlistedPath verifies a request whose path falls inside
// the derived allowlist produces no "outside allowlist" log line.
func TestNew_NoLogForAllowlistedPath(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New([]Route{{Upstream: upstream.URL, Credential: ""}})
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

// TestSunPathCap verifies sunPathCap returns the AF_UNIX sun_path byte cap
// for each platform (issue #3077): 104 on darwin, and 108 for anything else,
// checked against both "linux" and a made-up GOOS to confirm the 108 branch
// is a genuine default case rather than a "linux"-specific match.
func TestSunPathCap(t *testing.T) {
	cases := []struct {
		goos string
		want int
	}{
		{goos: "darwin", want: 104},
		{goos: "linux", want: 108},
		{goos: "made-up-os", want: 108},
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			if got := sunPathCap(tc.goos); got != tc.want {
				t.Errorf("sunPathCap(%q) = %d, want %d", tc.goos, got, tc.want)
			}
		})
	}
}

// TestTooLongForUnixSocket_Boundary verifies TooLongForUnixSocket's off-by-one
// boundary: a path one byte under the running platform's sun_path cap fits,
// while a path exactly at the cap does not (issue #3077) -- the kernel needs
// the last byte for its own NUL terminator.
func TestTooLongForUnixSocket_Boundary(t *testing.T) {
	sunPathLimit := sunPathCap(runtime.GOOS)

	fits := strings.Repeat("a", sunPathLimit-1)
	if TooLongForUnixSocket(fits) {
		t.Errorf("TooLongForUnixSocket(path of length %d) = true, want false (cap is %d)", len(fits), sunPathLimit)
	}

	tooLong := strings.Repeat("a", sunPathLimit)
	if !TooLongForUnixSocket(tooLong) {
		t.Errorf("TooLongForUnixSocket(path of length %d) = false, want true (cap is %d)", len(tooLong), sunPathLimit)
	}
}

// TestServe_PathTooLong verifies ListenAndServe rejects a socket
// path at or over the platform's sun_path cap with an error naming the
// actual byte length and the numeric cap, and does so via the preflight
// check rather than an underlying net.Listen failure -- confirmed by p.
// listener staying nil (issue #3077).
func TestServe_PathTooLong(t *testing.T) {
	sunPathLimit := sunPathCap(runtime.GOOS)

	dir := t.TempDir()
	// Pad the final path component so the full path lands exactly at
	// sunPathLimit bytes, regardless of how long t.TempDir()'s own base path
	// happens to be.
	padLen := sunPathLimit - len(dir) - len(string(filepath.Separator))
	if padLen < 1 {
		t.Fatalf("t.TempDir() path %q already too close to cap %d to pad meaningfully", dir, sunPathLimit)
	}
	socketPath := filepath.Join(dir, strings.Repeat("a", padLen))
	if len(socketPath) != sunPathLimit {
		t.Fatalf("constructed socketPath length = %d, want exactly %d", len(socketPath), sunPathLimit)
	}

	p := &Proxy{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})}
	err := p.ListenAndServe(socketPath)
	if err == nil {
		t.Fatalf("ListenAndServe(%d-byte path) = nil error, want error", len(socketPath))
	}
	if !strings.Contains(err.Error(), strconv.Itoa(len(socketPath))) {
		t.Errorf("error %q does not contain the actual byte length %d", err.Error(), len(socketPath))
	}
	if !strings.Contains(err.Error(), strconv.Itoa(sunPathLimit)) {
		t.Errorf("error %q does not contain the platform cap %d", err.Error(), sunPathLimit)
	}
	if p.listener != nil {
		t.Errorf("p.listener = %v, want nil (preflight check must reject before net.Listen)", p.listener)
	}
}

// TestNew_SuppressesRepeatedMissesWhenAllowlistNeverMatches verifies that
// when a deployment's paths never land inside the derived allowlist (the
// path-prefixed, Artifactory-style shape -- issue #3087), only the first
// out-of-allowlist request logs the detailed miss line; later misses are
// counted instead of logged individually, and the count is flushed via
// Close once the run ends without ever matching.
func TestNew_SuppressesRepeatedMissesWhenAllowlistNeverMatches(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New([]Route{{Upstream: upstream.URL, Credential: ""}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	paths := []string{
		"/artifactory/api/cargo/crates.io/api/v1/crates/foo/1.0.0/download",
		"/artifactory/api/npm/npm-remote/foo",
		"/artifactory/api/pypi/pypi-remote/simple/foo/",
	}
	for _, path := range paths {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		p.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}
	}

	logged := logBuf.String()
	if got := strings.Count(logged, "registryproxy: path outside derived allowlist:"); got != 1 {
		t.Errorf("detailed miss log appeared %d times, want exactly 1: %q", got, logged)
	}

	closer, ok := p.(interface{ Close() })
	if !ok {
		t.Fatalf("handler returned by New does not implement Close()")
	}
	closer.Close()

	logged = logBuf.String()
	want := "registryproxy: suppressed 2 further requests outside derived allowlist"
	if !strings.Contains(logged, want) {
		t.Errorf("log output = %q, want it to contain %q", logged, want)
	}
}

// TestNew_ProxyCloseFlushesSuppressedMisses verifies the flush happens
// through the production Close path: the *Proxy wrapper returned to
// callers, not the handler's own Close() (which no production caller
// invokes directly -- see cmd/launcher/internal/dispatch/box.go's
// `defer proxy.Close()`, issue #3087 review finding).
func TestNew_ProxyCloseFlushesSuppressedMisses(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New([]Route{{Upstream: upstream.URL, Credential: ""}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	proxy := &Proxy{Handler: p}

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	paths := []string{
		"/artifactory/api/cargo/crates.io/api/v1/crates/foo/1.0.0/download",
		"/artifactory/api/npm/npm-remote/foo",
		"/artifactory/api/pypi/pypi-remote/simple/foo/",
	}
	for _, path := range paths {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		proxy.Handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}
	}

	if err := proxy.Close(); err != nil {
		t.Fatalf("proxy.Close() = %v, want nil (listener was never started)", err)
	}

	logged := logBuf.String()
	want := "registryproxy: suppressed 2 further requests outside derived allowlist"
	if !strings.Contains(logged, want) {
		t.Errorf("log output = %q, want it to contain %q", logged, want)
	}
}

// TestNew_FlushesSuppressedMissesAsSoonAsAllowlistMatches verifies the flush
// happens the moment a request first matches the allowlist -- not merely on
// Close -- and that per-request miss logging resumes for any miss after
// that point (issue #3087).
func TestNew_FlushesSuppressedMissesAsSoonAsAllowlistMatches(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New([]Route{{Upstream: upstream.URL, Credential: ""}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	misses := []string{
		"/artifactory/api/cargo/crates.io/api/v1/crates/foo/1.0.0/download",
		"/artifactory/api/npm/npm-remote/foo",
		"/artifactory/api/pypi/pypi-remote/simple/foo/",
	}
	for _, path := range misses {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		p.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}
	}

	allowed := httptest.NewRequest(http.MethodGet, "/config.json", nil)
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, allowed)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	logged := logBuf.String()
	want := "registryproxy: suppressed 2 further requests outside derived allowlist"
	if !strings.Contains(logged, want) {
		t.Errorf("log output immediately after the matching request = %q, want it to already contain %q", logged, want)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/crates/baz/3.0.0/download", nil)
	rr = httptest.NewRecorder()
	p.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	logged = logBuf.String()
	if got := strings.Count(logged, "registryproxy: path outside derived allowlist:"); got != 2 {
		t.Errorf("detailed miss log appeared %d times, want exactly 2 (one pre-match, one post-match): %q", got, logged)
	}
}

// TestNew_ConcurrentRequestsNoRace drives a mix of allowlist-hit and
// allowlist-miss requests through the handler from many goroutines at once,
// to exercise the mutex guarding the shared miss-tracking state (round-1
// review's data race finding). Run with -race; exact suppressed-miss counts
// are inherently non-deterministic under concurrent ordering, so this only
// asserts the server completes cleanly without a panic, deadlock, or race.
func TestNew_ConcurrentRequestsNoRace(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New([]Route{{Upstream: upstream.URL, Credential: ""}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	paths := []string{
		"/config.json",
		"/v2/foo/manifests/latest",
		"/artifactory/api/npm/npm-remote/foo",
		"/api/v1/crates/foo/1.0.0/download",
	}

	const goroutines = 20
	const requestsPerGoroutine = 25

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < requestsPerGoroutine; j++ {
				path := paths[(i+j)%len(paths)]
				req := httptest.NewRequest(http.MethodGet, path, nil)
				rr := httptest.NewRecorder()
				p.ServeHTTP(rr, req)
				if rr.Code != http.StatusOK {
					t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
				}
			}
		}(i)
	}
	wg.Wait()

	if c, ok := p.(closer); ok {
		c.Close()
	}
}

// TestNew_LogsEachMissAfterAllowlistHasMatched verifies that once some
// request has matched the derived allowlist (proving the deployment is
// root-served), per-request miss logging is unchanged: every later
// out-of-allowlist request logs its own detailed line, and the suppression
// path introduced for the never-matched (path-prefixed) shape never
// triggers, since everMatched was already true before either miss.
func TestNew_LogsEachMissAfterAllowlistHasMatched(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New([]Route{{Upstream: upstream.URL, Credential: ""}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	allowed := httptest.NewRequest(http.MethodGet, "/config.json", nil)
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, allowed)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	misses := []string{
		"/api/v1/crates/foo/1.0.0/download",
		"/api/v1/crates/bar/2.0.0/download",
	}
	for _, path := range misses {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		p.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}
	}

	logged := logBuf.String()
	if got := strings.Count(logged, "registryproxy: path outside derived allowlist:"); got != 2 {
		t.Errorf("detailed miss log appeared %d times, want exactly 2: %q", got, logged)
	}
	if strings.Contains(logged, "registryproxy: suppressed") {
		t.Errorf("log output = %q, want no suppression line once the allowlist has matched", logged)
	}
}

// TestListenAndServeTCP_RejectsMissingOrWrongSecret_NeverDialsUpstream verifies
// that a TCP request lacking the correct TCPSecretHeader is rejected before
// ever reaching the GET/HEAD gate or dialing upstream -- mirroring
// TestNew_RejectsNonGetHead_NeverDialsUpstream's countingListener technique, but
// for the secret gate that only the TCP transport needs (issue #3111): a unix
// socket's own filesystem permissions are its equivalent gate, so
// ListenAndServe has no such check.
func TestListenAndServeTCP_RejectsMissingOrWrongSecret_NeverDialsUpstream(t *testing.T) {
	const secret = "s3kr1t-tcp-secret"

	cases := []struct {
		name   string
		method string
		header string
	}{
		{name: "missing secret", method: http.MethodGet, header: ""},
		{name: "wrong secret", method: http.MethodGet, header: "not-the-secret"},
		{name: "wrong method and missing secret", method: http.MethodPost, header: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("net.Listen: %v", err)
			}
			cl := &countingListener{Listener: inner}

			upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			upstream.Listener.Close()
			upstream.Listener = cl
			upstream.Start()
			defer upstream.Close()

			handler, err := New([]Route{{Upstream: upstream.URL, Credential: "real-credential"}})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			p := &Proxy{Handler: handler}
			if err := p.ListenAndServeTCP("127.0.0.1:0", secret); err != nil {
				t.Fatalf("ListenAndServeTCP: %v", err)
			}
			defer p.Close()

			req, err := http.NewRequest(tc.method, "http://"+p.Addr().String()+"/crates/foo", nil)
			if err != nil {
				t.Fatalf("http.NewRequest: %v", err)
			}
			if tc.header != "" {
				req.Header.Set(TCPSecretHeader, tc.header)
			}

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("http.DefaultClient.Do: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
			}
			if got := atomic.LoadInt32(&cl.accepts); got != 0 {
				t.Errorf("upstream listener accepted %d connections, want 0", got)
			}
		})
	}
}

// TestListenAndServeTCP_CorrectSecretForwardsToUpstream verifies that a GET
// request carrying the correct TCPSecretHeader passes the secret gate and
// reaches upstream, confirming the gate doesn't break the happy path.
func TestListenAndServeTCP_CorrectSecretForwardsToUpstream(t *testing.T) {
	const secret = "s3kr1t-tcp-secret"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("via tcp"))
	}))
	defer upstream.Close()

	handler, err := New([]Route{{Upstream: upstream.URL, Credential: ""}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	p := &Proxy{Handler: handler}
	if err := p.ListenAndServeTCP("127.0.0.1:0", secret); err != nil {
		t.Fatalf("ListenAndServeTCP: %v", err)
	}
	defer p.Close()

	req, err := http.NewRequest(http.MethodGet, "http://"+p.Addr().String()+"/crates/foo", nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	req.Header.Set(TCPSecretHeader, secret)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http.DefaultClient.Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(body) != "via tcp" {
		t.Errorf("body = %q, want %q", string(body), "via tcp")
	}
}

// TestListenAndServeTCP_AttachesCredentialUpstreamNeverLeaksToClient mirrors
// TestNew_AttachesCredentialToOutboundRequest but drives the request over
// the real TCP transport (ListenAndServeTCP) rather than calling ServeHTTP
// directly, proving the credential-isolation guarantee -- the proxy still
// attaches the configured credential to the upstream leg, but it never
// crosses back to whatever is on the other end of the TCP socket (the Box,
// per issue #3111's acceptance criterion) -- holds on both transports, not
// just the unix-socket one.
func TestListenAndServeTCP_AttachesCredentialUpstreamNeverLeaksToClient(t *testing.T) {
	const secret = "s3kr1t-tcp-secret"
	const credential = "real-upstream-registry-credential"

	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("X-Test", "yes")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("via tcp"))
	}))
	defer upstream.Close()

	handler, err := New([]Route{{Upstream: upstream.URL, Credential: credential}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	p := &Proxy{Handler: handler}
	if err := p.ListenAndServeTCP("127.0.0.1:0", secret); err != nil {
		t.Fatalf("ListenAndServeTCP: %v", err)
	}
	defer p.Close()

	req, err := http.NewRequest(http.MethodGet, "http://"+p.Addr().String()+"/crates/foo", nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	req.Header.Set(TCPSecretHeader, secret)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http.DefaultClient.Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if want := "Bearer " + credential; gotAuth != want {
		t.Errorf("upstream got Authorization %q, want %q (credential must still reach upstream over TCP)", gotAuth, want)
	}

	// The credential must never ride back to the client: not in a response
	// header (including under its own name, in case a future change echoes
	// it back), and not in the body.
	if got := resp.Header.Get("Authorization"); got != "" {
		t.Errorf("client-visible response carried Authorization %q, want none (credential leaked to client)", got)
	}
	for name, values := range resp.Header {
		for _, v := range values {
			if strings.Contains(v, credential) {
				t.Errorf("response header %q = %q contained the credential (credential leaked to client)", name, v)
			}
		}
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if strings.Contains(string(body), credential) {
		t.Errorf("response body %q contained the credential (credential leaked to client)", string(body))
	}
	if string(body) != "via tcp" {
		t.Errorf("body = %q, want %q", string(body), "via tcp")
	}
}

// TestListenAndServeTCP_RejectsEmptySecret_NeverListens verifies that
// ListenAndServeTCP refuses to start at all when handed an empty secret,
// rather than binding a listener whose gate then accepts every request
// carrying no TCPSecretHeader (an empty header value equals an empty
// secret) -- fail closed rather than fall open (issue #3111).
func TestListenAndServeTCP_RejectsEmptySecret_NeverListens(t *testing.T) {
	handler, err := New([]Route{{Upstream: "http://127.0.0.1:1", Credential: ""}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	p := &Proxy{Handler: handler}
	if err := p.ListenAndServeTCP("127.0.0.1:0", ""); err == nil {
		t.Fatal("ListenAndServeTCP with empty secret = nil error, want non-nil")
	}
	if addr := p.Addr(); addr != nil {
		t.Errorf("Addr() = %v after rejected empty secret, want nil (no listener established)", addr)
	}
}

// TestListenAndServeTCP_CorrectSecretStillRejectsNonGetHead_NeverDialsUpstream
// verifies that a correct-secret request is still subject to the existing
// GET/HEAD gate: the secret gate runs in front of, not instead of, the
// handler's own method check, and a rejected write still never dials
// upstream.
func TestListenAndServeTCP_CorrectSecretStillRejectsNonGetHead_NeverDialsUpstream(t *testing.T) {
	const secret = "s3kr1t-tcp-secret"

	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	cl := &countingListener{Listener: inner}

	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	upstream.Listener.Close()
	upstream.Listener = cl
	upstream.Start()
	defer upstream.Close()

	handler, err := New([]Route{{Upstream: upstream.URL, Credential: "s3kr1t"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	p := &Proxy{Handler: handler}
	if err := p.ListenAndServeTCP("127.0.0.1:0", secret); err != nil {
		t.Fatalf("ListenAndServeTCP: %v", err)
	}
	defer p.Close()

	req, err := http.NewRequest(http.MethodPost, "http://"+p.Addr().String()+"/crates/foo", nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	req.Header.Set(TCPSecretHeader, secret)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http.DefaultClient.Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}
	if got := atomic.LoadInt32(&cl.accepts); got != 0 {
		t.Errorf("upstream listener accepted %d connections, want 0", got)
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

	p, err := New([]Route{{Upstream: upstream.URL, Credential: credential}})
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
