package registryproxy

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestNew_HostRootedRejectsUpstreamWithPath verifies New refuses a
// host-rooted route whose Upstream carries a path: the join a host-rooted
// route relies on (see the Rewrite hook) only forwards the verbatim
// remainder when Upstream has none, so a path there would silently prefix
// every forwarded request.
func TestNew_HostRootedRejectsUpstreamWithPath(t *testing.T) {
	_, err := New(AssignPrefixes([]Route{{
		Upstream:      "https://example.com/artifactory",
		HostRooted:    true,
		EnforcedPaths: []string{"/"},
	}}))
	if err == nil {
		t.Fatal("New: got nil error, want an error naming the path on a host-rooted Upstream")
	}
}

// TestHostRooted_ForwardsVerbatimRemainderForEachEnforcedSubtree covers
// issue #3256 AC 1: a host-rooted route with two enforced cargo index
// subtrees on one host forwards a request under either subtree to the
// upstream origin at the verbatim remaining path, with the route credential
// attached.
func TestHostRooted_ForwardsVerbatimRemainderForEachEnforcedSubtree(t *testing.T) {
	var gotPaths []string
	var gotAuths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		gotAuths = append(gotAuths, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{
		Upstream:      upstream.URL,
		Credential:    "s3kr1t",
		HostRooted:    true,
		EnforcedPaths: []string{"/index-a", "/index-b"},
	}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for _, path := range []string{"/r0/index-a/config.json", "/r0/index-b/xy/zz/foo"} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		p.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want %d", path, rr.Code, http.StatusOK)
		}
	}

	wantPaths := []string{"/index-a/config.json", "/index-b/xy/zz/foo"}
	if len(gotPaths) != len(wantPaths) {
		t.Fatalf("upstream saw %d requests, want %d", len(gotPaths), len(wantPaths))
	}
	for i, want := range wantPaths {
		if gotPaths[i] != want {
			t.Errorf("request %d: upstream got path %q, want %q", i, gotPaths[i], want)
		}
		if want := "Bearer s3kr1t"; gotAuths[i] != want {
			t.Errorf("request %d: upstream got Authorization %q, want %q", i, gotAuths[i], want)
		}
	}
}

// TestHostRooted_RefusesPathOutsideEnforcedSet covers issue #3256 AC 2: a
// request outside the enforced set is answered 403, the fake upstream
// records zero requests for it, and the body names the refusing policy and
// lists the enforced paths.
func TestHostRooted_RefusesPathOutsideEnforcedSet(t *testing.T) {
	var upstreamRequests int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequests++
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{
		Upstream:      upstream.URL,
		Credential:    "s3kr1t",
		HostRooted:    true,
		EnforcedPaths: []string{"/index-a", "/index-b"},
	}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/some-other-path/config.json", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
	if upstreamRequests != 0 {
		t.Errorf("upstream recorded %d requests, want 0", upstreamRequests)
	}
	body, err := io.ReadAll(rr.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !strings.Contains(string(body), "host-rooted") {
		t.Errorf("body = %q, want it to name the refusing policy", string(body))
	}
	if !strings.Contains(string(body), "/index-a") || !strings.Contains(string(body), "/index-b") {
		t.Errorf("body = %q, want it to list the enforced paths", string(body))
	}
}

// TestHostRooted_RefusalNeverDialsUpstream verifies a 403 refusal on a
// host-rooted route never dials upstream at the TCP level, mirroring
// TestNew_EnforcesAllowlistNeverDialsUpstream's proof for the legacy
// enforce-allowlist policy.
func TestHostRooted_RefusalNeverDialsUpstream(t *testing.T) {
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

	p, err := New(AssignPrefixes([]Route{{
		Upstream:      upstream.URL,
		Credential:    "s3kr1t",
		HostRooted:    true,
		EnforcedPaths: []string{"/index-a"},
	}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/not-enforced/config.json", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
	if got := atomic.LoadInt32(&cl.accepts); got != 0 {
		t.Errorf("upstream listener accepted %d connections, want 0", got)
	}
}

// TestHostRooted_EmptyEnforcedPathsRefusesEverything verifies a host-rooted
// route whose derived path-set is legitimately empty fails closed rather
// than falling back to some permissive default -- emptiness must never read
// as "no policy configured".
func TestHostRooted_EmptyEnforcedPathsRefusesEverything(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{
		Upstream:   upstream.URL,
		HostRooted: true,
	}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/anything", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
}

// TestHostRooted_RootSubtreeAdmitsWholeHost verifies an EnforcedPaths entry
// of "/" -- registrypathset.Subtree's "the whole host" sentinel -- admits
// every path on a host-rooted route, mirroring
// registrypathset.HostPathSet.Admits's own root-subtree rule.
func TestHostRooted_RootSubtreeAdmitsWholeHost(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{
		Upstream:      upstream.URL,
		HostRooted:    true,
		EnforcedPaths: []string{"/"},
	}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/anything/at/all", nil)
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
}

// TestNew_StripsInboundAuthorization covers issue #3256 AC 3: the inbound
// client's own Authorization header never reaches upstream, whether it is
// replaced by an authenticated route's credential, deleted outright on an
// unauthenticated pass-through route, or the route attaches its credential
// to a different header entirely (AuthScheme "header:<Name>").
func TestNew_StripsInboundAuthorization(t *testing.T) {
	tests := []struct {
		name       string
		route      Route
		wantAuth   string
		wantHeader map[string]string // additional headers expected on the upstream request
	}{
		{
			name:     "authenticated route replaces inbound Authorization with its own credential",
			route:    Route{Credential: "s3kr1t"},
			wantAuth: "Bearer s3kr1t",
		},
		{
			name:     "unauthenticated pass-through route deletes inbound Authorization",
			route:    Route{Credential: ""},
			wantAuth: "",
		},
		{
			name:       "header scheme route deletes inbound Authorization, attaches its own header",
			route:      Route{AuthScheme: "header:X-Api-Key", Credential: "s3kr1t"},
			wantAuth:   "",
			wantHeader: map[string]string{"X-Api-Key": "s3kr1t"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got http.Header
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Clone()
				w.WriteHeader(http.StatusOK)
			}))
			defer upstream.Close()

			tc.route.Upstream = upstream.URL
			p, err := New(AssignPrefixes([]Route{tc.route}))
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/r0/config.json", nil)
			req.Header.Set("Authorization", "Bearer inbound-client-token")
			p.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
			}
			if gotAuth := got.Get("Authorization"); gotAuth != tc.wantAuth {
				t.Errorf("upstream got Authorization %q, want %q", gotAuth, tc.wantAuth)
			}
			for name, want := range tc.wantHeader {
				if gotVal := got.Get(name); gotVal != want {
					t.Errorf("upstream got %s %q, want %q", name, gotVal, want)
				}
			}
		})
	}
}
