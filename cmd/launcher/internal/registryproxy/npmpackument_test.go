package registryproxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/registryvocab"
)

// These tests drive the real npm ecosystem row (newWithEcosystemRows) through
// the same Forwarder round-trip harness registryproxy_test.go's cargo tests
// use, so npm's dist.tarball rewrite (issue #3401) is pinned against the rows
// a production run wires up, not a stand-in.

// npm install fetches a tarball straight off the packument's dist.tarball, not
// off any registry setting, so this row is the only thing that keeps that
// download on the credentialed path instead of leaving the proxy. The test
// pins the whole round trip: the rewrite, then admission of the tarball path
// the route's static EnforcedPaths refused before the packument was fetched.
func TestModifyResponse_NpmPackument_RewritesTarballAndLearnsCredentialedPath(t *testing.T) {
	const credential = "sekret-npm-token"
	const packument = `{"versions":{"1.0.0":{"dist":{"tarball":"https://registry.example.com/downloads/pkg-1.0.0.tgz"}}}}`
	const tarballBytes = "totally a tgz"

	var gotAuthorization string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/registry/pkg":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(packument))
		case "/downloads/pkg-1.0.0.tgz":
			gotAuthorization = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(tarballBytes))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	// EnforcedPaths covers only "/registry" (the packument's own subtree),
	// not "/downloads". Otherwise the tarball path would already be admitted
	// and a later 200 there would prove nothing was learned.
	routes := AssignPrefixes([]Route{{
		MatchHost:        "registry.example.com",
		EnforcedPaths:    []string{"/registry"},
		EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "npm", Path: "/registry"}},
		Upstream:         upstream.URL,
		Credential:       credential,
	}})
	p := newWithEcosystemRows(t, routes)
	prefix := routes[0].Prefix

	preRR := httptest.NewRecorder()
	preReq := httptest.NewRequest(http.MethodGet, "/"+prefix+"/downloads/pkg-1.0.0.tgz", nil)
	p.ServeHTTP(preRR, preReq)
	if preRR.Code != http.StatusForbidden {
		t.Fatalf("before learning: status = %d, want %d (refused)", preRR.Code, http.StatusForbidden)
	}

	packumentRR := httptest.NewRecorder()
	packumentReq := httptest.NewRequest(http.MethodGet, "/"+prefix+"/registry/pkg", nil)
	packumentReq.Host = "forwarder.example:9999"
	p.ServeHTTP(packumentRR, packumentReq)
	if packumentRR.Code != http.StatusOK {
		t.Fatalf("packument fetch: status = %d, want %d", packumentRR.Code, http.StatusOK)
	}
	wantBody := `{"versions":{"1.0.0":{"dist":{"tarball":"http://forwarder.example:9999/` + prefix + `/downloads/pkg-1.0.0.tgz"}}}}`
	if got := packumentRR.Body.String(); got != wantBody {
		t.Errorf("packument body = %s, want %s", got, wantBody)
	}
	if got := packumentRR.Header().Get("Content-Length"); got != strconv.Itoa(len(wantBody)) {
		t.Errorf("Content-Length = %q, want %q", got, strconv.Itoa(len(wantBody)))
	}

	postRR := httptest.NewRecorder()
	postReq := httptest.NewRequest(http.MethodGet, "/"+prefix+"/downloads/pkg-1.0.0.tgz", nil)
	p.ServeHTTP(postRR, postReq)
	if postRR.Code != http.StatusOK {
		t.Fatalf("after learning: status = %d, want %d (admitted)", postRR.Code, http.StatusOK)
	}
	if got := postRR.Body.String(); got != tarballBytes {
		t.Errorf("after learning: body = %q, want %q", got, tarballBytes)
	}
	if gotAuthorization != "Bearer "+credential {
		t.Errorf("upstream saw Authorization %q, want %q (route's credential on the learned tarball download)", gotAuthorization, "Bearer "+credential)
	}
}

// selectRoute hands the row the decoded remainder, not the escaped path it
// split routing on, so a scoped package name reaches the row's Matches func
// as "/@scope/name", not the wire's "%40scope%2Fname".
func TestModifyResponse_NpmPackument_ScopedNameMatches(t *testing.T) {
	const packument = `{"versions":{"1.0.0":{"dist":{"tarball":"https://registry.example.com/downloads/pkg-1.0.0.tgz"}}}}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/@scope/name" {
			t.Errorf("upstream got path %q, want /@scope/name", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(packument))
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{MatchHost: "registry.example.com", EnforcedPaths: []string{"/"}, EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "npm", Path: "/"}}, Upstream: upstream.URL}})
	p := newWithEcosystemRows(t, routes)
	prefix := routes[0].Prefix

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/%40scope%2Fname", nil)
	req.Host = "forwarder.example:9999"
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	wantBody := `{"versions":{"1.0.0":{"dist":{"tarball":"http://forwarder.example:9999/` + prefix + `/downloads/pkg-1.0.0.tgz"}}}}`
	if got := rr.Body.String(); got != wantBody {
		t.Errorf("body = %s, want %s (scoped packument fetch must still be rewritten)", got, wantBody)
	}
}

// A two-segment path whose first segment does not start with "@" is not a
// scoped name, so npmPackumentMatches leaves it unmatched. This case exercises
// the same case-2 arm a real scoped name uses, just failing its "@" guard, so
// the deeper default case does not cover it.
func TestModifyResponse_NpmPackument_TarballShapedTwoSegmentPathDoesNotMatch(t *testing.T) {
	const body = `{"tarball":"https://registry.example.com/downloads/pkg-1.0.0.tgz"}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{MatchHost: "registry.example.com", EnforcedPaths: []string{"/"}, EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "npm", Path: "/"}}, Upstream: upstream.URL}})
	p := newWithEcosystemRows(t, routes)
	prefix := routes[0].Prefix

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/pkg/download", nil)
	req.Host = "forwarder.example:9999"
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Body.String(); got != body {
		t.Errorf("body = %s, want byte-identical to upstream's %s (row must not match a two-segment non-scoped path)", got, body)
	}
}

// A foreign-host edit's empty To must never reach learnRewriteBase, or its
// unset LearnedPath would normalize to "/" and learn the whole host open
// (issue #3401). The packument here mixes a same-host tarball with a CDN one:
// only the same-host one is rewritten, and the CDN's own path must still be
// refused on a follow-up request.
func TestModifyResponse_NpmPackument_MixedHostsOnlyRewritesSameHostAndLearnsNothingForeign(t *testing.T) {
	const credential = "s3kr1t-npm-token"
	const cdnTarball = "https://cdn.example.com/assets/pkg-2.0.0.tgz"
	packument := fmt.Sprintf(`{"versions":{"1.0.0":{"dist":{"tarball":"https://registry.example.com/downloads/pkg-1.0.0.tgz"}},"2.0.0":{"dist":{"tarball":%q}}}}`, cdnTarball)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(packument))
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{
		MatchHost:        "registry.example.com",
		EnforcedPaths:    []string{"/registry"},
		EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "npm", Path: "/registry"}},
		Upstream:         upstream.URL,
		Credential:       credential,
	}})
	p := newWithEcosystemRows(t, routes)
	prefix := routes[0].Prefix

	logBuf := captureLog(t)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/registry/pkg", nil)
	req.Host = "forwarder.example:9999"
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	got := rr.Body.String()
	wantRewritten := `"tarball":"http://forwarder.example:9999/` + prefix + `/downloads/pkg-1.0.0.tgz"`
	if !strings.Contains(got, wantRewritten) {
		t.Errorf("body = %s, want it to contain the rewritten same-host tarball %s", got, wantRewritten)
	}
	if !strings.Contains(got, cdnTarball) {
		t.Errorf("body = %s, want the CDN tarball %s left byte-identical", got, cdnTarball)
	}

	logged := logBuf.String()
	if !strings.Contains(logged, cdnTarball) {
		t.Errorf("log output did not name the skipped CDN tarball: %q", logged)
	}
	if strings.Contains(logged, credential) {
		t.Errorf("log output contained the credential: %q", logged)
	}

	followRR := httptest.NewRecorder()
	followReq := httptest.NewRequest(http.MethodGet, "/"+prefix+"/assets/pkg-2.0.0.tgz", nil)
	p.ServeHTTP(followRR, followReq)
	if followRR.Code != http.StatusForbidden {
		t.Errorf("follow-up to the CDN's own path: status = %d, want %d (nothing must have been learned from a declined edit)", followRR.Code, http.StatusForbidden)
	}
}

// No npm row names HEAD, so a HEAD request for the packument path takes the
// same untouched relay path any other unrewritable method takes. The row must
// never run at all, so it must log no rewrite line.
func TestModifyResponse_NpmPackument_HeadNeverMatches(t *testing.T) {
	const packument = `{"versions":{"1.0.0":{"dist":{"tarball":"https://registry.example.com/downloads/pkg-1.0.0.tgz"}}}}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(packument))
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{MatchHost: "registry.example.com", EnforcedPaths: []string{"/"}, EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "npm", Path: "/"}}, Upstream: upstream.URL}})
	p := newWithEcosystemRows(t, routes)
	prefix := routes[0].Prefix

	logBuf := captureLog(t)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "/"+prefix+"/pkg", nil)
	req.Host = "forwarder.example:9999"
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Body.String(); got != "" {
		t.Errorf("HEAD body = %q, want empty (net/http strips a HEAD response body regardless of what the row would have done)", got)
	}
	if logged := logBuf.String(); strings.Contains(logged, "npm packument") {
		t.Errorf("log output mentioned the npm packument row for a HEAD request: %q", logged)
	}
}

// A 404 or 500 body is an error page, not real packument content, so the
// non-200 guard must relay it byte-identical even when it carries a
// packument-shaped dist.tarball naming the route's own match host.
func TestModifyResponse_NpmPackument_NonOKStatusNeverMatches(t *testing.T) {
	const body = `{"versions":{"1.0.0":{"dist":{"tarball":"https://registry.example.com/downloads/pkg-1.0.0.tgz"}}}}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(body))
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{MatchHost: "registry.example.com", EnforcedPaths: []string{"/"}, EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "npm", Path: "/"}}, Upstream: upstream.URL}})
	p := newWithEcosystemRows(t, routes)
	prefix := routes[0].Prefix

	logBuf := captureLog(t)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/pkg", nil)
	req.Host = "forwarder.example:9999"
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
	if got := rr.Body.String(); got != body {
		t.Errorf("body = %s, want byte-identical to upstream's %s", got, body)
	}
	if logged := logBuf.String(); strings.Contains(logged, "npm packument") {
		t.Errorf("log output mentioned the npm packument row for a non-200 response: %q", logged)
	}
}
