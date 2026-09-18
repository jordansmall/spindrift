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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/registryvocab"
	"spindrift.dev/launcher/internal/unixsocket"
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

	p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: ""}}), nil)
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

func TestNew_ForwardsHEAD(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("upstream got method %q, want HEAD", r.Method)
		}
		w.Header().Set("X-Test", "yes")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: ""}}), nil)
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

// The semicolon and malformed-percent-escape cases are the ones
// httputil.ReverseProxy's Rewrite path silently mangles to empty via
// cleanQueryParams, so the proxy must forward the raw query itself.
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

			p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: ""}}), nil)
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

// An upstream URL carrying its own query string must not clobber the inbound
// one, or the reverse. The wanted values follow
// httputil.NewSingleHostReverseProxy's legacy Director: join with "&" when
// both are non-empty, otherwise take whichever one is non-empty.
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

			upstreamURL := upstream.URL
			if tc.upstreamQuery != "" {
				upstreamURL += "?" + tc.upstreamQuery
			}

			p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstreamURL, Credential: ""}}), nil)
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

// The outbound X-Forwarded-For must still name the client's address, as
// httputil.NewSingleHostReverseProxy's legacy Director did.
func TestNew_SetsXForwardedForHeader(t *testing.T) {
	var gotXFF string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotXFF = r.Header.Get("X-Forwarded-For")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: ""}}), nil)
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

func TestServe_UnixSocket(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("via socket"))
	}))
	defer upstream.Close()

	handler, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: ""}}), nil)
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

// A socket file left behind by a prior run makes net.Listen fail with
// "address already in use" unless ListenAndServe removes it first.
func TestServe_RemovesStaleSocket(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "proxy.sock")

	stale, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	stale.Close()

	handler, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: "http://127.0.0.1:0", Credential: ""}}), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p := &Proxy{Handler: handler}
	if err := p.ListenAndServe(socketPath); err != nil {
		t.Fatalf("ListenAndServe: %v", err)
	}
	defer p.Close()
}

// An accepted empty route table would panic on the first request, where
// selectRoute indexes routes[0] of an empty slice.
func TestNew_RejectsEmptyRoutes(t *testing.T) {
	if _, err := New(nil, nil); err == nil {
		t.Fatal("New(nil) = nil error, want error")
	}
	if _, err := New([]Route{}, nil); err == nil {
		t.Fatal("New([]Route{}) = nil error, want error")
	}
}

// A malformed upstream URL must come back as an error, never a panic.
func TestNew_MalformedUpstream(t *testing.T) {
	cases := []string{
		"://not-a-url",
		"not-even-a-url no scheme",
		"",
		"/just/a/path",
	}
	for _, upstream := range cases {
		t.Run(upstream, func(t *testing.T) {
			if _, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream, Credential: ""}}), nil); err == nil {
				t.Errorf("New(%q) = nil error, want error", upstream)
			}
		})
	}
}

// Path-prefix dispatch replaced Host-header selection (issue #3142). Each
// upstream fails the test outright if it ever sees the other route's
// credential, so a credential crossing routes cannot pass silently.
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
		{MatchHost: "registry-a.example", EnforcedPaths: []string{"/"}, Upstream: upstreamA.URL, Credential: "token-a"},
		{MatchHost: "registry-b.example", EnforcedPaths: []string{"/"}, Upstream: upstreamB.URL, AuthScheme: "header:X-JFrog-Art-Api", Credential: "token-b"},
	})
	p, err := New(routes, nil)
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

// The refusal happens in front of ReverseProxy entirely (issue #3142). The
// countingListener proves no upstream is dialed at the TCP level, the same
// technique TestNew_RejectsNonGetHead_NeverDialsUpstream uses for the method
// gate.
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

	routes := AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL}})
	p, err := New(routes, nil)
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

// The bare root and a path whose first segment is empty both name no route
// prefix, so they take the same unknown-prefix refusal (issue #3142).
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

			p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL}}), nil)
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

// Some npm clients separate a scoped package's "@scope" and name with "%2f".
// Decoding it into a literal slash would make upstream read an extra path
// segment (issue #3142).
func TestNew_EscapedRemainderPreserved(t *testing.T) {
	var gotRawPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawPath = r.URL.EscapedPath()
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL}})
	p, err := New(routes, nil)
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

// A single-route table is the shape a scalar-knob bridge or a single-route
// TOML routes file builds, and it must still work once its one route also
// requires a Prefix (issue #3142 slice 2's back-compat criterion).
func TestNew_SingleRouteTableBackCompat(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("single route"))
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: "s3kr1t"}})
	p, err := New(routes, nil)
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

// registryvocab.HostKey strips the brackets before slugify runs, so a
// bracketed literal IPv6 MatchHost slugifies like its bracket-free form.
// Since prefix routing replaced Host-header selection (issue #3142), this is
// the only test still reaching that bracket-stripping.
func TestAssignPrefixes_BracketedIPv6MatchHost(t *testing.T) {
	routes := AssignPrefixes([]Route{{MatchHost: "[::1]"}})
	if got, want := routes[0].Prefix, "--1"; got != want {
		t.Errorf("Prefix = %q, want %q", got, want)
	}
}

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

				p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: credential}}), nil)
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

// countingListener counts only accepts that returned a connection, so a test
// can see whether upstream was dialed at the TCP level at all. That is a
// stronger signal than "the upstream handler never ran". The count is
// incremented on the httptest server's accept goroutine with nothing
// synchronizing it to the assertion point; countingTransport is synchronous.
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

// Two counters, because each rules out a different thing: the accept count
// rules out a completed TCP accept but is observed asynchronously, while the
// round-trip count is recorded on this goroutine and also rules out a dial
// abandoned before any response.
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

	p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: "s3kr1t"}}), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ct := countUpstreamAttempts(t, p)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/crates/foo", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
	if got := atomic.LoadInt32(&cl.accepts); got != 0 {
		t.Errorf("upstream listener accepted %d connections, want 0", got)
	}
	if got := atomic.LoadInt32(&ct.attempts); got != 0 {
		t.Errorf("upstream round-trip attempts = %d, want 0", got)
	}
}

// countingTransport increments before delegating, so an abandoned or
// panicking round trip is still counted. httputil.ReverseProxy.ServeHTTP
// calls RoundTrip on the same goroutine as p.ServeHTTP, so unlike
// countingListener's accept count this one is visible the instant ServeHTTP
// returns, with no wait.
type countingTransport struct {
	attempts int32
	delegate http.RoundTripper
}

func (t *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	atomic.AddInt32(&t.attempts, 1)
	return t.delegate.RoundTrip(req)
}

// countUpstreamAttempts installs a countingTransport on the ReverseProxy the
// handler New returns holds. It asserts the concrete *routeLogHandler rather
// than the narrow interface other tests use for Close(), because it needs a
// field on that struct, not a method.
func countUpstreamAttempts(t *testing.T, h http.Handler) *countingTransport {
	t.Helper()
	rlh, ok := h.(*routeLogHandler)
	if !ok {
		t.Fatalf("handler returned by New is not a *routeLogHandler")
	}
	ct := &countingTransport{delegate: http.DefaultTransport}
	rlh.rp.Transport = ct
	return ct
}

// The upstream hijacks the connection and closes it without writing, so the
// proxy's ErrorHandler answers 502 and countUpstreamAttempts still has to
// record the attempt. The count is read immediately after ServeHTTP returns,
// with no sleep.
func TestCountingTransport_RecordsUpstreamAttemptAbandonedBeforeResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatalf("upstream ResponseWriter does not support hijacking")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		conn.Close()
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: ""}}), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ct := countUpstreamAttempts(t, p)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/crates/foo", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadGateway)
	}
	if got := atomic.LoadInt32(&ct.attempts); got != 1 {
		t.Errorf("upstream attempts = %d, want 1", got)
	}
}

// Defense in depth: registryroutes already validates scheme names before a
// route reaches New, but an unknown one here must error rather than silently
// misrender the credential.
func TestNew_UnknownAuthSchemeErrors(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	_, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, AuthScheme: "made-up-scheme", Credential: "s3kr1t"}}), nil)
	if err == nil {
		t.Fatal("New with an unknown AuthScheme = nil error, want error")
	}
}

// A non-empty credential rides every forwarded request as
// "Authorization: Bearer <credential>" (ADR 0044).
func TestNew_AttachesCredentialToOutboundRequest(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: "s3kr1t"}}), nil)
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

// Issue #3124: cargo sends a credentials.toml token verbatim as the
// Authorization value rather than prepending a scheme, so registries
// documenting a cargo setup bake the scheme into the token (Artifactory's own
// docs emit a Bearer-prefixed one). Such a credential is the whole header
// value; one naming no scheme is still sent as Bearer.
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
		// HTTP auth schemes are case-insensitive per RFC 7235, and a
		// registry's docs may spell one in any case.
		{"lowercase bearer passes through", "bearer eyJhbGc", "bearer eyJhbGc"},
		{"mixed-case Basic passes through", "bAsIc dXNlcjpwdw==", "bAsIc dXNlcjpwdw=="},
		// Only a genuine scheme prefix counts. A token that merely contains a
		// scheme word, or starts with one without the delimiting space, is an
		// ordinary opaque credential.
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

// End-to-end regression for issue #3124: before the fix a Bearer-prefixed
// credential reached upstream doubled, which Artifactory rejected with a 401
// naming a token type it could not resolve.
func TestNew_CredentialWithInlineSchemeIsNotDoublePrefixed(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: "Bearer eyJhbGciOiJSUzI1NiJ9"}}), nil)
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

// AuthScheme "basic" base64-encodes a plain user:password credential, but a
// credential already naming its own Basic scheme passes through verbatim,
// under the same genuine-prefix rule bearer uses (issue #3139 slice 2).
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

			p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, AuthScheme: "basic", Credential: tc.credential}}), nil)
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

// The inline-scheme pass-through also gives the proxy HTTP Basic support,
// which it had no way to express before issue #3124.
func TestNew_BasicCredentialReachesUpstreamUnchanged(t *testing.T) {
	var gotUser, gotPass string
	var gotOK bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, gotOK = r.BasicAuth()
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:hunter2"))}}), nil)
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

// A Box-controlled client can name "Authorization" in its own Connection
// header, which would make httputil.ReverseProxy strip the header the proxy
// just set and defeat credential injection entirely (ADR 0044).
func TestNew_AttachesCredentialEvenWithConnectionHeaderTrick(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: "s3kr1t"}}), nil)
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

// httputil.NewSingleHostReverseProxy's base director rewrites req.URL.Host
// but not req.Host, so without an explicit fix a Box-controlled client could
// steer a configured credential to a different vhost or tenant sharing the
// upstream's IP and certificate just by setting its own Host header.
func TestNew_RewritesHostHeaderToUpstream(t *testing.T) {
	var gotHost string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: "s3kr1t"}}), nil)
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

// AuthScheme "header:<Name>" attaches the credential verbatim to the named
// header instead of Authorization, the JFrog X-JFrog-Art-Api pattern (issue
// #3139 slice 2, ADR 0045).
func TestNew_HeaderAuthScheme(t *testing.T) {
	var gotNamed, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotNamed = r.Header.Get("X-JFrog-Art-Api")
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, AuthScheme: "header:X-JFrog-Art-Api", Credential: "s3kr1t-api-key"}}), nil)
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

// The unauthenticated pass-through policy holds for every scheme, not just
// the bearer default.
func TestNew_EmptyCredentialSkipsHeaderRegardlessOfScheme(t *testing.T) {
	for _, scheme := range []string{"", "bearer", "basic", "header:X-JFrog-Art-Api"} {
		t.Run(scheme, func(t *testing.T) {
			headers := http.Header{}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				headers = r.Header.Clone()
				w.WriteHeader(http.StatusOK)
			}))
			defer upstream.Close()

			p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, AuthScheme: scheme, Credential: ""}}), nil)
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

func TestNew_EmptyCredentialAttachesNoAuthorizationHeader(t *testing.T) {
	var gotAuth string
	var sawHeader bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, sawHeader = r.Header.Get("Authorization"), r.Header.Get("Authorization") != ""
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: ""}}), nil)
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

// The proxy relays a 3xx rather than following it, so the credential never
// crosses to the redirect target (ADR 0044). The hit count pins the single
// hop.
func TestNew_DoesNotFollowRedirect(t *testing.T) {
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Location", "https://elsewhere.example/target")
		w.WriteHeader(http.StatusFound)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: "s3kr1t"}}), nil)
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

// httptest.NewTLSServer's self-signed certificate is not in the default cert
// pool, so the outbound handshake must fail and ReverseProxy's error handler
// must answer 502. A regression swapping in a Transport with
// InsecureSkipVerify would complete the handshake and return the upstream's
// 200 instead.
func TestNew_VerifiesUpstreamTLSCertificate(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("should never be seen"))
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: ""}}), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Silence the default error handler's log line for the expected handshake
	// failure.
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

// Guards against a future stray log line leaking the credential during a
// normal request cycle.
func TestNew_NeverLogsCredential(t *testing.T) {
	const credential = "s3kr1t-do-not-log-me"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: credential}}), nil)
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

// Refusal ordering is load-bearing (issue #3177): a write to a path outside
// the enforced set answers 405, not 403.
func TestNew_MethodGatePrecedes403(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/index"}, Upstream: upstream.URL, Credential: "s3kr1t"}}), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/r0/api/v1/crates/foo/1.0.0/download", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d (method gate must precede path-set enforcement)", rr.Code, http.StatusMethodNotAllowed)
	}
}

// Refusal ordering is load-bearing (issue #3177): a missing or wrong secret
// on a path outside the enforced set answers 401, not 403.
func TestListenAndServeTCP_SecretGatePrecedes403(t *testing.T) {
	const secret = "s3kr1t-tcp-secret"

	cases := []struct {
		name   string
		header string
	}{
		{name: "missing secret", header: ""},
		{name: "wrong secret", header: "not-the-secret"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			defer upstream.Close()

			handler, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/index"}, Upstream: upstream.URL, Credential: "real-credential"}}), nil)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			p := &Proxy{Handler: handler}
			if err := p.ListenAndServeTCP("127.0.0.1:0", secret); err != nil {
				t.Fatalf("ListenAndServeTCP: %v", err)
			}
			defer p.Close()

			req, err := http.NewRequest(http.MethodGet, "http://"+p.Addr().String()+"/r0/api/v1/crates/foo/1.0.0/download", nil)
			if err != nil {
				t.Fatalf("http.NewRequest: %v", err)
			}
			if tc.header != "" {
				req.Header.Set(registrymanifest.TCPSecretHeader, tc.header)
			}

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("http.DefaultClient.Do: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d (secret gate must precede path-set enforcement)", resp.StatusCode, http.StatusUnauthorized)
			}
		})
	}
}

// A socket path at or over the platform's sun_path cap must be refused by the
// preflight check, not by net.Listen, and the error must name both the actual
// byte length and the cap (issue #3077). A nil p.listener proves which check
// refused it.
func TestServe_PathTooLong(t *testing.T) {
	sunPathLimit := unixsocket.Cap()

	dir := t.TempDir()
	// Pad the final component so the path lands exactly at sunPathLimit bytes
	// whatever t.TempDir()'s own base path length is.
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

// Exercises the mutex guarding the handler's shared per-route state (round-1
// review's data race finding). Run with -race: the interleaving is
// non-deterministic, so this only asserts no panic, deadlock, or race.
func TestNew_ConcurrentRequestsNoRace(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: ""}}), nil)
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

// Extends the single-route check to several routes (issue #3176). The
// handler's per-route state maps allocate entries lazily under h.mu the first
// time each route is seen, so this drives concurrent lookup, allocation and
// mutation across three prefixes. Suppressed-miss counts are
// order-dependent, so it only asserts every response is OK, with no race.
func TestNew_ConcurrentRequestsAcrossRoutesNoRace(t *testing.T) {
	const numRoutes = 3
	routes := make([]Route, numRoutes)
	for i := 0; i < numRoutes; i++ {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer upstream.Close()
		routes[i] = Route{MatchHost: fmt.Sprintf("route-%d.example", i), EnforcedPaths: []string{"/"}, Upstream: upstream.URL}
	}
	assigned := AssignPrefixes(routes)

	p, err := New(assigned, nil)
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
			"/"+r.Prefix+"/config.json",
			"/"+r.Prefix+"/api/v1/crates/foo/1.0.0/download",
			"/"+r.Prefix+"/api/v1/crates/foo/1.0.0/download", // the same path again
			"/"+r.Prefix+"/artifactory/api/npm/npm-remote/foo",
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

// Only the TCP transport needs a secret gate (issue #3111): a unix socket's
// filesystem permissions are its equivalent, so ListenAndServe has no such
// check. The countingListener technique is the same one
// TestNew_RejectsNonGetHead_NeverDialsUpstream uses.
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

			handler, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: "real-credential"}}), nil)
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
				req.Header.Set(registrymanifest.TCPSecretHeader, tc.header)
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

func TestListenAndServeTCP_CorrectSecretForwardsToUpstream(t *testing.T) {
	const secret = "s3kr1t-tcp-secret"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("via tcp"))
	}))
	defer upstream.Close()

	handler, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: ""}}), nil)
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
	req.Header.Set(registrymanifest.TCPSecretHeader, secret)

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

// Drives a real TCP request rather than calling ServeHTTP, so the
// credential-isolation guarantee is proven on both transports: the credential
// reaches upstream but never crosses back to the Box on the other end of the
// socket (issue #3111's acceptance criterion).
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

	handler, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: credential}}), nil)
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
	req.Header.Set(registrymanifest.TCPSecretHeader, secret)

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

	// Every header is swept, not just Authorization, in case a future change
	// echoes the credential back under some other name.
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

// An empty secret equals the empty header value every request carries by
// default, so a listener bound with one would accept everything. Fail closed
// instead (issue #3111).
func TestListenAndServeTCP_RejectsEmptySecret_NeverListens(t *testing.T) {
	handler, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: "http://127.0.0.1:1", Credential: ""}}), nil)
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

// The secret gate runs in front of, not instead of, the handler's own method
// check.
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

	handler, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: "s3kr1t"}}), nil)
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
	req.Header.Set(registrymanifest.TCPSecretHeader, secret)

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

// registryroutes.Parse rejects an empty match-host, so no routes file reaches
// this branch. AssignPrefixes is exported and takes Route values from any
// caller, so the synthetic "r<index>" fallback stays the guard against a
// prefix-less route.
func TestAssignPrefixes_EmptyMatchHostFallsBackToIndex(t *testing.T) {
	routes := AssignPrefixes([]Route{
		{MatchHost: "registry-a.example"},
		{MatchHost: ""},
	})
	if got, want := routes[1].Prefix, "r1"; got != want {
		t.Errorf("Prefix = %q, want %q", got, want)
	}
}

// An empty Prefix makes the route unroutable, so New must refuse it rather
// than accept it silently.
func TestNew_RejectsEmptyPrefix(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	if _, err := New([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Prefix: ""}}, nil); err == nil {
		t.Fatal("New with empty Prefix = nil error, want error")
	}
}

// A request naming a shared prefix would have no unique route to select.
func TestNew_RejectsDuplicatePrefix(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	_, err := New([]Route{
		{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Prefix: "dup"},
		{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Prefix: "dup"},
	}, nil)
	if err == nil {
		t.Fatal("New with duplicate Prefix = nil error, want error")
	}
}

// The Prefix becomes the first URL path segment a request selects a route by,
// so a character outside [a-z0-9-] must be refused.
func TestNew_RejectsInvalidPrefixChars(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	for _, prefix := range []string{"Registry", "registry_a", "registry/a", "registry a"} {
		t.Run(prefix, func(t *testing.T) {
			if _, err := New([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Prefix: prefix}}, nil); err == nil {
				t.Fatalf("New with Prefix %q = nil error, want error", prefix)
			}
		})
	}
}

// These three hosts differ only by characters AssignPrefixes maps to the same
// '-'. Dedupe is by table order: the first keeps the bare slug, later ones
// take a numeric suffix.
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

// The third host slugifies to exactly the string AssignPrefixes generates for
// the second one's collision, so a generated prefix must itself be registered
// as used or the two would collide again.
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

func TestAssignPrefixes_SlugFromMatchHost(t *testing.T) {
	routes := AssignPrefixes([]Route{
		{MatchHost: "Registry.Example.COM:8443"},
	})
	if got, want := routes[0].Prefix, "registry-example-com"; got != want {
		t.Errorf("Prefix = %q, want %q", got, want)
	}
}

func TestNew_NeverLogsCredentialForRefusedPath(t *testing.T) {
	const credential = "sekret-token"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: credential}}), nil)
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

// The rewritten "dl" points at the address the client itself used to reach
// the proxy (req.Host), with the route's prefix re-inserted and the dl's own
// path preserved.
func TestModifyResponse_CargoConfigJSON_RewritesDL(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/config.json" {
			t.Errorf("upstream got path %q, want /config.json", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dl":"https://crates.example.com/api/v1/crates"}`))
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", EnforcedPaths: []string{"/"}, EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/"}}, Upstream: upstream.URL}})
	p := newWithEcosystemRows(t, routes)
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

// A dl naming a host other than the route's match-host (a CDN) is relayed
// byte-identical, and exactly one log line records the deliberate skip, an
// acceptance criterion of issue #3175.
func TestModifyResponse_ForeignHostDLLeftAloneAndLogsSkipOnce(t *testing.T) {
	const credential = "s3kr1t-do-not-log-me"
	const body = `{"dl":"https://cdn.example.com/api/v1/crates"}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", EnforcedPaths: []string{"/"}, EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/"}}, Upstream: upstream.URL, Credential: credential}})
	p := newWithEcosystemRows(t, routes)
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
	if !strings.Contains(logged, "https://cdn.example.com/api/v1/crates") {
		t.Errorf("skip log line did not name the skipped dl: %q", logged)
	}
	if strings.Contains(logged, credential) {
		t.Errorf("log output contained the credential: %q", logged)
	}
}

// This ordering cost is intended, not a bug. The Rewrite hook has only the
// request shape to go on, so it forces Accept-Encoding: identity before anyone
// knows whether the dl rewrite will apply. A client asking for gzip therefore
// gets an identity response even when the dl names a foreign host and the
// rewrite is skipped.
func TestModifyResponse_ForeignHostDLGzipRequestGetsIdentityFromUpstream(t *testing.T) {
	const body = `{"dl":"https://cdn.example.com/api/v1/crates"}`

	var gotAcceptEncoding string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAcceptEncoding = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Type", "application/json")
		// This upstream would gzip had the client's Accept-Encoding survived
		// the Rewrite hook; it did not.
		_, _ = w.Write([]byte(body))
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", EnforcedPaths: []string{"/"}, EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/"}}, Upstream: upstream.URL}})
	p := newWithEcosystemRows(t, routes)
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

func TestModifyResponse_NoMatchingRowRelayedByteIdentical(t *testing.T) {
	const body = `{"dl":"https://crates.example.com/api/v1/crates"}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", EnforcedPaths: []string{"/"}, EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/"}}, Upstream: upstream.URL}})
	p := newWithEcosystemRows(t, routes)
	prefix := routes[0].Prefix

	// The cargo download endpoint names no rewrite row, even though this
	// fixture body carries a "dl" field naming the route's own match-host.
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

// Guards issue #2854's wrong-media-type defect: the reverted hook decided by
// sniffing Content-Type and a "dl" field, so any response of that shape got
// rewritten whatever request produced it. This fixture has exactly that
// shape, but its request path names no rewrite row.
func TestModifyResponse_WrongMediaTypeShapeNotMatchedIsUntouched(t *testing.T) {
	const body = `{"dl":"https://crates.example.com/api/v1/crates"}`

	for _, path := range []string{"/api/v1/crates", "/index/co/nf/config"} {
		t.Run(path, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			defer upstream.Close()

			routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", EnforcedPaths: []string{"/"}, EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/"}}, Upstream: upstream.URL}})
			p := newWithEcosystemRows(t, routes)
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

// Guards issue #2854's HEAD-crash defect: a HEAD response has no body for
// ModifyResponse to read, and the reverted hook crashed parsing one anyway.
func TestModifyResponse_HeadForCargoConfigJSONUntouched(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Test", "head-response")
		w.WriteHeader(http.StatusOK)
		// net/http strips any body for a HEAD request, so none is written
		// here even though Content-Type is set. That is the shape a real
		// HEAD /config.json response has.
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", EnforcedPaths: []string{"/"}, EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/"}}, Upstream: upstream.URL}})
	p := newWithEcosystemRows(t, routes)
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

// The credential rides the outbound request, so a rewritten body is the one
// place it could come back to the client.
func TestModifyResponse_RewrittenResponseNeverCarriesCredential(t *testing.T) {
	const credential = "s3kr1t-do-not-leak-me"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dl":"https://crates.example.com/api/v1/crates"}`))
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", EnforcedPaths: []string{"/"}, EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/"}}, Upstream: upstream.URL, Credential: credential}})
	p := newWithEcosystemRows(t, routes)
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

// Blocking review finding on issue #3175: net/http's Transport only
// auto-decompresses a gzip response when it added Accept-Encoding itself, so
// a real cargo client's own header leaves the bytes compressed by the time
// modifyResponse reads them. Before the fix that body failed json.Decode and
// was relayed untouched. The upstream compresses only when asked, like a CDN.
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

	routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", EnforcedPaths: []string{"/"}, EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/"}}, Upstream: upstream.URL}})
	p := newWithEcosystemRows(t, routes)
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

// Only a request shape matching a rewrite row gets forced to "identity" by
// the Rewrite hook, so every other request keeps the client's own
// Accept-Encoding.
func TestNew_NonMatchingShapePreservesClientAcceptEncoding(t *testing.T) {
	var gotAcceptEncoding string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAcceptEncoding = r.Header.Get("Accept-Encoding")
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", EnforcedPaths: []string{"/"}, EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/"}}, Upstream: upstream.URL}})
	p := newWithEcosystemRows(t, routes)
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

// Second half of issue #3175's blocking review finding: a matched row whose
// body held nothing rewritable used to log nothing, making a no-op rewrite
// undiagnosable. The new line names the row and carries neither the
// credential nor any of the body.
func TestModifyResponse_MatchedRowNoRewritableFieldLogsWithoutBodyOrCredential(t *testing.T) {
	const credential = "s3kr1t-do-not-log-me"
	const body = `{"not-a-dl-field":"nothing to rewrite here"}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", EnforcedPaths: []string{"/"}, EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/"}}, Upstream: upstream.URL, Credential: credential}})
	p := newWithEcosystemRows(t, routes)
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

// Closes the other half of issue #3175's blocking review finding: only the
// skip path had a log-capturing test, never the rewrite path itself. The
// fixture's credential and unrelated field are distinctive sentinels so a
// leak cannot hide inside a plausible-looking substring.
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

	routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", EnforcedPaths: []string{"/"}, EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/"}}, Upstream: upstream.URL, Credential: credential}})
	p := newWithEcosystemRows(t, routes)
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

	if got := strings.Count(logged, "cargo config.json: rewrote"); got != 1 {
		t.Errorf(`"cargo config.json: rewrote" occurred %d times in log output, want exactly 1: %q`, got, logged)
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

// An HTTP/1.0 client sending no Host header leaves req.Host empty, so
// ServeHTTP never sets selectedRoute.forwarder and modifyResponse must relay
// the body rather than dereference the nil *url.URL.
func TestModifyResponse_NilForwarderRelaysConfigJSONUnrewritten(t *testing.T) {
	const body = `{"dl":"https://crates.example.com/api/v1/crates"}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", EnforcedPaths: []string{"/"}, EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/"}}, Upstream: upstream.URL}})
	p := newWithEcosystemRows(t, routes)
	prefix := routes[0].Prefix

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/config.json", nil)
	req.Host = "" // no Host header, so selectedRoute.forwarder stays nil
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Body.String(); got != body {
		t.Errorf("body = %s, want byte-identical to upstream's %s (nil forwarder must skip rewriting, not crash)", got, body)
	}
}

// An error page arriving at the config.json shape is not a config.json
// document, so it is relayed with its status rather than parsed and
// rewritten.
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

			routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", EnforcedPaths: []string{"/"}, EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/"}}, Upstream: upstream.URL}})
			p := newWithEcosystemRows(t, routes)
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

// A body over maxRewriteBodyBytes must reach the client whole, which is why
// bodyWithClose splices the buffered prefix back onto the unread remainder
// instead of truncating. The marker straddles the cap offset itself, so an
// off-by-one at the splice join lands on top of it rather than hiding in the
// filler.
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

	routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", EnforcedPaths: []string{"/"}, EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/"}}, Upstream: upstream.URL}})
	p := newWithEcosystemRows(t, routes)
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

// The upstream declares a Content-Length it never delivers and closes
// mid-body, so io.ReadAll inside modifyResponse errors. The client must see a
// 502 rather than a hang or a panic.
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
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n{\"dl\":")
		_ = buf.Flush()
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{MatchHost: "crates.example.com", EnforcedPaths: []string{"/"}, EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/"}}, Upstream: upstream.URL}})
	p := newWithEcosystemRows(t, routes)
	prefix := routes[0].Prefix

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/config.json", nil)
	req.Host = "forwarder.example:9999"
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d (body read error must become 502, not a hang or panic)", rr.Code, http.StatusBadGateway)
	}
}

// captureLog redirects the standard logger into a buffer for the rest of t.
// The package under test logs through the standard logger with no injectable
// seam, so this swap is process-wide: no test using it may call t.Parallel().
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prevOutput) })
	return &buf
}

// The line names the route prefix, method, route-relative path and status, so
// it stays distinguishable from the transport-error line (issue #3125).
func TestNew_LogsUpstreamFailureStatus(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			}))
			defer upstream.Close()

			p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: ""}}), nil)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			logBuf := captureLog(t)

			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/r0/config.json", nil)
			p.ServeHTTP(rr, req)

			if rr.Code != status {
				t.Fatalf("status = %d, want %d", rr.Code, status)
			}
			logged := logBuf.String()
			want := fmt.Sprintf("registryproxy: r0: upstream error status: %s /config.json %d", http.MethodGet, status)
			if got := strings.Count(logged, want); got != 1 {
				t.Errorf("log output = %q, want exactly one line %q", logged, want)
			}
		})
	}
}

func TestNew_NoLogForSuccessfulUpstreamStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: ""}}), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	logBuf := captureLog(t)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/config.json", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if strings.Contains(logBuf.String(), "upstream error status") {
		t.Errorf("log output = %q, want no upstream error status line for a 200", logBuf.String())
	}
}

// Pins ADR 0044's single-hop behaviour for the failure log line too: a
// redirect is not a failure, so it is relayed intact and never logged as one.
func TestNew_RedirectStatusNeitherLoggedNorFollowed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://cdn.example.com/artifact")
		w.WriteHeader(http.StatusFound)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: ""}}), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	logBuf := captureLog(t)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/config.json", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d (single-hop: redirect relayed, never followed)", rr.Code, http.StatusFound)
	}
	if got := rr.Header().Get("Location"); got != "https://cdn.example.com/artifact" {
		t.Errorf("Location = %q, want it relayed unchanged", got)
	}
	if strings.Contains(logBuf.String(), "upstream error status") {
		t.Errorf("log output = %q, want no upstream error status line for a 3xx", logBuf.String())
	}
}

// The failure log line is observation only, never a mutation of what the
// client receives.
func TestNew_UpstreamFailureRelayedByteIdentical(t *testing.T) {
	const body = "not found here"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream-Marker", "present")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(body))
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: ""}}), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/config.json", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
	if got := rr.Body.String(); got != body {
		t.Errorf("body = %q, want %q", got, body)
	}
	if got := rr.Header().Get("X-Upstream-Marker"); got != "present" {
		t.Errorf("X-Upstream-Marker = %q, want it relayed unchanged", got)
	}
}

// The dedup rule (issue #3125): the first failing path logs in full, later
// distinct ones are suppressed until Close flushes their summary, and a
// repeat of the first is neither re-logged nor double-counted.
func TestNew_SuppressesRepeatedUpstreamFailures(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: ""}}), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	logBuf := captureLog(t)

	paths := []string{
		"/r0/config.json", // first failure, logged in full
		"/r0/other.json",  // second distinct failure, suppressed
		"/r0/third.json",  // third distinct failure, suppressed
		"/r0/config.json", // repeat of the first, neither logged nor counted
	}
	for _, path := range paths {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		p.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
		}
	}

	logged := logBuf.String()
	if got := strings.Count(logged, "registryproxy: r0: upstream error status:"); got != 1 {
		t.Errorf("detailed failure log appeared %d times, want exactly 1: %q", got, logged)
	}

	closer, ok := p.(interface{ Close() })
	if !ok {
		t.Fatalf("handler returned by New does not implement Close()")
	}
	closer.Close()

	logged = logBuf.String()
	want := "registryproxy: r0: suppressed 2 further distinct upstream failures"
	if !strings.Contains(logged, want) {
		t.Errorf("log output = %q, want it to contain %q", logged, want)
	}
}

// Each route accumulates and flushes its own failure state, and the summaries
// come out in route-table order (issue #3125).
func TestNew_UpstreamFailuresArePerRoute(t *testing.T) {
	upstreamA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstreamA.Close()
	upstreamB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstreamB.Close()

	p, err := New(AssignPrefixes([]Route{
		{EnforcedPaths: []string{"/"}, Upstream: upstreamA.URL, Credential: ""},
		{EnforcedPaths: []string{"/"}, Upstream: upstreamB.URL, Credential: ""},
	}), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	proxy := &Proxy{Handler: p}

	logBuf := captureLog(t)

	requests := []string{
		"/r0/config.json",
		"/r0/other.json",
		"/r1/config.json",
		"/r1/other.json",
		"/r1/third.json",
	}
	for _, path := range requests {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		proxy.Handler.ServeHTTP(rr, req)
	}

	if err := proxy.Close(); err != nil {
		t.Fatalf("proxy.Close() = %v, want nil (listener was never started)", err)
	}

	logged := logBuf.String()
	wantR0First := "registryproxy: r0: upstream error status: GET /config.json 404"
	wantR0Summary := "registryproxy: r0: suppressed 1 further distinct upstream failure"
	wantR1First := "registryproxy: r1: upstream error status: GET /config.json 500"
	wantR1Summary := "registryproxy: r1: suppressed 2 further distinct upstream failures"
	for _, want := range []string{wantR0First, wantR0Summary, wantR1First, wantR1Summary} {
		if !strings.Contains(logged, want) {
			t.Errorf("log output = %q, want it to contain %q", logged, want)
		}
	}
	if gotR0, gotR1 := strings.Index(logged, wantR0Summary), strings.Index(logged, wantR1Summary); gotR0 > gotR1 {
		t.Errorf("r0's summary (at %d) logged after r1's (at %d), want route-table order", gotR0, gotR1)
	}
}

func TestNew_NeverLogsCredentialForUpstreamFailure(t *testing.T) {
	const credential = "s3kr1t-do-not-log-me-either"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: credential}}), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	logBuf := captureLog(t)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/config.json", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
	if strings.Contains(logBuf.String(), credential) {
		t.Errorf("log output contained the credential: %q", logBuf.String())
	}
}

// An unreachable upstream logs its own line naming the method and
// route-relative path, distinct from the HTTP-status one, and the client
// still gets ReverseProxy's usual 502.
func TestNew_LogsUpstreamTransportFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	upstreamURL := upstream.URL
	upstream.Close() // now refuses connections

	p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstreamURL, Credential: ""}}), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	logBuf := captureLog(t)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/config.json", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadGateway)
	}
	logged := logBuf.String()
	want := fmt.Sprintf("registryproxy: r0: upstream request failed: %s /config.json:", http.MethodGet)
	if got := strings.Count(logged, want); got != 1 {
		t.Errorf("log output = %q, want exactly one line starting %q", logged, want)
	}
	if strings.Contains(logged, "upstream error status") {
		t.Errorf("log output = %q, want no upstream error status line for a transport failure", logged)
	}
}

// One route answers 401, the other is unreachable. The HTTP-status line names
// a status code and the transport line never does.
func TestNew_DistinguishesTransportFailureFromHTTPStatusFailure(t *testing.T) {
	upstreamA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstreamA.Close()

	upstreamB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	upstreamBURL := upstreamB.URL
	upstreamB.Close()

	p, err := New(AssignPrefixes([]Route{
		{EnforcedPaths: []string{"/"}, Upstream: upstreamA.URL, Credential: ""},
		{EnforcedPaths: []string{"/"}, Upstream: upstreamBURL, Credential: ""},
	}), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	logBuf := captureLog(t)

	rrA := httptest.NewRecorder()
	p.ServeHTTP(rrA, httptest.NewRequest(http.MethodGet, "/r0/config.json", nil))
	rrB := httptest.NewRecorder()
	p.ServeHTTP(rrB, httptest.NewRequest(http.MethodGet, "/r1/config.json", nil))

	logged := logBuf.String()
	wantA := "registryproxy: r0: upstream error status: GET /config.json 401"
	wantB := fmt.Sprintf("registryproxy: r1: upstream request failed: %s /config.json:", http.MethodGet)
	if !strings.Contains(logged, wantA) {
		t.Errorf("log output = %q, want it to contain %q", logged, wantA)
	}
	if !strings.Contains(logged, wantB) {
		t.Errorf("log output = %q, want it to contain %q", logged, wantB)
	}
}

// http.Transport.RoundTrip's error never echoes request headers, so the
// credential cannot reach the transport-failure line through %v either.
func TestNew_NeverLogsCredentialForTransportFailure(t *testing.T) {
	const credential = "s3kr1t-do-not-log-me-transport"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	upstreamURL := upstream.URL
	upstream.Close() // now refuses connections

	p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstreamURL, Credential: credential}}), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	logBuf := captureLog(t)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/config.json", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadGateway)
	}
	if strings.Contains(logBuf.String(), credential) {
		t.Errorf("log output contained the credential: %q", logBuf.String())
	}
}

// Both failure legs on one route share a single routeFailureState: whichever
// happens first logs in full, the other is suppressed, and Close's teardown
// summary is one count covering both.
func TestNew_SharesSuppressionAcrossTransportAndStatusFailures(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	upstreamURL := upstream.URL

	p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstreamURL, Credential: ""}}), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	logBuf := captureLog(t)

	rr1 := httptest.NewRecorder()
	p.ServeHTTP(rr1, httptest.NewRequest(http.MethodGet, "/r0/config.json", nil))
	if rr1.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr1.Code, http.StatusNotFound)
	}

	upstream.Close() // now refuses connections, for the transport leg below

	rr2 := httptest.NewRecorder()
	p.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/r0/other.json", nil))
	if rr2.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rr2.Code, http.StatusBadGateway)
	}

	logged := logBuf.String()
	if got := strings.Count(logged, "registryproxy: r0: upstream error status:"); got != 1 {
		t.Errorf("HTTP-status log appeared %d times, want exactly 1: %q", got, logged)
	}
	if strings.Contains(logged, "upstream request failed") {
		t.Errorf("log output = %q, want the transport failure suppressed, not logged in full", logged)
	}

	closer, ok := p.(interface{ Close() })
	if !ok {
		t.Fatalf("handler returned by New does not implement Close()")
	}
	closer.Close()

	logged = logBuf.String()
	want := "registryproxy: r0: suppressed 1 further distinct upstream failure"
	if !strings.Contains(logged, want) {
		t.Errorf("log output = %q, want it to contain %q (covering the transport failure suppressed after the HTTP-status one logged in full)", logged, want)
	}
}

func TestNew_NoTransportOrStatusLogForSuccessfulRequest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: ""}}), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	logBuf := captureLog(t)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/config.json", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	logged := logBuf.String()
	if strings.Contains(logged, "upstream error status") || strings.Contains(logged, "upstream request failed") {
		t.Errorf("log output = %q, want no failure line for a 200", logged)
	}
}

// A client hanging up mid-request is routine under ecosystem-client
// parallelism and timeouts. The cancellation reaches ErrorHandler as
// context.Canceled, but nothing upstream failed, so nothing is logged.
func TestNew_ClientAbortNotLoggedAsUpstreamFailure(t *testing.T) {
	received := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(received)
		<-r.Context().Done()
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: ""}}), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	logBuf := captureLog(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-received
		cancel()
	}()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/config.json", nil).WithContext(ctx)
	p.ServeHTTP(rr, req)

	logged := logBuf.String()
	if strings.Contains(logged, "upstream request failed") || strings.Contains(logged, "upstream error status") {
		t.Errorf("log output = %q, want no failure line for a client abort", logged)
	}
}

// Issue #3125's motivating case: a client abort must not consume the route's
// single full-detail failure slot, so the 401 that follows still reports its
// method, path and status rather than an anonymous suppressed count.
func TestNew_ClientAbortLeavesFirstFailureSlotForGenuineFailure(t *testing.T) {
	received := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hang" {
			close(received)
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{EnforcedPaths: []string{"/"}, Upstream: upstream.URL, Credential: ""}}), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	logBuf := captureLog(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-received
		cancel()
	}()

	rrAbort := httptest.NewRecorder()
	p.ServeHTTP(rrAbort, httptest.NewRequest(http.MethodGet, "/r0/hang", nil).WithContext(ctx))

	rr401 := httptest.NewRecorder()
	p.ServeHTTP(rr401, httptest.NewRequest(http.MethodGet, "/r0/crates/foo", nil))

	if rr401.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr401.Code, http.StatusUnauthorized)
	}
	logged := logBuf.String()
	want := "registryproxy: r0: upstream error status: GET /crates/foo 401"
	if !strings.Contains(logged, want) {
		t.Errorf("log output = %q, want it to contain the full first-failure line %q", logged, want)
	}
	if strings.Contains(logged, "upstream request failed") {
		t.Errorf("log output = %q, want no transport failure line for a client abort", logged)
	}
}
