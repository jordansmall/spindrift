package registryproxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/registryvocab"
)

// This file drives findResponseRewriteRow and the learning path
// (learnRewriteBase/learnedAdmits) with a registryvocab.RewriteRow for an
// invented ecosystem tag ("widget") that names no real ecosystem.Table row
// -- independent of cargo, so these pin the matching/learning mechanism
// itself rather than anything cargo-specific (issue #3400).

// fakeAssetBody is the tiny JSON shape the fake rows below rewrite: one or
// two absolute-URL fields naming a downloadable asset, the same "one field
// names a URL to rewrite" shape as cargo's dl, but under names no real row
// uses.
type fakeAssetBody struct {
	Asset     string `json:"asset,omitempty"`
	Primary   string `json:"primary,omitempty"`
	Secondary string `json:"secondary,omitempty"`
}

// rewriteFakeAssetField classifies one absolute-URL field and, when it can
// be repointed at the Forwarder, returns the edit carrying the rewritten
// value in its To -- shared by both rewriters below so each states only its
// own outcome contract instead of re-deriving the parse/host-check cascade.
// The returned outcome classifies that one field -- RewriteNone for a
// missing or unparseable value, RewriteSkippedForeignHost for one naming a
// host other than the route's own match-host -- and each caller decides
// what that means for the body as a whole.
func rewriteFakeAssetField(raw string, rc registryvocab.RewriteContext) (registryvocab.RewriteEdit, registryvocab.RewriteOutcome) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return registryvocab.RewriteEdit{}, registryvocab.RewriteNone
	}
	if u.Host != rc.MatchHost {
		return registryvocab.RewriteEdit{From: raw}, registryvocab.RewriteSkippedForeignHost
	}
	newURL := &url.URL{Scheme: rc.Forwarder.Scheme, Host: rc.Forwarder.Host, Path: "/" + rc.Prefix + u.Path}
	return registryvocab.RewriteEdit{From: u.String(), To: newURL.String(), LearnedPath: u.Path}, registryvocab.RewriteApplied
}

// rewriteFakeSingleAsset rewrites doc.Asset the same way cargo's
// rewriteCargoDL rewrites dl: RewriteNone when there's no asset field to
// find, RewriteSkippedForeignHost when it names a host other than the
// route's own match-host, otherwise RewriteApplied with the asset
// repointed at the Forwarder and LearnedPath set to the asset's own
// route-relative path.
func rewriteFakeSingleAsset(body []byte, rc registryvocab.RewriteContext) registryvocab.RewriteResult {
	var doc fakeAssetBody
	if err := json.Unmarshal(body, &doc); err != nil {
		return registryvocab.RewriteResult{Body: body, Outcome: registryvocab.RewriteNone}
	}
	edit, outcome := rewriteFakeAssetField(doc.Asset, rc)
	switch outcome {
	case registryvocab.RewriteSkippedForeignHost:
		return registryvocab.RewriteResult{Body: body, Edits: []registryvocab.RewriteEdit{edit}, Outcome: outcome}
	case registryvocab.RewriteNone:
		return registryvocab.RewriteResult{Body: body, Outcome: outcome}
	}
	doc.Asset = edit.To
	newBody, err := json.Marshal(doc)
	if err != nil {
		return registryvocab.RewriteResult{Body: body, Outcome: registryvocab.RewriteNone}
	}
	return registryvocab.RewriteResult{
		Body:    newBody,
		Edits:   []registryvocab.RewriteEdit{edit},
		Outcome: registryvocab.RewriteApplied,
	}
}

// rewriteFakeDualAssets rewrites both Primary and Secondary, each against
// the route's own match-host, and reports one edit per field -- pinning
// that a RewriteResult with more than one edit gets every edit logged and
// every LearnedPath learned, not just the first.
func rewriteFakeDualAssets(body []byte, rc registryvocab.RewriteContext) registryvocab.RewriteResult {
	var doc fakeAssetBody
	if err := json.Unmarshal(body, &doc); err != nil {
		return registryvocab.RewriteResult{Body: body, Outcome: registryvocab.RewriteNone}
	}
	var edits []registryvocab.RewriteEdit
	if edit, outcome := rewriteFakeAssetField(doc.Primary, rc); outcome == registryvocab.RewriteApplied {
		doc.Primary = edit.To
		edits = append(edits, edit)
	}
	if edit, outcome := rewriteFakeAssetField(doc.Secondary, rc); outcome == registryvocab.RewriteApplied {
		doc.Secondary = edit.To
		edits = append(edits, edit)
	}
	if len(edits) == 0 {
		return registryvocab.RewriteResult{Body: body, Outcome: registryvocab.RewriteNone}
	}
	newBody, err := json.Marshal(doc)
	if err != nil {
		return registryvocab.RewriteResult{Body: body, Outcome: registryvocab.RewriteNone}
	}
	return registryvocab.RewriteResult{Body: newBody, Edits: edits, Outcome: registryvocab.RewriteApplied}
}

// rewriteFakeSkipNoEdits reports a deliberate skip carrying no edits at
// all -- a shape a caller-supplied row is free to produce (it decided not
// to touch the body without singling out any one value), which must not be
// reported as if nothing rewritable had been found.
func rewriteFakeSkipNoEdits(body []byte, rc registryvocab.RewriteContext) registryvocab.RewriteResult {
	return registryvocab.RewriteResult{Body: body, Outcome: registryvocab.RewriteSkippedForeignHost}
}

// fakeManifestRow matches "GET <base>/manifest.json" under the "widget"
// ecosystem tag, the same request shape cargo's config.json row matches
// under "cargo" -- but tagged for an ecosystem no ecosystem.Table row
// declares.
func fakeManifestRow(rewrite func([]byte, registryvocab.RewriteContext) registryvocab.RewriteResult) registryvocab.RewriteRow {
	return registryvocab.RewriteRow{
		Name:      "widget manifest.json",
		Ecosystem: "widget",
		Method:    http.MethodGet,
		Matches: func(routeRelativePath, base string) bool {
			return routeRelativePath == registryvocab.JoinBase(base, "/manifest.json")
		},
		Rewrite: rewrite,
	}
}

// TestFakeRewriteRow_MatchesAndLearns verifies the full round trip for a
// made-up ecosystem: a manifest.json fetch under a "widget"-tagged base gets
// its asset field rewritten to the Forwarder, and the edit's LearnedPath is
// then admitted on a follow-up request that the route's static EnforcedPaths
// alone would refuse -- the "learning" half of ADR 0047, pinned here
// independent of cargo.
func TestFakeRewriteRow_MatchesAndLearns(t *testing.T) {
	const assetBody = `{"asset":"https://widget.example.com/downloads/pkg-1.0.tar.gz"}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index/manifest.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(assetBody))
		case "/downloads/pkg-1.0.tar.gz":
			_, _ = w.Write([]byte("package bytes"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{
		MatchHost:        "widget.example.com",
		EnforcedPaths:    []string{"/index"},
		EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "widget", Path: "/index"}},
		Upstream:         upstream.URL,
	}})
	p, err := New(routes, []registryvocab.RewriteRow{fakeManifestRow(rewriteFakeSingleAsset)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	prefix := routes[0].Prefix

	// The static enforced set is "/index" only, so "/downloads/..." must
	// start out refused -- otherwise a later 200 there wouldn't prove
	// anything was learned.
	preRR := httptest.NewRecorder()
	preReq := httptest.NewRequest(http.MethodGet, "/"+prefix+"/downloads/pkg-1.0.tar.gz", nil)
	p.ServeHTTP(preRR, preReq)
	if preRR.Code != http.StatusForbidden {
		t.Fatalf("before learning: status = %d, want %d (refused)", preRR.Code, http.StatusForbidden)
	}

	manifestRR := httptest.NewRecorder()
	manifestReq := httptest.NewRequest(http.MethodGet, "/"+prefix+"/index/manifest.json", nil)
	manifestReq.Host = "forwarder.example:9999"
	p.ServeHTTP(manifestRR, manifestReq)
	if manifestRR.Code != http.StatusOK {
		t.Fatalf("manifest fetch: status = %d, want %d", manifestRR.Code, http.StatusOK)
	}
	wantBody := `{"asset":"http://forwarder.example:9999/` + prefix + `/downloads/pkg-1.0.tar.gz"}`
	if got := manifestRR.Body.String(); got != wantBody {
		t.Errorf("manifest body = %s, want %s", got, wantBody)
	}

	postRR := httptest.NewRecorder()
	postReq := httptest.NewRequest(http.MethodGet, "/"+prefix+"/downloads/pkg-1.0.tar.gz", nil)
	p.ServeHTTP(postRR, postReq)
	if postRR.Code != http.StatusOK {
		t.Fatalf("after learning: status = %d, want %d (admitted)", postRR.Code, http.StatusOK)
	}
	if got := postRR.Body.String(); got != "package bytes" {
		t.Errorf("after learning: body = %q, want %q", got, "package bytes")
	}
}

// TestFakeRewriteRow_EcosystemTagMismatchNeverMatches verifies the
// ecosystem-tag keying itself: a row declared for one ecosystem tag never
// matches against a route's base tagged for a different ecosystem, even
// when the row's method and path shape would otherwise fit exactly. This is
// the case most worth pinning, since a bug here would let one ecosystem's
// row rewrite another's response.
func TestFakeRewriteRow_EcosystemTagMismatchNeverMatches(t *testing.T) {
	const assetBody = `{"asset":"https://widget.example.com/downloads/pkg-1.0.tar.gz"}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(assetBody))
	}))
	defer upstream.Close()

	// The route's own subtree is tagged "widget", but the row below is
	// tagged "gadget" -- same method, same path shape, different ecosystem.
	routes := AssignPrefixes([]Route{{
		MatchHost:        "widget.example.com",
		EnforcedPaths:    []string{"/index"},
		EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "widget", Path: "/index"}},
		Upstream:         upstream.URL,
	}})
	row := fakeManifestRow(rewriteFakeSingleAsset)
	row.Ecosystem = "gadget"
	p, err := New(routes, []registryvocab.RewriteRow{row})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	prefix := routes[0].Prefix

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/index/manifest.json", nil)
	req.Host = "forwarder.example:9999"
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Body.String(); got != assetBody {
		t.Errorf("body = %s, want byte-identical to upstream's %s (row must not match a foreign ecosystem tag's base)", got, assetBody)
	}
}

// TestFakeRewriteRow_MultipleEditsAllLoggedAndAllLearned verifies a
// RewriteResult carrying more than one edit gets every edit logged and
// every LearnedPath learned -- the result is a list now, so a caller that
// only looked at the first edit would silently regress once a row ever
// needs to rewrite more than one field.
func TestFakeRewriteRow_MultipleEditsAllLoggedAndAllLearned(t *testing.T) {
	const manifestBody = `{"primary":"https://widget.example.com/primary/pkg.tar.gz","secondary":"https://widget.example.com/mirror/pkg.tar.gz"}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index/manifest.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(manifestBody))
		default:
			_, _ = w.Write([]byte("asset bytes for " + r.URL.Path))
		}
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{
		MatchHost:        "widget.example.com",
		EnforcedPaths:    []string{"/index"},
		EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "widget", Path: "/index"}},
		Upstream:         upstream.URL,
	}})
	p, err := New(routes, []registryvocab.RewriteRow{fakeManifestRow(rewriteFakeDualAssets)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	prefix := routes[0].Prefix

	logBuf := captureLog(t)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/index/manifest.json", nil)
	req.Host = "forwarder.example:9999"
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	logged := logBuf.String()
	if got := strings.Count(logged, "widget manifest.json: rewrote"); got != 2 {
		t.Errorf("rewrote log line occurred %d times, want exactly 2 (one per edit): %q", got, logged)
	}

	for _, relPath := range []string{"/primary/pkg.tar.gz", "/mirror/pkg.tar.gz"} {
		followRR := httptest.NewRecorder()
		followReq := httptest.NewRequest(http.MethodGet, "/"+prefix+relPath, nil)
		p.ServeHTTP(followRR, followReq)
		if followRR.Code != http.StatusOK {
			t.Errorf("follow-up %s: status = %d, want %d (learned path must be admitted)", relPath, followRR.Code, http.StatusOK)
		}
	}
}

// TestFakeRewriteRow_SkipWithNoEditsLogsAsASkip verifies a deliberate skip
// that names no edited value still reports itself as a skip, rather than
// falling through to the RewriteNone line and misreporting the row as
// having found nothing rewritable.
func TestFakeRewriteRow_SkipWithNoEditsLogsAsASkip(t *testing.T) {
	const manifestBody = `{"asset":"https://widget.example.com/downloads/pkg-1.0.tar.gz"}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(manifestBody))
	}))
	defer upstream.Close()

	routes := AssignPrefixes([]Route{{
		MatchHost:        "widget.example.com",
		EnforcedPaths:    []string{"/index"},
		EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "widget", Path: "/index"}},
		Upstream:         upstream.URL,
	}})
	p, err := New(routes, []registryvocab.RewriteRow{fakeManifestRow(rewriteFakeSkipNoEdits)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	prefix := routes[0].Prefix

	logBuf := captureLog(t)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/index/manifest.json", nil)
	req.Host = "forwarder.example:9999"
	p.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Body.String(); got != manifestBody {
		t.Errorf("body = %s, want byte-identical to upstream's %s", got, manifestBody)
	}
	logged := logBuf.String()
	if strings.Contains(logged, "nothing rewritable") {
		t.Errorf("skip with no edits logged as a no-match: %q", logged)
	}
	if !strings.Contains(logged, "widget manifest.json: skipped") {
		t.Errorf("skip with no edits did not log a skip line naming the row: %q", logged)
	}
}

// TestFakeRewriteRow_ForeignHostAndNoneBothRelayUntouched verifies both
// non-applied outcomes relay the response body untouched -- a
// RewriteSkippedForeignHost asset (naming a host other than the route's own
// match-host) and a RewriteNone body (no recognizable asset field at all).
func TestFakeRewriteRow_ForeignHostAndNoneBothRelayUntouched(t *testing.T) {
	testCases := []struct {
		name string
		body string
	}{
		{name: "foreign host", body: `{"asset":"https://cdn.example.com/downloads/pkg-1.0.tar.gz"}`},
		{name: "no recognizable field", body: `{"not-an-asset":"nothing to rewrite here"}`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer upstream.Close()

			routes := AssignPrefixes([]Route{{
				MatchHost:        "widget.example.com",
				EnforcedPaths:    []string{"/index"},
				EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "widget", Path: "/index"}},
				Upstream:         upstream.URL,
			}})
			p, err := New(routes, []registryvocab.RewriteRow{fakeManifestRow(rewriteFakeSingleAsset)})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			prefix := routes[0].Prefix

			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/index/manifest.json", nil)
			req.Host = "forwarder.example:9999"
			p.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
			}
			if got := rr.Body.String(); got != tc.body {
				t.Errorf("body = %s, want byte-identical to upstream's %s", got, tc.body)
			}
		})
	}
}
