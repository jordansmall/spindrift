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

// TestNew_ThreadsCargoIndexBasesWithoutError verifies New accepts a
// host-rooted Route carrying CargoIndexBases and forwards an ordinary
// request normally -- a request that never touches config.json doesn't
// exercise the field at all (see findResponseRewriteRow for where it does).
func TestNew_ThreadsCargoIndexBasesWithoutError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{
		Upstream:        upstream.URL,
		HostRooted:      true,
		EnforcedPaths:   []string{"/index-a"},
		CargoIndexBases: []string{"/index-a"},
	}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/index-a/config.json", nil)
	p.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
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

// TestHostRooted_ConfigJSONRewrittenPerCargoIndexBase covers issue #3257: a
// host-rooted route with two cargo index bases rewrites the config.json
// served under either base, even though its own dl is a
// sibling of the index base rather than nested under it (the Artifactory
// layout) -- proving the row now matches per declared index base rather
// than only the bare "/config.json" literal a single-index route matches.
func TestHostRooted_ConfigJSONRewrittenPerCargoIndexBase(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/index-a/config.json":
			_, _ = w.Write([]byte(`{"dl":"https://crates.example.com/api/v1/crates-a"}`))
		case "/index-b/config.json":
			_, _ = w.Write([]byte(`{"dl":"https://crates.example.com/api/v1/crates-b"}`))
		default:
			t.Errorf("upstream got unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{
		MatchHost:       "crates.example.com",
		Upstream:        upstream.URL,
		HostRooted:      true,
		EnforcedPaths:   []string{"/index-a", "/index-b"},
		CargoIndexBases: []string{"/index-a", "/index-b"},
	}})
	p, err := New(routes)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	prefix := routes[0].Prefix

	tests := []struct {
		path     string
		wantBody string
	}{
		{"/" + prefix + "/index-a/config.json", `{"dl":"http://forwarder.example:9999/` + prefix + `/api/v1/crates-a"}`},
		{"/" + prefix + "/index-b/config.json", `{"dl":"http://forwarder.example:9999/` + prefix + `/api/v1/crates-b"}`},
	}
	for _, tc := range tests {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Host = "forwarder.example:9999"
		p.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want %d", tc.path, rr.Code, http.StatusOK)
		}
		if got := rr.Body.String(); got != tc.wantBody {
			t.Errorf("%s: body = %s, want %s", tc.path, got, tc.wantBody)
		}
	}
}

// TestHostRooted_ConfigJSONRewrittenWithDLNestedUnderIndexBase covers the
// same per-index-base row match as
// TestHostRooted_ConfigJSONRewrittenPerCargoIndexBase, but for the Gitea
// layout, where dl nests under the index base rather than sitting beside
// it -- proving the match rule is layout-agnostic (rewriteCargoDL's own
// stripBasePath logic, not the row match, is what decides whether a given
// dl is rewritable).
func TestHostRooted_ConfigJSONRewrittenWithDLNestedUnderIndexBase(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/index-a/config.json" {
			t.Errorf("upstream got unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"dl":"https://crates.example.com/index-a/api/v1/crates"}`))
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{
		MatchHost:       "crates.example.com",
		Upstream:        upstream.URL,
		HostRooted:      true,
		EnforcedPaths:   []string{"/index-a"},
		CargoIndexBases: []string{"/index-a"},
	}})
	p, err := New(routes)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	prefix := routes[0].Prefix

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/index-a/config.json", nil)
	req.Host = "forwarder.example:9999"
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	wantBody := `{"dl":"http://forwarder.example:9999/` + prefix + `/index-a/api/v1/crates"}`
	if got := rr.Body.String(); got != wantBody {
		t.Errorf("body = %s, want %s", got, wantBody)
	}
}

// TestHostRooted_PathResemblingConfigJSONNotMatchedAsRow verifies there is
// no suffix-guessing: a request path that merely resembles
// "<base>/config.json" but doesn't equal it exactly is never treated as a
// config.json row match, so its body -- even a JSON object with its own
// "dl" field -- is relayed byte-identical.
func TestHostRooted_PathResemblingConfigJSONNotMatchedAsRow(t *testing.T) {
	const wantBody = `{"dl":"https://crates.example.com/some/other/thing"}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index-a/config-json-wannabe" {
			t.Errorf("upstream got unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(wantBody))
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{
		MatchHost:       "crates.example.com",
		Upstream:        upstream.URL,
		HostRooted:      true,
		EnforcedPaths:   []string{"/index-a"},
		CargoIndexBases: []string{"/index-a"},
	}})
	p, err := New(routes)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	prefix := routes[0].Prefix

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/index-a/config-json-wannabe", nil)
	req.Host = "forwarder.example:9999"
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Body.String(); got != wantBody {
		t.Errorf("body = %s, want byte-identical relay of %s (no suffix-guessing)", got, wantBody)
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
