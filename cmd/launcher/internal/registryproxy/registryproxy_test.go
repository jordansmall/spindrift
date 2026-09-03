// Package registryproxy implements a credential-free, GET/HEAD-only
// pass-through reverse proxy served over a unix domain socket (issue #2849).
package registryproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
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

	p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: ""}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/crates/foo", nil)
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

	p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: ""}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "/r0/crates/foo", nil)
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

			p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: ""}}))
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			target := "/r0/crates/foo"
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

			p, err := New(AssignPrefixes([]Route{{Upstream: upstreamURL, Credential: ""}}))
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			target := "/r0/crates/foo"
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

	p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: ""}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/crates/foo", nil)
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

	handler, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: ""}}))
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

	resp, err := client.Get("http://unix/r0/anything")
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
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/r0/anything", nil)
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

	handler, err := New(AssignPrefixes([]Route{{Upstream: "http://127.0.0.1:0", Credential: ""}}))
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
			if _, err := New(AssignPrefixes([]Route{{Upstream: upstream, Credential: ""}})); err == nil {
				t.Errorf("New(%q) = nil error, want error", upstream)
			}
		})
	}
}

// TestNew_RoutesByPathPrefix verifies a multi-route table dispatches each
// request by the first segment of its path, replacing the Host-header
// selection this superseded (issue #3142): a request under each route's own
// prefix is stripped of that segment and reaches that route's own upstream
// carrying that route's own credential rendered per its own AuthScheme --
// never the other route's path or credential, proven by each upstream
// failing the test outright if it ever observes the other's.
func TestNew_RoutesByPathPrefix(t *testing.T) {
	var gotPathA, gotAuthA string
	upstreamA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPathA, gotAuthA = r.URL.Path, r.Header.Get("Authorization")
		if got := r.Header.Get("X-JFrog-Art-Api"); got != "" {
			t.Errorf("upstream A got X-JFrog-Art-Api %q, want none (route B's credential must never cross to route A)", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("from A"))
	}))
	defer upstreamA.Close()
	var gotPathB, gotHeaderB string
	upstreamB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPathB, gotHeaderB = r.URL.Path, r.Header.Get("X-JFrog-Art-Api")
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("upstream B got Authorization %q, want none (route A's credential must never cross to route B)", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("from B"))
	}))
	defer upstreamB.Close()

	routes := AssignPrefixes([]Route{
		{MatchHost: "registry-a.example", Upstream: upstreamA.URL, Credential: "token-a"},
		{MatchHost: "registry-b.example", Upstream: upstreamB.URL, AuthScheme: "header:X-JFrog-Art-Api", Credential: "token-b"},
	})
	p, err := New(routes)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	do := func(path string) (int, string) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		p.ServeHTTP(rr, req)
		body, err := io.ReadAll(rr.Body)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		return rr.Code, string(body)
	}

	if code, body := do("/" + routes[0].Prefix + "/pkg"); code != http.StatusOK || body != "from A" {
		t.Fatalf("request under route A's prefix: status=%d body=%q, want %d %q", code, body, http.StatusOK, "from A")
	}
	if gotPathA != "/pkg" {
		t.Errorf("upstream A got path %q, want /pkg (prefix must be stripped)", gotPathA)
	}
	if want := "Bearer token-a"; gotAuthA != want {
		t.Errorf("upstream A got Authorization %q, want %q", gotAuthA, want)
	}

	if code, body := do("/" + routes[1].Prefix + "/pkg"); code != http.StatusOK || body != "from B" {
		t.Fatalf("request under route B's prefix: status=%d body=%q, want %d %q", code, body, http.StatusOK, "from B")
	}
	if gotPathB != "/pkg" {
		t.Errorf("upstream B got path %q, want /pkg (prefix must be stripped)", gotPathB)
	}
	if want := "token-b"; gotHeaderB != want {
		t.Errorf("upstream B got X-JFrog-Art-Api %q, want %q", gotHeaderB, want)
	}
}

// TestNew_UnknownPrefixReturns404WithoutDialingUpstream verifies a request
// whose first path segment names no configured route's Prefix is refused
// with 404, and never dials any upstream at all -- the refusal happens in
// front of ReverseProxy entirely (issue #3142), proven here the same way
// TestNew_RejectsNonGetHead_NeverDialsUpstream proves it for the method
// gate: the upstream's own listener never accepts a connection, and its
// handler (which would fail the test if reached) never runs.
func TestNew_UnknownPrefixReturns404WithoutDialingUpstream(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	cl := &countingListener{Listener: inner}

	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("upstream got request for %q, want no upstream dial for an unknown route prefix", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	upstream.Listener.Close()
	upstream.Listener = cl
	upstream.Start()
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{Upstream: upstream.URL}})
	p, err := New(routes)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/not-"+routes[0].Prefix+"/pkg", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
	if got := atomic.LoadInt32(&cl.accepts); got != 0 {
		t.Errorf("upstream listener accepted %d connections, want 0", got)
	}
}

// TestNew_RootAndEmptySegmentPathsReturn404WithoutDialingUpstream verifies
// the bare root path and a path with a leading empty segment (a path that,
// after its opening "/", starts with another "/") both name no route prefix
// and are refused with 404 without dialing upstream, the same as any other
// unknown-prefix path (issue #3142).
func TestNew_RootAndEmptySegmentPathsReturn404WithoutDialingUpstream(t *testing.T) {
	for _, path := range []string{"/", "//pkg"} {
		t.Run(path, func(t *testing.T) {
			inner, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("net.Listen: %v", err)
			}
			cl := &countingListener{Listener: inner}

			upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("upstream got request for %q, want no upstream dial for path %q", r.URL.Path, path)
				w.WriteHeader(http.StatusOK)
			}))
			upstream.Listener.Close()
			upstream.Listener = cl
			upstream.Start()
			defer upstream.Close()

			p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL}}))
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			p.ServeHTTP(rr, req)

			if rr.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
			}
			if got := atomic.LoadInt32(&cl.accepts); got != 0 {
				t.Errorf("upstream listener accepted %d connections, want 0", got)
			}
		})
	}
}

// TestNew_BasePathUpstreamJoin verifies an upstream URL that itself carries
// a base path (e.g. "https://host/artifactory") is joined with the
// prefix-stripped remainder without a doubled slash or doubled segment, and
// that a request naming exactly "/<prefix>" (no trailing slash) forwards to
// that base path verbatim (issue #3142).
func TestNew_BasePathUpstreamJoin(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{Upstream: upstream.URL + "/artifactory"}})
	p, err := New(routes)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	prefix := routes[0].Prefix

	cases := []struct {
		name string
		path string
		want string
	}{
		{"remainder joins onto the base path with no doubled slash", "/" + prefix + "/api/npm/foo", "/artifactory/api/npm/foo"},
		{"exact prefix with nothing after it forwards the base path verbatim", "/" + prefix, "/artifactory"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotPath = ""
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			p.ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
			}
			if gotPath != tc.want {
				t.Errorf("upstream got path %q, want %q", gotPath, tc.want)
			}
		})
	}
}

// TestNew_EscapedRemainderPreserved verifies a percent-escaped slash in the
// remainder after the prefix -- npm's "%2f" separating a scoped package's
// "@scope" and name in some client requests -- reaches upstream still
// escaped, not decoded into a literal '/' that would otherwise be
// misread as an extra path segment (issue #3142).
func TestNew_EscapedRemainderPreserved(t *testing.T) {
	var gotRawPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawPath = r.URL.EscapedPath()
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{Upstream: upstream.URL}})
	p, err := New(routes)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+routes[0].Prefix+"/@types%2fnode", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if want := "/@types%2fnode"; gotRawPath != want {
		t.Errorf("upstream got escaped path %q, want %q (%%2f must not decode into a literal slash)", gotRawPath, want)
	}
}

// TestNew_AllowlistChecksStrippedPath verifies the allowlist-miss log check
// runs against the path with the route-selecting prefix already stripped,
// not the raw inbound path: a path that only matches an allowlisted shape
// once its prefix is removed (here cargo's "/config.json") must not log an
// allowlist miss, and a request whose raw inbound path happens to look
// allowlisted only because of a prefix collision must not be treated as a
// false match either (issue #3142).
func TestNew_AllowlistChecksStrippedPath(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{MatchHost: "registry.example", Upstream: upstream.URL}})
	p, err := New(routes)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	prefix := routes[0].Prefix

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	// The raw inbound path ("/registry-example/config.json") does not
	// itself match any allowlist pattern (it has two segments, not cargo's
	// single "/config.json"), but the path stripped of its prefix does --
	// only the strip makes it match.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/config.json", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if strings.Contains(logBuf.String(), "registryproxy: path outside derived allowlist:") {
		t.Errorf("log output = %q, want no allowlist-miss line (the stripped path /config.json is allowlisted)", logBuf.String())
	}
}

// TestNew_SingleRouteTableBackCompat verifies a single-route table -- the
// shape a scalar-knob-bridge or single-route TOML routes file builds --
// still works end-to-end once its one route also requires a Prefix (issue
// #3142 slice 2's back-compat acceptance criterion): a request under that
// route's own prefix is forwarded to its upstream.
func TestNew_SingleRouteTableBackCompat(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("single route"))
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: "s3kr1t"}})
	p, err := New(routes)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+routes[0].Prefix+"/crates/foo", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body, err := io.ReadAll(rr.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(body) != "single route" {
		t.Errorf("body = %q, want %q", string(body), "single route")
	}
}

// TestAssignPrefixes_BracketedIPv6MatchHost verifies a MatchHost of "[::1]"
// (no port) still derives a valid Prefix: hostOnly strips the brackets
// before slugify runs, so a bracketed literal IPv6 MatchHost slugifies the
// same as its bracket-free form would. This is the AssignPrefixes-side
// replacement for what a Host-header-selection test covered before prefix
// routing replaced it (issue #3142) -- hostOnly's own bracket-stripping is
// otherwise only reachable through here now.
func TestAssignPrefixes_BracketedIPv6MatchHost(t *testing.T) {
	routes := AssignPrefixes([]Route{{MatchHost: "[::1]"}})
	if got, want := routes[0].Prefix, "--1"; got != want {
		t.Errorf("Prefix = %q, want %q", got, want)
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

				p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: credential}}))
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

	p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: "s3kr1t"}}))
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

	_, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, AuthScheme: "made-up-scheme", Credential: "s3kr1t"}}))
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

	p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: "s3kr1t"}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/crates/foo", nil)
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

	p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: "Bearer eyJhbGciOiJSUzI1NiJ9"}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/config.json", nil)
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

			p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, AuthScheme: "basic", Credential: tc.credential}}))
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/r0/config.json", nil)
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

	p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:hunter2"))}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/config.json", nil)
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

	p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: "s3kr1t"}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/crates/foo", nil)
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

	p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: "s3kr1t"}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", upstream.URL, err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/crates/foo", nil)
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

	p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, AuthScheme: "header:X-JFrog-Art-Api", Credential: "s3kr1t-api-key"}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/config.json", nil)
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

			p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, AuthScheme: scheme, Credential: ""}}))
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/r0/config.json", nil)
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

	p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: ""}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/crates/foo", nil)
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

	p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: "s3kr1t"}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/crates/foo", nil)
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

	p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: ""}}))
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
	req := httptest.NewRequest(http.MethodGet, "/r0/crates/foo", nil)
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

	p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: credential}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/crates/foo", nil)
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

	p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: ""}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/api/v1/crates/foo/1.0.0/download", nil)
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

	p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: ""}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/api/v1/crates/foo/1.0.0/download", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	logged := logBuf.String()
	if !strings.Contains(logged, "registryproxy: r0: path outside derived allowlist:") {
		t.Errorf("log output = %q, want it to contain the distinguishing marker naming route r0", logged)
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

	p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: ""}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/config.json", nil)
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

	p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: ""}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	paths := []string{
		"/r0/artifactory/api/cargo/crates.io/api/v1/crates/foo/1.0.0/download",
		"/r0/artifactory/api/npm/npm-remote/foo",
		"/r0/artifactory/api/pypi/pypi-remote/simple/foo/",
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
	if got := strings.Count(logged, "registryproxy: r0: path outside derived allowlist:"); got != 1 {
		t.Errorf("detailed miss log appeared %d times, want exactly 1: %q", got, logged)
	}

	closer, ok := p.(interface{ Close() })
	if !ok {
		t.Fatalf("handler returned by New does not implement Close()")
	}
	closer.Close()

	logged = logBuf.String()
	want := "registryproxy: r0: suppressed 2 further distinct paths outside derived allowlist"
	if !strings.Contains(logged, want) {
		t.Errorf("log output = %q, want it to contain %q", logged, want)
	}
}

// TestNew_SuppressedMissSummaryCountsDistinctPathsNotRequests verifies that
// repeated misses against the same (non-first) path summarise as one
// distinct path, not once per request -- a build looping over one
// un-allowlisted path must not report as though it hit hundreds of distinct
// ones (issue #3176).
func TestNew_SuppressedMissSummaryCountsDistinctPathsNotRequests(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: ""}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	// First request (on a distinct path) logs in full and is never counted
	// as a "further" path (issue #3176 review finding). The remaining five
	// all repeat one other path and must collapse into a single suppressed
	// distinct path, not five.
	const firstPath = "/r0/artifactory/api/npm/npm-remote/foo"
	const repeatedPath = "/r0/artifactory/api/pypi/pypi-remote/simple/foo/"
	paths := []string{firstPath}
	for i := 0; i < 5; i++ {
		paths = append(paths, repeatedPath)
	}
	for _, path := range paths {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		p.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}
	}

	closer, ok := p.(interface{ Close() })
	if !ok {
		t.Fatalf("handler returned by New does not implement Close()")
	}
	closer.Close()

	logged := logBuf.String()
	want := "registryproxy: r0: suppressed 1 further distinct path outside derived allowlist"
	if !strings.Contains(logged, want) {
		t.Errorf("log output = %q, want it to contain %q", logged, want)
	}
}

// TestNew_SuppressedMissSummaryCountsEachDistinctPathOnce verifies that
// several different missed paths summarise as that many distinct paths, and
// that repeating one of them does not inflate the count (issue #3176).
func TestNew_SuppressedMissSummaryCountsEachDistinctPathOnce(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: ""}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	// First request logs in full and is excluded from the suppressed set.
	// Of the remaining three, two are distinct paths and one repeats a path
	// already suppressed, so the summary must report 2, not 3.
	paths := []string{
		"/r0/artifactory/api/npm/npm-remote/foo",
		"/r0/artifactory/api/pypi/pypi-remote/simple/foo/",
		"/r0/artifactory/api/pypi/pypi-remote/simple/foo/",
		"/r0/artifactory/api/cargo/crates.io/api/v1/crates/foo/1.0.0/download",
	}
	for _, path := range paths {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		p.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}
	}

	closer, ok := p.(interface{ Close() })
	if !ok {
		t.Fatalf("handler returned by New does not implement Close()")
	}
	closer.Close()

	logged := logBuf.String()
	want := "registryproxy: r0: suppressed 2 further distinct paths outside derived allowlist"
	if !strings.Contains(logged, want) {
		t.Errorf("log output = %q, want it to contain %q", logged, want)
	}
}

// TestNew_SuppressedMissSummaryExcludesTheFirstLoggedMissPath verifies that a
// repeat request on the same path as the route's first (fully logged) miss
// does not also get counted as a "further distinct path" -- that path was
// already reported in full, so re-adding it to the suppressed set inflates
// the summary by one for free (issue #3176 review finding). It also pins the
// summary noun as singular ("1 further distinct path") for that one
// remaining distinct path, not plural ("1 further distinct paths").
func TestNew_SuppressedMissSummaryExcludesTheFirstLoggedMissPath(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: ""}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	// First request (X) logs in full. Second request repeats X -- already
	// reported, must not be re-counted. Third request (Y) is genuinely
	// distinct and is the only one that should land in the summary.
	paths := []string{
		"/r0/artifactory/api/npm/npm-remote/foo",
		"/r0/artifactory/api/npm/npm-remote/foo",
		"/r0/artifactory/api/pypi/pypi-remote/simple/foo/",
	}
	for _, path := range paths {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		p.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}
	}

	closer, ok := p.(interface{ Close() })
	if !ok {
		t.Fatalf("handler returned by New does not implement Close()")
	}
	closer.Close()

	logged := logBuf.String()
	want := "registryproxy: r0: suppressed 1 further distinct path outside derived allowlist"
	if !strings.Contains(logged, want) {
		t.Errorf("log output = %q, want it to contain %q", logged, want)
	}
	if strings.Contains(logged, "1 further distinct paths") {
		t.Errorf("log output = %q, want singular \"path\", not plural \"paths\", for count 1", logged)
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

	p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: ""}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	proxy := &Proxy{Handler: p}

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	paths := []string{
		"/r0/artifactory/api/cargo/crates.io/api/v1/crates/foo/1.0.0/download",
		"/r0/artifactory/api/npm/npm-remote/foo",
		"/r0/artifactory/api/pypi/pypi-remote/simple/foo/",
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
	want := "registryproxy: r0: suppressed 2 further distinct paths outside derived allowlist"
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

	p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: ""}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	misses := []string{
		"/r0/artifactory/api/cargo/crates.io/api/v1/crates/foo/1.0.0/download",
		"/r0/artifactory/api/npm/npm-remote/foo",
		"/r0/artifactory/api/pypi/pypi-remote/simple/foo/",
	}
	for _, path := range misses {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		p.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}
	}

	allowed := httptest.NewRequest(http.MethodGet, "/r0/config.json", nil)
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, allowed)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	logged := logBuf.String()
	want := "registryproxy: r0: suppressed 2 further distinct paths outside derived allowlist"
	if !strings.Contains(logged, want) {
		t.Errorf("log output immediately after the matching request = %q, want it to already contain %q", logged, want)
	}

	req := httptest.NewRequest(http.MethodGet, "/r0/api/v1/crates/baz/3.0.0/download", nil)
	rr = httptest.NewRecorder()
	p.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	logged = logBuf.String()
	if got := strings.Count(logged, "registryproxy: r0: path outside derived allowlist:"); got != 2 {
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

	p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: ""}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	paths := []string{
		"/r0/config.json",
		"/r0/v2/foo/manifests/latest",
		"/r0/artifactory/api/npm/npm-remote/foo",
		"/r0/api/v1/crates/foo/1.0.0/download",
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

// TestNew_ConcurrentRequestsAcrossRoutesNoRace extends the single-route
// concurrency check above to several routes at once (issue #3176): the new
// shared surface is missStates itself, whose per-route entries are allocated
// lazily under h.mu the first time each route is seen, so this drives
// concurrent lookup/allocation/mutation across three distinct route
// prefixes -- including repeats of the very same missed path on one route --
// then, once every request has drained, calls Close to flush. Exact
// suppressed-miss counts are inherently non-deterministic under concurrent
// ordering, so this only asserts every response is relayed OK, with no
// panic, deadlock, or race (run with -race).
func TestNew_ConcurrentRequestsAcrossRoutesNoRace(t *testing.T) {
	const numRoutes = 3
	routes := make([]Route, numRoutes)
	for i := 0; i < numRoutes; i++ {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer upstream.Close()
		routes[i] = Route{MatchHost: fmt.Sprintf("route-%d.example", i), Upstream: upstream.URL}
	}
	assigned := AssignPrefixes(routes)

	p, err := New(assigned)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	var paths []string
	for _, r := range assigned {
		paths = append(paths,
			"/"+r.Prefix+"/config.json",                        // allowlist hit
			"/"+r.Prefix+"/api/v1/crates/foo/1.0.0/download",   // miss
			"/"+r.Prefix+"/api/v1/crates/foo/1.0.0/download",   // same missed path again
			"/"+r.Prefix+"/artifactory/api/npm/npm-remote/foo", // a second, distinct miss
		)
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
					t.Errorf("status = %d, want %d for %s", rr.Code, http.StatusOK, path)
				}
			}
		}(i)
	}
	wg.Wait()

	closer, ok := p.(interface{ Close() })
	if !ok {
		t.Fatalf("handler returned by New does not implement Close()")
	}
	closer.Close()
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

	p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: ""}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	allowed := httptest.NewRequest(http.MethodGet, "/r0/config.json", nil)
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, allowed)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	misses := []string{
		"/r0/api/v1/crates/foo/1.0.0/download",
		"/r0/api/v1/crates/bar/2.0.0/download",
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
	if got := strings.Count(logged, "registryproxy: r0: path outside derived allowlist:"); got != 2 {
		t.Errorf("detailed miss log appeared %d times, want exactly 2: %q", got, logged)
	}
	if strings.Contains(logged, "registryproxy: r0: suppressed") {
		t.Errorf("log output = %q, want no suppression line once the allowlist has matched", logged)
	}
}

// TestNew_SuppressedMissesArePerRoute verifies that allowlist-miss
// suppression state is independent per route (issue #3176): a route whose
// requests never match the allowlist keeps suppressing after its first miss
// regardless of another route's traffic ever matching -- one route's match
// must not flush or un-suppress another route's misses.
func TestNew_SuppressedMissesArePerRoute(t *testing.T) {
	neverMatches := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer neverMatches.Close()
	alwaysMatches := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer alwaysMatches.Close()

	p, err := New(AssignPrefixes([]Route{
		{Upstream: neverMatches.URL, Credential: ""},
		{Upstream: alwaysMatches.URL, Credential: ""},
	}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	// r0's first miss -- logged in full.
	req := httptest.NewRequest(http.MethodGet, "/r0/artifactory/api/npm/npm-remote/foo", nil)
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	// r0's second miss, on a distinct path -- suppressed, so r0 holds a
	// nonzero suppressed set before r1 ever matches. Sequenced first because
	// otherwise r1's match finds nothing of r0's to wrongly flush, and a
	// match branch that flushed every route would pass undetected.
	req = httptest.NewRequest(http.MethodGet, "/r0/artifactory/api/pypi/pypi-remote/simple/foo/", nil)
	rr = httptest.NewRecorder()
	p.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	// r1 matches the allowlist -- must not affect r0's suppression state.
	req = httptest.NewRequest(http.MethodGet, "/r1/config.json", nil)
	rr = httptest.NewRecorder()
	p.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	// Pin: at this instant r0 holds one suppressed path and has not
	// matched, so r0's summary must not have flushed just because r1's did.
	logged := logBuf.String()
	if strings.Contains(logged, "registryproxy: r0: suppressed") {
		t.Errorf("log output = %q, want no r0 summary yet (r1's match must not flush r0's suppressed state)", logged)
	}

	// r0's third miss, on a third distinct path -- still suppressed, since
	// r0 itself has never matched, regardless of r1 having matched.
	req = httptest.NewRequest(http.MethodGet, "/r0/artifactory/api/cargo/crates.io/api/v1/crates/foo/1.0.0/download", nil)
	rr = httptest.NewRecorder()
	p.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	logged = logBuf.String()
	if got := strings.Count(logged, "registryproxy: r0: path outside derived allowlist:"); got != 1 {
		t.Errorf("r0 detailed miss log appeared %d times, want exactly 1: %q", got, logged)
	}

	closer, ok := p.(interface{ Close() })
	if !ok {
		t.Fatalf("handler returned by New does not implement Close()")
	}
	closer.Close()

	logged = logBuf.String()
	want := "registryproxy: r0: suppressed 2 further distinct paths outside derived allowlist"
	if !strings.Contains(logged, want) {
		t.Errorf("log output = %q, want it to contain %q", logged, want)
	}
	if strings.Contains(logged, "registryproxy: r1:") {
		t.Errorf("log output = %q, want no r1 log lines (r1 only ever matched the allowlist)", logged)
	}
}

// TestProxy_CloseFlushesEachRouteIndependently verifies that Proxy.Close
// flushes every route's outstanding suppressed-miss summary, each naming its
// own route, in the route table's own order (issue #3176) -- not just the
// first route with pending state.
func TestProxy_CloseFlushesEachRouteIndependently(t *testing.T) {
	upstreamA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstreamA.Close()
	upstreamB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstreamB.Close()

	p, err := New(AssignPrefixes([]Route{
		{Upstream: upstreamA.URL, Credential: ""},
		{Upstream: upstreamB.URL, Credential: ""},
	}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	proxy := &Proxy{Handler: p}

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	misses := []string{
		"/r0/artifactory/api/npm/npm-remote/foo",
		"/r0/artifactory/api/pypi/pypi-remote/simple/foo/",
		"/r1/artifactory/api/npm/npm-remote/bar",
		"/r1/artifactory/api/pypi/pypi-remote/simple/bar/",
		"/r1/artifactory/api/cargo/crates.io/api/v1/crates/bar/1.0.0/download",
	}
	for _, path := range misses {
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
	wantR0 := "registryproxy: r0: suppressed 1 further distinct path outside derived allowlist"
	wantR1 := "registryproxy: r1: suppressed 2 further distinct paths outside derived allowlist"
	if !strings.Contains(logged, wantR0) {
		t.Errorf("log output = %q, want it to contain %q", logged, wantR0)
	}
	if !strings.Contains(logged, wantR1) {
		t.Errorf("log output = %q, want it to contain %q", logged, wantR1)
	}
	if gotR0, gotR1 := strings.Index(logged, wantR0), strings.Index(logged, wantR1); gotR0 > gotR1 {
		t.Errorf("r0's summary (at %d) logged after r1's (at %d), want route-table order", gotR0, gotR1)
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

			handler, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: "real-credential"}}))
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

	handler, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: ""}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	p := &Proxy{Handler: handler}
	if err := p.ListenAndServeTCP("127.0.0.1:0", secret); err != nil {
		t.Fatalf("ListenAndServeTCP: %v", err)
	}
	defer p.Close()

	req, err := http.NewRequest(http.MethodGet, "http://"+p.Addr().String()+"/r0/crates/foo", nil)
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

	handler, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: credential}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	p := &Proxy{Handler: handler}
	if err := p.ListenAndServeTCP("127.0.0.1:0", secret); err != nil {
		t.Fatalf("ListenAndServeTCP: %v", err)
	}
	defer p.Close()

	req, err := http.NewRequest(http.MethodGet, "http://"+p.Addr().String()+"/r0/crates/foo", nil)
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
	handler, err := New(AssignPrefixes([]Route{{Upstream: "http://127.0.0.1:1", Credential: ""}}))
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

	handler, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: "s3kr1t"}}))
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
// TestAssignPrefixes_EmptyMatchHostFallsBackToIndex verifies a route with no
// MatchHost gets a synthetic "r<index>" prefix instead of an empty one.
// registryroutes.Parse rejects an empty match-host, so no routes file reaches
// this branch; AssignPrefixes is exported and takes Route values from any
// caller, so the fallback stays the guard against a prefix-less route.
func TestAssignPrefixes_EmptyMatchHostFallsBackToIndex(t *testing.T) {
	routes := AssignPrefixes([]Route{
		{MatchHost: "registry-a.example"},
		{MatchHost: ""},
	})
	if got, want := routes[1].Prefix, "r1"; got != want {
		t.Errorf("Prefix = %q, want %q", got, want)
	}
}

// TestNew_RejectsEmptyPrefix verifies New refuses a route whose Prefix is
// empty rather than silently accepting an unroutable route.
func TestNew_RejectsEmptyPrefix(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	if _, err := New([]Route{{Upstream: upstream.URL, Prefix: ""}}); err == nil {
		t.Fatal("New with empty Prefix = nil error, want error")
	}
}

// TestNew_RejectsDuplicatePrefix verifies New refuses two routes that share
// a Prefix, since a Forwarder-facing request naming that prefix would then
// have no unique route to select.
func TestNew_RejectsDuplicatePrefix(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	_, err := New([]Route{
		{Upstream: upstream.URL, Prefix: "dup"},
		{Upstream: upstream.URL, Prefix: "dup"},
	})
	if err == nil {
		t.Fatal("New with duplicate Prefix = nil error, want error")
	}
}

// TestNew_RejectsInvalidPrefixChars verifies New refuses a Prefix carrying a
// character outside [a-z0-9-], since it becomes the first URL path segment a
// Forwarder-facing request selects a route by.
func TestNew_RejectsInvalidPrefixChars(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	for _, prefix := range []string{"Registry", "registry_a", "registry/a", "registry a"} {
		t.Run(prefix, func(t *testing.T) {
			if _, err := New([]Route{{Upstream: upstream.URL, Prefix: prefix}}); err == nil {
				t.Fatalf("New with Prefix %q = nil error, want error", prefix)
			}
		})
	}
}

// TestAssignPrefixes_CollisionDedupe verifies distinct hosts whose slugs
// collide (e.g. differing only by a character AssignPrefixes maps to the
// same '-') dedupe deterministically by table order: the first occurrence
// keeps the bare slug, and each later one appends "-2", "-3", ...
func TestAssignPrefixes_CollisionDedupe(t *testing.T) {
	routes := AssignPrefixes([]Route{
		{MatchHost: "registry.a"},
		{MatchHost: "registry-a"},
		{MatchHost: "registry_a"},
	})
	got := []string{routes[0].Prefix, routes[1].Prefix, routes[2].Prefix}
	want := []string{"registry-a", "registry-a-2", "registry-a-3"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("routes[%d].Prefix = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestAssignPrefixes_CollisionDedupe_GeneratedPrefixCollidesWithLiteral
// verifies that when a later route's own MatchHost literally slugifies to
// the same string AssignPrefixes would generate for an earlier collision
// (e.g. "example-com-2"), the generated prefix is still registered as used
// so the literal collision gets its own suffix instead of reusing it. All
// three assigned prefixes must be unique and deterministic by table order.
func TestAssignPrefixes_CollisionDedupe_GeneratedPrefixCollidesWithLiteral(t *testing.T) {
	routes := AssignPrefixes([]Route{
		{MatchHost: "example.com"},
		{MatchHost: "example.com"},
		{MatchHost: "example-com-2"},
	})
	got := []string{routes[0].Prefix, routes[1].Prefix, routes[2].Prefix}
	want := []string{"example-com", "example-com-2", "example-com-2-2"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("routes[%d].Prefix = %q, want %q", i, got[i], want[i])
		}
	}
	seenPrefix := make(map[string]bool, len(got))
	for i, p := range got {
		if seenPrefix[p] {
			t.Errorf("routes[%d].Prefix = %q duplicates an earlier route's Prefix", i, p)
		}
		seenPrefix[p] = true
	}
}

// TestAssignPrefixes_SlugFromMatchHost verifies the derived Prefix is the
// route's MatchHost lowercased, port-stripped, with every character outside
// [a-z0-9] mapped to '-'.
func TestAssignPrefixes_SlugFromMatchHost(t *testing.T) {
	routes := AssignPrefixes([]Route{
		{MatchHost: "Registry.Example.COM:8443"},
	})
	if got, want := routes[0].Prefix, "registry-example-com"; got != want {
		t.Errorf("Prefix = %q, want %q", got, want)
	}
}

func TestNew_NeverLogsCredentialForOutOfAllowlistPath(t *testing.T) {
	const credential = "sekret-token"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{Upstream: upstream.URL, Credential: credential}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/api/v1/crates/foo/1.0.0/download", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if strings.Contains(logBuf.String(), credential) {
		t.Errorf("log output contained the credential: %q", logBuf.String())
	}
}

// TestNew_NeverLogsCredentialAcrossRoutesForMissSummaryLines is a sibling of
// TestNew_NeverLogsCredentialForOutOfAllowlistPath rather than an extension
// of it: that test exercises one route and one miss line, but the
// suppressed-miss summary line (issue #3176's per-route flush, both at match
// and at Close) is a second, distinct place a route's credential could leak,
// and it only exists once at least two routes are in play with differing
// credentials. Route "a" produces a first-miss line then a suppressed count
// flushed immediately by a matching request; route "b" produces a first-miss
// line then a suppressed count only flushed later, by Close. No line from
// either route may ever contain the other's (or its own) credential.
func TestNew_NeverLogsCredentialAcrossRoutesForMissSummaryLines(t *testing.T) {
	const credA = "credential-for-route-a"
	const credB = "credential-for-route-b"

	okUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer okUpstream.Close()

	assigned := AssignPrefixes([]Route{
		{MatchHost: "route-a.example", Upstream: okUpstream.URL, Credential: credA},
		{MatchHost: "route-b.example", Upstream: okUpstream.URL, Credential: credB},
	})

	p, err := New(assigned)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	prefixA, prefixB := assigned[0].Prefix, assigned[1].Prefix

	// Route a: first miss (logs in full), a second distinct miss
	// (suppressed), then a match -- flushing the suppressed count
	// immediately rather than deferring it to Close.
	for _, path := range []string{
		"/" + prefixA + "/api/v1/crates/foo/1.0.0/download",
		"/" + prefixA + "/artifactory/api/npm/npm-remote/foo",
	} {
		mrr := httptest.NewRecorder()
		p.ServeHTTP(mrr, httptest.NewRequest(http.MethodGet, path, nil))
		if mrr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d for %s", mrr.Code, http.StatusOK, path)
		}
	}
	mrr := httptest.NewRecorder()
	p.ServeHTTP(mrr, httptest.NewRequest(http.MethodGet, "/"+prefixA+"/config.json", nil))
	if mrr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d for route a's matching request", mrr.Code, http.StatusOK)
	}

	// Route b: first miss (logs in full, never re-counted -- issue #3176
	// review finding), then a distinct second path repeated twice more
	// (suppressed, still one distinct path) -- left unmatched, so the
	// summary only flushes later, at Close.
	for _, path := range []string{
		"/" + prefixB + "/api/v1/crates/bar/2.0.0/download",
		"/" + prefixB + "/artifactory/api/npm/npm-remote/bar",
		"/" + prefixB + "/artifactory/api/npm/npm-remote/bar",
	} {
		brr := httptest.NewRecorder()
		p.ServeHTTP(brr, httptest.NewRequest(http.MethodGet, path, nil))
		if brr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d for %s", brr.Code, http.StatusOK, path)
		}
	}

	closer, ok := p.(interface{ Close() })
	if !ok {
		t.Fatalf("handler returned by New does not implement Close()")
	}
	closer.Close()

	logged := logBuf.String()
	if strings.Contains(logged, credA) {
		t.Errorf("log output contained route a's credential: %q", logged)
	}
	if strings.Contains(logged, credB) {
		t.Errorf("log output contained route b's credential: %q", logged)
	}
	// Sanity check the scenario actually exercised the lines it claims to:
	// a first-miss line, a match-time flush, and a Close-time flush.
	if got := strings.Count(logged, "registryproxy: "+prefixA+": path outside derived allowlist:"); got != 1 {
		t.Errorf("route a first-miss lines = %d, want 1: %q", got, logged)
	}
	if !strings.Contains(logged, "registryproxy: "+prefixA+": suppressed 1 further distinct path outside derived allowlist") {
		t.Errorf("log output missing route a's match-time flush: %q", logged)
	}
	if got := strings.Count(logged, "registryproxy: "+prefixB+": path outside derived allowlist:"); got != 1 {
		t.Errorf("route b first-miss lines = %d, want 1: %q", got, logged)
	}
	if !strings.Contains(logged, "registryproxy: "+prefixB+": suppressed 1 further distinct path outside derived allowlist") {
		t.Errorf("log output missing route b's Close-time flush: %q", logged)
	}
}

// TestModifyResponse_CargoConfigJSON_RewritesDL verifies a GET for a route's
// config.json, whose "dl" names that route's own match-host, comes back
// through the proxy end-to-end with "dl" rewritten to the Forwarder -- the
// address the client itself used to reach the proxy (req.Host) -- with the
// route's prefix re-inserted and the dl's own path preserved.
func TestModifyResponse_CargoConfigJSON_RewritesDL(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/config.json" {
			t.Errorf("upstream got path %q, want /config.json", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dl":"https://crates.example.com/api/v1/crates"}`))
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", Upstream: upstream.URL}})
	p, err := New(routes)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	prefix := routes[0].Prefix

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/config.json", nil)
	req.Host = "forwarder.example:9999"
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	wantBody := `{"dl":"http://forwarder.example:9999/` + prefix + `/api/v1/crates"}`
	if got := rr.Body.String(); got != wantBody {
		t.Errorf("body = %s, want %s", got, wantBody)
	}
	if got := rr.Header().Get("Content-Length"); got != strconv.Itoa(len(wantBody)) {
		t.Errorf("Content-Length = %q, want %q", got, strconv.Itoa(len(wantBody)))
	}
}

// TestModifyResponse_CargoConfigJSON_PreservesUpstreamBasePath guards
// against issue #2854's path-double-join defect, end-to-end through
// ModifyResponse: a route whose upstream itself carries a base path (an
// Artifactory-style remote repo) must have that base path stripped out of
// the rewritten dl, not carried through -- the rewritten dl is
// route-relative, so a later request built from it re-enters the proxy and
// gets the base path joined back on exactly once by the Rewrite hook,
// rather than carrying it through here and doubling it up on that second
// join (issue #3175's blocking review finding).
func TestModifyResponse_CargoConfigJSON_PreservesUpstreamBasePath(t *testing.T) {
	const basePath = "/artifactory/api/cargo/example-remote"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if want := basePath + "/config.json"; r.URL.Path != want {
			t.Errorf("upstream got path %q, want %q", r.URL.Path, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dl":"https://artifactory.example.com` + basePath + `/api/v1/crates"}`))
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{
		MatchHost: "artifactory.example.com",
		Upstream:  upstream.URL + basePath,
	}})
	p, err := New(routes)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	prefix := routes[0].Prefix

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/config.json", nil)
	req.Host = "forwarder.example:9999"
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	wantBody := `{"dl":"http://forwarder.example:9999/` + prefix + `/api/v1/crates"}`
	if got := rr.Body.String(); got != wantBody {
		t.Errorf("body = %s, want %s (rewritten dl must be route-relative -- the base path is re-joined by the Rewrite hook on the round trip, not carried here)", got, wantBody)
	}
}

// TestModifyResponse_RewrittenDLRoundTripsWithoutDoublingBasePath closes the
// loop a review finding on issue #3175 left untested: on a base-path route,
// the dl rewritten by the first request must itself be fetchable back
// through the same proxy without the upstream base path getting joined a
// second time -- issue #2854's path-double-join defect, resurfacing one hop
// later when the rewritten dl re-enters the proxy's own Rewrite hook.
func TestModifyResponse_RewrittenDLRoundTripsWithoutDoublingBasePath(t *testing.T) {
	const basePath = "/artifactory/api/cargo/example-remote"
	const downloadPath = basePath + "/api/v1/crates/foo/1.0.0/download"

	var gotDownloadPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case basePath + "/config.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"dl":"https://artifactory.example.com` + downloadPath + `"}`))
		case downloadPath:
			gotDownloadPath = r.URL.Path
			_, _ = w.Write([]byte("crate-bytes"))
		default:
			t.Errorf("upstream got unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{
		MatchHost: "artifactory.example.com",
		Upstream:  upstream.URL + basePath,
	}})
	p, err := New(routes)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	prefix := routes[0].Prefix

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/config.json", nil)
	req.Host = "forwarder.example:9999"
	p.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var config struct {
		DL string `json:"dl"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &config); err != nil {
		t.Fatalf("unmarshal config.json response: %v", err)
	}
	dlURL, err := url.Parse(config.DL)
	if err != nil {
		t.Fatalf("parse rewritten dl %q: %v", config.DL, err)
	}

	// The round trip: cargo builds its crate-download request by joining
	// the rewritten dl with a crate path, then fetches it -- straight back
	// through this same proxy.
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, dlURL.Path, nil)
	req2.Host = "forwarder.example:9999"
	p.ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusOK {
		t.Fatalf("round-trip status = %d, want %d (rewritten dl %q)", rr2.Code, http.StatusOK, config.DL)
	}
	if got := rr2.Body.String(); got != "crate-bytes" {
		t.Errorf("round-trip body = %q, want %q", got, "crate-bytes")
	}
	if gotDownloadPath != downloadPath {
		t.Errorf("upstream saw download path %q, want %q (base path must not be joined twice)", gotDownloadPath, downloadPath)
	}
}

// TestModifyResponse_ForeignHostDLLeftAloneAndLogsSkipOnce verifies a
// config.json whose "dl" names a host other than the route's match-host (a
// CDN) is relayed with that dl byte-identical, and exactly one log line
// records the deliberate skip -- an acceptance criterion of issue #3175.
// Also asserts neither log line the request produces contains the route's
// credential.
func TestModifyResponse_ForeignHostDLLeftAloneAndLogsSkipOnce(t *testing.T) {
	const credential = "s3kr1t-do-not-log-me"
	const body = `{"dl":"https://cdn.example.com/api/v1/crates"}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", Upstream: upstream.URL, Credential: credential}})
	p, err := New(routes)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	prefix := routes[0].Prefix

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(prevOutput) })

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/config.json", nil)
	req.Host = "forwarder.example:9999"
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Body.String(); got != body {
		t.Errorf("body = %s, want byte-identical to upstream's %s", got, body)
	}

	logged := logBuf.String()
	if got := strings.Count(logged, "left unchanged"); got != 1 {
		t.Errorf("skip log line occurred %d times in log output, want exactly 1: %q", got, logged)
	}
	if strings.Contains(logged, credential) {
		t.Errorf("log output contained the credential: %q", logged)
	}
}

// TestModifyResponse_ForeignHostDLGzipRequestGetsIdentityFromUpstream
// documents an intended ordering cost: the Rewrite hook forces
// Accept-Encoding: identity on the outbound request before the response
// body -- and therefore whether the dl rewrite will even apply -- is known,
// because the request shape (not the response) is all Rewrite has to go
// on. So a client that asked for gzip still gets an identity response from
// upstream even when the dl turns out to name a foreign host and the
// rewrite is skipped. This is a characterization test for existing,
// intended behaviour, not a bug.
func TestModifyResponse_ForeignHostDLGzipRequestGetsIdentityFromUpstream(t *testing.T) {
	const body = `{"dl":"https://cdn.example.com/api/v1/crates"}`

	var gotAcceptEncoding string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAcceptEncoding = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Type", "application/json")
		// Would gzip here (and set Content-Encoding: gzip) had the
		// client's Accept-Encoding: gzip been forwarded; it wasn't.
		_, _ = w.Write([]byte(body))
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", Upstream: upstream.URL}})
	p, err := New(routes)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	prefix := routes[0].Prefix

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/config.json", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if gotAcceptEncoding != "identity" {
		t.Errorf("upstream saw Accept-Encoding = %q, want %q", gotAcceptEncoding, "identity")
	}
	if got := rr.Body.String(); got != body {
		t.Errorf("body = %s, want byte-identical to upstream's %s", got, body)
	}
}

// TestModifyResponse_NoMatchingRowRelayedByteIdentical verifies a response
// to a request matching no responseRewriteTable row is relayed
// byte-identical: body and Content-Length both unchanged.
func TestModifyResponse_NoMatchingRowRelayedByteIdentical(t *testing.T) {
	const body = `{"dl":"https://crates.example.com/api/v1/crates"}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", Upstream: upstream.URL}})
	p, err := New(routes)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	prefix := routes[0].Prefix

	// The cargo download endpoint (dl's own target) is not "/config.json",
	// so it names no responseRewriteTable row at all -- even though its
	// body happens to carry a "dl" field naming this route's own
	// match-host.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/api/v1/crates/foo/1.0.0/download", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Body.String(); got != body {
		t.Errorf("body = %s, want byte-identical to upstream's %s", got, body)
	}
	if got := rr.Header().Get("Content-Length"); got != strconv.Itoa(len(body)) {
		t.Errorf("Content-Length = %q, want %q (unchanged)", got, strconv.Itoa(len(body)))
	}
}

// TestModifyResponse_WrongMediaTypeShapeNotMatchedIsUntouched guards against
// issue #2854's wrong-media-type defect: the reverted hook decided whether to
// rewrite by sniffing Content-Type and looking for a "dl" field in the body,
// so any response shaped like that -- regardless of which request produced
// it -- got rewritten. Here the response has exactly that shape
// (Content-Type: application/json, body with a "dl" naming the route's own
// match-host) but the request's path names no responseRewriteTable row, so
// the new shape-keyed table must leave it untouched.
func TestModifyResponse_WrongMediaTypeShapeNotMatchedIsUntouched(t *testing.T) {
	const body = `{"dl":"https://crates.example.com/api/v1/crates"}`

	for _, path := range []string{"/api/v1/crates", "/index/co/nf/config"} {
		t.Run(path, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			defer upstream.Close()

			routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", Upstream: upstream.URL}})
			p, err := New(routes)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			prefix := routes[0].Prefix

			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/"+prefix+path, nil)
			p.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
			}
			if got := rr.Body.String(); got != body {
				t.Errorf("body = %s, want byte-identical to upstream's %s (issue #2854 wrong-media-type defect: a JSON dl body must not be rewritten just because it looks like one)", got, body)
			}
		})
	}
}

// TestModifyResponse_HeadForCargoConfigJSONUntouched guards against issue
// #2854's HEAD-crash defect: a HEAD response has no body for ModifyResponse
// to read, and the reverted hook crashed trying to parse one anyway. A HEAD
// for the cargo config.json shape must pass through with the upstream's
// status and headers, and must not panic the proxy.
func TestModifyResponse_HeadForCargoConfigJSONUntouched(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Test", "head-response")
		w.WriteHeader(http.StatusOK)
		// net/http strips any body for a HEAD request automatically, so no
		// body is written here even though Content-Type is set -- that's
		// the shape a real HEAD /config.json response has.
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", Upstream: upstream.URL}})
	p, err := New(routes)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	prefix := routes[0].Prefix

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "/"+prefix+"/config.json", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Header().Get("X-Test"); got != "head-response" {
		t.Errorf("X-Test header = %q, want %q", got, "head-response")
	}
	if body := rr.Body.String(); body != "" {
		t.Errorf("body = %q, want empty (HEAD)", body)
	}
}

// TestModifyResponse_RewrittenResponseNeverCarriesCredential verifies a
// rewritten config.json response body -- as received by the client -- never
// contains the route's own credential, even though that credential was
// attached to the outbound request the proxy made to upstream.
func TestModifyResponse_RewrittenResponseNeverCarriesCredential(t *testing.T) {
	const credential = "s3kr1t-do-not-leak-me"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dl":"https://crates.example.com/api/v1/crates"}`))
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", Upstream: upstream.URL, Credential: credential}})
	p, err := New(routes)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	prefix := routes[0].Prefix

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/config.json", nil)
	req.Host = "forwarder.example:9999"
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if strings.Contains(rr.Body.String(), credential) {
		t.Errorf("response body contained the credential: %q", rr.Body.String())
	}
}

// TestModifyResponse_GzippedConfigJSONStillRewritten guards against the
// blocking review finding on issue #3175: a real cargo client sends its own
// "Accept-Encoding: gzip", and net/http's Transport only auto-decompresses a
// gzip response when *it* added that header itself -- forwarded verbatim, it
// leaves the response's bytes gzip-compressed by the time modifyResponse
// reads them. Before the fix, that compressed body failed json.Decode and
// was relayed untouched -- the exact 401 this ticket exists to fix, silently
// undiagnosable. This upstream only compresses when the request names gzip,
// mirroring a real registry/CDN.
func TestModifyResponse_GzippedConfigJSONStillRewritten(t *testing.T) {
	const rawBody = `{"dl":"https://crates.example.com/api/v1/crates"}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			gz := gzip.NewWriter(w)
			_, _ = gz.Write([]byte(rawBody))
			_ = gz.Close()
			return
		}
		_, _ = w.Write([]byte(rawBody))
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", Upstream: upstream.URL}})
	p, err := New(routes)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	prefix := routes[0].Prefix

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/config.json", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Host = "forwarder.example:9999"
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	var config struct {
		DL string `json:"dl"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &config); err != nil {
		t.Fatalf("response body is not valid JSON (gzip bytes leaked through unrewritten?): %v, body = %q", err, rr.Body.String())
	}
	wantDL := "http://forwarder.example:9999/" + prefix + "/api/v1/crates"
	if config.DL != wantDL {
		t.Errorf("dl = %q, want %q", config.DL, wantDL)
	}
}

// TestNew_NonMatchingShapePreservesClientAcceptEncoding verifies a request
// whose shape matches no responseRewriteTable row reaches upstream with the
// client's own Accept-Encoding untouched -- only a request shape that does
// match a row gets forced to "identity" (see the Rewrite hook), so this path
// must be unaffected.
func TestNew_NonMatchingShapePreservesClientAcceptEncoding(t *testing.T) {
	var gotAcceptEncoding string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAcceptEncoding = r.Header.Get("Accept-Encoding")
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", Upstream: upstream.URL}})
	p, err := New(routes)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	prefix := routes[0].Prefix

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/api/v1/crates/foo/1.0.0/download", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if gotAcceptEncoding != "gzip, deflate" {
		t.Errorf("upstream got Accept-Encoding %q, want %q (untouched)", gotAcceptEncoding, "gzip, deflate")
	}
}

// TestModifyResponse_MatchedRowNoRewritableFieldLogsWithoutBodyOrCredential
// verifies the second half of issue #3175's blocking review finding: a
// request whose shape matches a responseRewriteTable row, but whose body
// held nothing rewritable (rewriteNone), must now log one line naming the
// row -- previously silent, making a no-op rewrite undiagnosable -- and that
// line must contain neither the route's credential nor any of the body.
func TestModifyResponse_MatchedRowNoRewritableFieldLogsWithoutBodyOrCredential(t *testing.T) {
	const credential = "s3kr1t-do-not-log-me"
	const body = `{"not-a-dl-field":"nothing to rewrite here"}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", Upstream: upstream.URL, Credential: credential}})
	p, err := New(routes)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	prefix := routes[0].Prefix

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(prevOutput) })

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/config.json", nil)
	req.Host = "forwarder.example:9999"
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Body.String(); got != body {
		t.Errorf("body = %s, want byte-identical to upstream's %s", got, body)
	}

	logged := logBuf.String()
	if !strings.Contains(logged, "cargo config.json") {
		t.Errorf("log output = %q, want a line naming the matched row (\"cargo config.json\")", logged)
	}
	if strings.Contains(logged, credential) {
		t.Errorf("log output contained the credential: %q", logged)
	}
	if strings.Contains(logged, "not-a-dl-field") || strings.Contains(logged, "nothing to rewrite here") {
		t.Errorf("log output contained body content: %q", logged)
	}
}

// TestModifyResponse_SuccessfulRewriteLogsOnceWithoutCredentialOrBody closes
// the other half of a blocking review finding on issue #3175: only the skip
// path had a log-capturing test; the rewrite path itself -- the far more
// common case -- had none. Asserts the rewrite line exists, names the
// before/after dl values, occurs exactly once for one response, and that
// neither it nor anything else logged carries the route's credential or an
// unrelated body field -- both fixture strings are distinctive sentinels so
// an accidental leak can't hide inside a plausible-looking substring.
func TestModifyResponse_SuccessfulRewriteLogsOnceWithoutCredentialOrBody(t *testing.T) {
	const credential = "s3kr1t-sentinel-do-not-log-me"
	const sentinelField = "sentinel-unrelated-field"
	const sentinelValue = "sentinel-unrelated-value"
	rawBody := `{"dl":"https://crates.example.com/api/v1/crates","` + sentinelField + `":"` + sentinelValue + `"}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(rawBody))
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", Upstream: upstream.URL, Credential: credential}})
	p, err := New(routes)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	prefix := routes[0].Prefix

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(prevOutput) })

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/config.json", nil)
	req.Host = "forwarder.example:9999"
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	wantFrom := "https://crates.example.com/api/v1/crates"
	wantTo := "http://forwarder.example:9999/" + prefix + "/api/v1/crates"
	logged := logBuf.String()

	if got := strings.Count(logged, "rewrote dl"); got != 1 {
		t.Errorf(`"rewrote dl" occurred %d times in log output, want exactly 1: %q`, got, logged)
	}
	if !strings.Contains(logged, wantFrom) || !strings.Contains(logged, wantTo) {
		t.Errorf("log output = %q, want it to name both the before (%q) and after (%q) dl values", logged, wantFrom, wantTo)
	}
	if strings.Contains(logged, credential) {
		t.Errorf("log output contained the credential: %q", logged)
	}
	if strings.Contains(logged, sentinelField) || strings.Contains(logged, sentinelValue) {
		t.Errorf("log output contained an unrelated body field: %q", logged)
	}
}

// TestModifyResponse_NilForwarderRelaysConfigJSONUnrewritten covers the
// nil-forwarder skip branch: an HTTP/1.0 client that sends no Host header
// leaves req.Host empty, so ServeHTTP never sets selectedRoute.forwarder --
// modifyResponse must relay the body byte-identical rather than dereference
// the nil *url.URL.
func TestModifyResponse_NilForwarderRelaysConfigJSONUnrewritten(t *testing.T) {
	const body = `{"dl":"https://crates.example.com/api/v1/crates"}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", Upstream: upstream.URL}})
	p, err := New(routes)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	prefix := routes[0].Prefix

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/config.json", nil)
	req.Host = "" // no Host header at all -- selectedRoute.forwarder stays nil
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Body.String(); got != body {
		t.Errorf("body = %s, want byte-identical to upstream's %s (nil forwarder must skip rewriting, not crash)", got, body)
	}
}

// TestModifyResponse_NonOKStatusSkipsRewrite covers the non-200 skip branch:
// an error page happens to arrive at the config.json shape but isn't a real
// config.json document, so it must be relayed byte-identical with its
// original status preserved rather than parsed and rewritten.
func TestModifyResponse_NonOKStatusSkipsRewrite(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			const body = `{"dl":"https://crates.example.com/api/v1/crates"}`

			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(body))
			}))
			defer upstream.Close()

			routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", Upstream: upstream.URL}})
			p, err := New(routes)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			prefix := routes[0].Prefix

			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/config.json", nil)
			req.Host = "forwarder.example:9999"
			p.ServeHTTP(rr, req)

			if rr.Code != status {
				t.Fatalf("status = %d, want %d", rr.Code, status)
			}
			if got := rr.Body.String(); got != body {
				t.Errorf("body = %s, want byte-identical to upstream's %s (non-200 must skip rewrite)", got, body)
			}
		})
	}
}

// TestModifyResponse_OverCapBodySplicedByteIdentical covers the over-cap
// splice-relay branch: a body larger than maxRewriteBodyBytes must reach the
// client whole and byte-for-byte, including everything past the cap --
// that's the entire point of bodyWithClose splicing the already-buffered
// prefix back onto the unread remainder rather than truncating. The marker
// straddles the cap offset itself, so an off-by-one at the splice join would
// land on top of it instead of hiding in the filler on either side.
func TestModifyResponse_OverCapBodySplicedByteIdentical(t *testing.T) {
	const straddle = "STRADDLE-MARKER-AT-CAP-BOUNDARY"

	body := make([]byte, maxRewriteBodyBytes+4096)
	for i := range body {
		body[i] = byte('a' + i%26)
	}
	copy(body[maxRewriteBodyBytes-len(straddle)/2:], straddle)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", Upstream: upstream.URL}})
	p, err := New(routes)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	prefix := routes[0].Prefix

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/config.json", nil)
	req.Host = "forwarder.example:9999"
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	got := rr.Body.Bytes()
	if len(got) != len(body) {
		t.Fatalf("body length = %d, want %d (over-cap body must relay whole, not truncated at the cap)", len(got), len(body))
	}
	if !bytes.Equal(got, body) {
		for i := range got {
			if got[i] != body[i] {
				t.Fatalf("body differs at byte %d (cap boundary is at %d): got %q, want %q", i, maxRewriteBodyBytes, got[i], body[i])
			}
		}
	}
}

// TestModifyResponse_BodyReadErrorReturns502 covers the body-read-error
// branch: upstream declares a Content-Length it never delivers and closes
// the connection mid-body, so io.ReadAll inside modifyResponse errors. The
// client must see a 502 rather than a hang or a panic.
func TestModifyResponse_BodyReadErrorReturns502(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatalf("upstream ResponseWriter does not support hijacking")
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		defer conn.Close()
		// Content-Length promises 1000 bytes of body but only a handful
		// follow, then the connection closes -- the client's Read then
		// fails with an unexpected-EOF short of the promised length.
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n{\"dl\":")
		_ = buf.Flush()
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", Upstream: upstream.URL}})
	p, err := New(routes)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	prefix := routes[0].Prefix

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/config.json", nil)
	req.Host = "forwarder.example:9999"
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d (body read error must become 502, not a hang or panic)", rr.Code, http.StatusBadGateway)
	}
}
