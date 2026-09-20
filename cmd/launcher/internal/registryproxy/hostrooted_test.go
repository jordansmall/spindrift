package registryproxy

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"spindrift.dev/launcher/internal/bindregistry"
	"spindrift.dev/launcher/internal/registryvocab"
)

// A host-rooted route forwards the verbatim remainder only when Upstream
// carries no path of its own (see the Rewrite hook), so New refuses one that
// does: the path would silently prefix every forwarded request.
func TestNew_HostRootedRejectsUpstreamWithPath(t *testing.T) {
	_, err := New(AssignPrefixes([]Route{{
		Upstream: "https://example.com/artifactory",

		EnforcedPaths: []string{"/"},
	}}), nil)
	if err == nil {
		t.Fatal("New: got nil error, want an error naming the path on a host-rooted Upstream")
	}
}

// A request that never touches config.json does not exercise EnforcedSubtrees
// at all (findResponseRewriteRow is where it does), so this pins only that New
// accepts the field and still forwards normally.
func TestNew_ThreadsEnforcedSubtreesWithoutError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, _ := newWithEcosystemRows(t, Route{
		Upstream: upstream.URL,

		EnforcedPaths:    []string{"/index-a"},
		EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/index-a"}},
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r0/index-a/config.json", nil)
	p.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
}

// Issue #3256 AC 1: a host-rooted route with two enforced cargo index subtrees
// on one host forwards a request under either subtree to the upstream origin
// at the verbatim remaining path, with the route credential attached.
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
		Upstream:   upstream.URL,
		Credential: "s3kr1t",

		EnforcedPaths: []string{"/index-a", "/index-b"},
	}}), nil)
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

// Issue #3256 AC 2: a request outside the enforced set gets a 403, the fake
// upstream records no request for it, and the body names the refusing policy
// and lists the enforced paths.
func TestHostRooted_RefusesPathOutsideEnforcedSet(t *testing.T) {
	var upstreamRequests int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequests++
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{
		Upstream:   upstream.URL,
		Credential: "s3kr1t",

		EnforcedPaths: []string{"/index-a", "/index-b"},
	}}), nil)
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
	if !strings.Contains(string(body), "enforcement refused") {
		t.Errorf("body = %q, want it to name the refusing policy", string(body))
	}
	if !strings.Contains(string(body), "/index-a") || !strings.Contains(string(body), "/index-b") {
		t.Errorf("body = %q, want it to list the enforced paths", string(body))
	}
}

// Enforcement runs before the proxy commits to forwarding anything, so a 403
// refusal never dials upstream at the TCP level.
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
		Upstream:   upstream.URL,
		Credential: "s3kr1t",

		EnforcedPaths: []string{"/index-a"},
	}}), nil)
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

// A host-rooted route whose derived path-set is legitimately empty must fail
// closed rather than fall back to a permissive default. Emptiness never reads
// as "no policy configured".
func TestHostRooted_EmptyEnforcedPathsRefusesEverything(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{
		Upstream: upstream.URL,
	}}), nil)
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

// An EnforcedPaths entry of "/" is registryvocab.Subtree's whole-host
// sentinel, so it admits every path on a host-rooted route, mirroring
// registryvocab.PathSet.Admits's own root-subtree rule.
func TestHostRooted_RootSubtreeAdmitsWholeHost(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{
		Upstream: upstream.URL,

		EnforcedPaths: []string{"/"},
	}}), nil)
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

// Issue #3257: a host-rooted route with two cargo index bases rewrites the
// config.json served under either base, even where dl is a sibling of the
// index base rather than nested under it (the Artifactory layout). The row
// matches per declared index base, not just the bare "/config.json" literal a
// single-index route matches.
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

	p, routes := newWithEcosystemRows(t, Route{
		MatchHost: "crates.example.com",
		Upstream:  upstream.URL,

		EnforcedPaths:    []string{"/index-a", "/index-b"},
		EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/index-a"}, {Ecosystem: "cargo", Path: "/index-b"}},
	})
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

// The same per-index-base row match as
// TestHostRooted_ConfigJSONRewrittenPerCargoIndexBase, for the Gitea layout
// where dl nests under the index base instead of sitting beside it. The match
// rule is layout-agnostic; rewriteCargoDL's host check, not the row match,
// decides whether a given dl is rewritable.
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

	p, routes := newWithEcosystemRows(t, Route{
		MatchHost: "crates.example.com",
		Upstream:  upstream.URL,

		EnforcedPaths:    []string{"/index-a"},
		EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/index-a"}},
	})
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

// No suffix-guessing: a path that resembles "<base>/config.json" without
// equalling it is never a config.json row match, so its body is relayed
// byte-identical even when it is a JSON object with its own "dl" field.
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

	p, routes := newWithEcosystemRows(t, Route{
		MatchHost: "crates.example.com",
		Upstream:  upstream.URL,

		EnforcedPaths:    []string{"/index-a"},
		EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/index-a"}},
	})
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

// Issue #3256 AC 3: the inbound client's own Authorization header never
// reaches upstream, whether an authenticated route replaces it, an
// unauthenticated pass-through route deletes it, or the route attaches its
// credential to another header (AuthScheme "header:<Name>").
func TestNew_StripsInboundAuthorization(t *testing.T) {
	tests := []struct {
		name       string
		route      Route
		wantAuth   string
		wantHeader map[string]string
	}{
		{
			name:     "authenticated route replaces inbound Authorization with its own credential",
			route:    Route{Credential: "s3kr1t", EnforcedPaths: []string{"/"}},
			wantAuth: "Bearer s3kr1t",
		},
		{
			name:     "unauthenticated pass-through route deletes inbound Authorization",
			route:    Route{Credential: "", EnforcedPaths: []string{"/"}},
			wantAuth: "",
		},
		{
			name:       "header scheme route deletes inbound Authorization, attaches its own header",
			route:      Route{AuthScheme: "header:X-Api-Key", Credential: "s3kr1t", EnforcedPaths: []string{"/"}},
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
			p, err := New(AssignPrefixes([]Route{tc.route}), nil)
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

// Issue #3257 AC 1/2/3: once a config.json rewrite learns a same-host dl
// subtree sitting beside the cargo index base rather than under it (the
// Artifactory layout), a later download into that subtree is admitted even
// though it was never in the route's static EnforcedPaths.
func TestHostRooted_LearnedDLBaseAdmitsDownloadSiblingShape(t *testing.T) {
	var downloadRequests int
	var downloadHeaders http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index-a/config.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"dl":"https://crates.example.com/api/v1/crates"}`))
		case "/api/v1/crates/foo/1.0/download":
			downloadRequests++
			downloadHeaders = r.Header.Clone()
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("upstream got unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	p, routes := newWithEcosystemRows(t, Route{
		MatchHost: "crates.example.com",
		Upstream:  upstream.URL,

		EnforcedPaths:    []string{"/index-a"},
		EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/index-a"}},
		Credential:       "s3kr1t",
	})
	prefix := routes[0].Prefix

	configReq := httptest.NewRequest(http.MethodGet, "/"+prefix+"/index-a/config.json", nil)
	configReq.Host = "forwarder.example:9999"
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, configReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("config.json fetch: status = %d, want %d", rr.Code, http.StatusOK)
	}

	downloadReq := httptest.NewRequest(http.MethodGet, "/"+prefix+"/api/v1/crates/foo/1.0/download", nil)
	downloadReq.Host = "forwarder.example:9999"
	rr = httptest.NewRecorder()
	p.ServeHTTP(rr, downloadReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("download: status = %d, want %d", rr.Code, http.StatusOK)
	}
	if downloadRequests != 1 {
		t.Errorf("upstream recorded %d download requests, want 1", downloadRequests)
	}
	if gotAuth := downloadHeaders.Get("Authorization"); gotAuth != "Bearer s3kr1t" {
		t.Errorf("download request Authorization = %q, want %q", gotAuth, "Bearer s3kr1t")
	}
}

// The Gitea layout version of
// TestHostRooted_LearnedDLBaseAdmitsDownloadSiblingShape, where dl nests under
// the cargo index base instead of sitting beside it, so the learning path is
// layout-agnostic in the same way the rewrite itself is.
func TestHostRooted_LearnedDLBaseAdmitsDownloadNestedShape(t *testing.T) {
	var downloadRequests int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index-a/config.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"dl":"https://crates.example.com/index-a/api/v1/crates"}`))
		case "/index-a/api/v1/crates/foo/1.0/download":
			downloadRequests++
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("upstream got unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	p, routes := newWithEcosystemRows(t, Route{
		MatchHost: "crates.example.com",
		Upstream:  upstream.URL,

		EnforcedPaths:    []string{"/index-a"},
		EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/index-a"}},
	})
	prefix := routes[0].Prefix

	configReq := httptest.NewRequest(http.MethodGet, "/"+prefix+"/index-a/config.json", nil)
	configReq.Host = "forwarder.example:9999"
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, configReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("config.json fetch: status = %d, want %d", rr.Code, http.StatusOK)
	}

	downloadReq := httptest.NewRequest(http.MethodGet, "/"+prefix+"/index-a/api/v1/crates/foo/1.0/download", nil)
	downloadReq.Host = "forwarder.example:9999"
	rr = httptest.NewRecorder()
	p.ServeHTTP(rr, downloadReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("download: status = %d, want %d", rr.Code, http.StatusOK)
	}
	if downloadRequests != 1 {
		t.Errorf("upstream recorded %d download requests, want 1", downloadRequests)
	}
}

// Issue #3257 AC 5: a download path requested before any config.json fetch
// could learn its dl subtree is refused with 403, and the fake upstream
// records no request.
func TestHostRooted_DownloadRefusedBeforeConfigJSONFetched(t *testing.T) {
	var upstreamRequests int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequests++
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, routes := newWithEcosystemRows(t, Route{
		MatchHost: "crates.example.com",
		Upstream:  upstream.URL,

		EnforcedPaths:    []string{"/index-a"},
		EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/index-a"}},
	})
	prefix := routes[0].Prefix

	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/api/v1/crates/foo/1.0/download", nil)
	req.Host = "forwarder.example:9999"
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
	if upstreamRequests != 0 {
		t.Errorf("upstream recorded %d requests, want 0", upstreamRequests)
	}
}

// Issue #3257 AC 4: a config.json response naming a dl on a host other than
// the route's match-host is relayed unrewritten, and the route learns nothing
// from it, so a later request to what would have been the dl's path is still
// refused.
func TestHostRooted_CrossHostDLNeverLearned(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index-a/config.json" {
			t.Errorf("upstream got unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dl":"https://other.example.com/api/v1/crates"}`))
	}))
	defer upstream.Close()

	p, routes := newWithEcosystemRows(t, Route{
		MatchHost: "crates.example.com",
		Upstream:  upstream.URL,

		EnforcedPaths:    []string{"/index-a"},
		EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/index-a"}},
	})
	prefix := routes[0].Prefix

	configReq := httptest.NewRequest(http.MethodGet, "/"+prefix+"/index-a/config.json", nil)
	configReq.Host = "forwarder.example:9999"
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, configReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("config.json fetch: status = %d, want %d", rr.Code, http.StatusOK)
	}

	downloadReq := httptest.NewRequest(http.MethodGet, "/"+prefix+"/api/v1/crates/foo/1.0/download", nil)
	downloadReq.Host = "forwarder.example:9999"
	rr = httptest.NewRecorder()
	p.ServeHTTP(rr, downloadReq)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("download: status = %d, want %d (a cross-host dl must never be learned)", rr.Code, http.StatusForbidden)
	}
}

// Issue #3257 AC 4: two cargo registries sharing one host each accumulate
// their own dl subtree independently, and a third path neither config.json
// ever named is still refused.
func TestHostRooted_TwoIndexBasesLearnIndependently(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index-a/config.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"dl":"https://crates.example.com/api/v1/crates-a"}`))
		case "/index-b/config.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"dl":"https://crates.example.com/api/v1/crates-b"}`))
		case "/api/v1/crates-a/foo/1.0/download", "/api/v1/crates-b/bar/2.0/download":
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("upstream got unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	p, routes := newWithEcosystemRows(t, Route{
		MatchHost: "crates.example.com",
		Upstream:  upstream.URL,

		EnforcedPaths:    []string{"/index-a", "/index-b"},
		EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/index-a"}, {Ecosystem: "cargo", Path: "/index-b"}},
	})
	prefix := routes[0].Prefix

	for _, indexPath := range []string{"/index-a/config.json", "/index-b/config.json"} {
		req := httptest.NewRequest(http.MethodGet, "/"+prefix+indexPath, nil)
		req.Host = "forwarder.example:9999"
		rr := httptest.NewRecorder()
		p.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want %d", indexPath, rr.Code, http.StatusOK)
		}
	}

	for _, downloadPath := range []string{"/api/v1/crates-a/foo/1.0/download", "/api/v1/crates-b/bar/2.0/download"} {
		req := httptest.NewRequest(http.MethodGet, "/"+prefix+downloadPath, nil)
		req.Host = "forwarder.example:9999"
		rr := httptest.NewRecorder()
		p.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want %d", downloadPath, rr.Code, http.StatusOK)
		}
	}

	unrelatedReq := httptest.NewRequest(http.MethodGet, "/"+prefix+"/api/v1/never-declared/baz/1.0/download", nil)
	unrelatedReq.Host = "forwarder.example:9999"
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, unrelatedReq)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("unrelated path: status = %d, want %d", rr.Code, http.StatusForbidden)
	}
}

// Learning the same dl subtree twice for one route must not grow its learned
// set past one entry, or repeat config.json fetches would leak memory over a
// long-lived Forwarder process.
func TestRouteLogHandler_LearnRewriteBaseDedups(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	handler, routes := newWithEcosystemRows(t, Route{
		Upstream: upstream.URL,

		EnforcedPaths:    []string{"/index-a"},
		EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/index-a"}},
	})
	h, ok := handler.(*routeLogHandler)
	if !ok {
		t.Fatalf("New returned %T, want *routeLogHandler", handler)
	}
	prefix := routes[0].Prefix

	h.learnRewriteBase(prefix, "/api/v1/crates")
	h.learnRewriteBase(prefix, "/api/v1/crates")
	if got := len(h.learnedPaths[prefix]); got != 1 {
		t.Errorf("learnedPaths[%q] has %d entries after two identical learns, want 1", prefix, got)
	}
}

// Rows are caller input, so one handing back an empty LearnedPath (an edit
// whose target sits at the upstream's own root) must not be stored verbatim:
// it would then admit through PathSet.Admits' HasPrefix(cleaned, sub+"/")
// branch instead of the "/" whole-host sentinel cargo's own rewriter
// normalizes to.
func TestRouteLogHandler_LearnEmptyPathNormalizesToRoot(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	handler, routes := newWithEcosystemRows(t, Route{
		Upstream: upstream.URL,

		EnforcedPaths:    []string{"/index-a"},
		EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/index-a"}},
	})
	h, ok := handler.(*routeLogHandler)
	if !ok {
		t.Fatalf("New returned %T, want *routeLogHandler", handler)
	}
	prefix := routes[0].Prefix

	h.learnRewriteBase(prefix, "")
	if got := h.learnedPaths[prefix]; len(got) != 1 || got[0] != "/" {
		t.Errorf("learnedPaths[%q] = %q after learning an empty path, want [\"/\"]", prefix, got)
	}
	// Learning the normalized form again must still dedup against the entry
	// the empty path produced, not stack a second one beside it.
	h.learnRewriteBase(prefix, "/")
	if got := len(h.learnedPaths[prefix]); got != 1 {
		t.Errorf("learnedPaths[%q] has %d entries after learning \"\" then \"/\", want 1", prefix, got)
	}
}

// Drives the dl rewrite through a real bindregistry.NewTCPForwarder in front
// of the gated TCP listener, unlike
// TestHostRooted_ConfigJSONRewrittenPerCargoIndexBase above, which sets
// req.Host by hand.
func TestHostRooted_ConfigJSONDLNamesForwarderThroughGatedTCPListener(t *testing.T) {
	const secret = "s3kr1t-e2e-secret"
	const crateBody = "crate-bytes-for-foo"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index-a/config.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"dl":"https://crates.example.com/api/v1/crates-a"}`))
		case "/api/v1/crates-a/foo-1.0.0.crate":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(crateBody))
		default:
			t.Errorf("upstream got unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	handler, routes := newWithEcosystemRows(t, Route{
		MatchHost: "crates.example.com",
		Upstream:  upstream.URL,

		EnforcedPaths:    []string{"/index-a"},
		EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/index-a"}},
	})
	prefix := routes[0].Prefix

	p := &Proxy{Handler: handler}
	if err := p.ListenAndServeTCP("127.0.0.1:0", secret); err != nil {
		t.Fatalf("ListenAndServeTCP: %v", err)
	}
	defer p.Close()

	listenerHost, listenerPortStr, err := net.SplitHostPort(p.Addr().String())
	if err != nil {
		t.Fatalf("net.SplitHostPort(%q): %v", p.Addr().String(), err)
	}
	listenerPort, err := strconv.Atoi(listenerPortStr)
	if err != nil {
		t.Fatalf("strconv.Atoi(%q): %v", listenerPortStr, err)
	}

	fwd, err := bindregistry.NewTCPForwarder(listenerHost, listenerPort, secret)
	if err != nil {
		t.Fatalf("NewTCPForwarder: %v", err)
	}
	forwarder := httptest.NewServer(fwd)
	defer forwarder.Close()
	forwarderHost := forwarder.Listener.Addr().String()

	configResp, err := http.Get(forwarder.URL + "/" + prefix + "/index-a/config.json")
	if err != nil {
		t.Fatalf("http.Get(config.json): %v", err)
	}
	defer configResp.Body.Close()
	if configResp.StatusCode != http.StatusOK {
		t.Fatalf("config.json: status = %d, want %d", configResp.StatusCode, http.StatusOK)
	}
	configBody, err := io.ReadAll(configResp.Body)
	if err != nil {
		t.Fatalf("ReadAll(config.json): %v", err)
	}

	wantDL := "http://" + forwarderHost + "/" + prefix + "/api/v1/crates-a"
	if got, want := string(configBody), `{"dl":"`+wantDL+`"}`; got != want {
		t.Fatalf("config.json: body = %s, want %s", got, want)
	}

	downloadResp, err := http.Get(wantDL + "/foo-1.0.0.crate")
	if err != nil {
		t.Fatalf("http.Get(download): %v", err)
	}
	defer downloadResp.Body.Close()
	if downloadResp.StatusCode != http.StatusOK {
		t.Fatalf("download: status = %d, want %d", downloadResp.StatusCode, http.StatusOK)
	}
	body, err := io.ReadAll(downloadResp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(body) != crateBody {
		t.Errorf("download body = %q, want %q", string(body), crateBody)
	}
}

// A request naming exactly the route prefix and nothing else ("/r0", no
// trailing slash) has no remainder to join, and the origin contributes no path
// of its own, so the upstream sees the root path with the route's credential
// attached.
func TestHostRooted_BarePrefixForwardsRootToOrigin(t *testing.T) {
	var gotPath, gotRequestURI, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotRequestURI = r.RequestURI
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := New(AssignPrefixes([]Route{{
		Upstream:      upstream.URL,
		Credential:    "s3kr1t",
		EnforcedPaths: []string{"/"},
	}}), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	srv := httptest.NewServer(p)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/r0")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if gotPath != "/" {
		t.Errorf("upstream path = %q, want %q", gotPath, "/")
	}
	if gotRequestURI != "/" {
		t.Errorf("upstream request-URI = %q, want %q", gotRequestURI, "/")
	}
	if gotAuth != "Bearer s3kr1t" {
		t.Errorf("upstream Authorization = %q, want %q", gotAuth, "Bearer s3kr1t")
	}
}
