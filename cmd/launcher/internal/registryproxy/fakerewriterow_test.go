package registryproxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/registryvocab"
)

// This file drives findResponseRewriteRow and the learning path
// (learnRewriteBase/learnedAdmits) with a registryvocab.RewriteRow for an
// invented ecosystem tag ("widget") that names no real registered Strategy,
// so these tests pin the matching and learning mechanism itself rather than
// anything specific to one real ecosystem (issue #3400).

// fakeAssetBody has the same "one field names a URL to rewrite" shape as
// the synthetic row's download, but under field names no real row uses.
type fakeAssetBody struct {
	Asset     string `json:"asset,omitempty"`
	Primary   string `json:"primary,omitempty"`
	Secondary string `json:"secondary,omitempty"`
}

// rewriteFakeAssetField classifies one absolute-URL field and, when it can be
// repointed at the Forwarder, returns the rewritten value in the edit's To.
// The outcome describes that one field, so each caller decides what it means
// for the body as a whole.
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

// rewriteFakeSingleAsset rewrites doc.Asset the same way the synthetic row's
// rewriteSyntheticDownload rewrites download.
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

// rewriteFakeDualAssets reports one edit per field, pinning that a
// RewriteResult with more than one edit gets every edit logged and every
// LearnedPath learned, not just the first.
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

// rewriteFakeMixedAssets pins that one metadata-shaped JSON body can carry both
// an applied edit and a declined one side by side (issue #3401).
// rewriteFakeAssetField already returns a From-only edit for
// RewriteSkippedForeignHost, which is the empty-To shape a declined edit
// needs.
func rewriteFakeMixedAssets(body []byte, rc registryvocab.RewriteContext) registryvocab.RewriteResult {
	var doc fakeAssetBody
	if err := json.Unmarshal(body, &doc); err != nil {
		return registryvocab.RewriteResult{Body: body, Outcome: registryvocab.RewriteNone}
	}
	var edits []registryvocab.RewriteEdit
	if edit, outcome := rewriteFakeAssetField(doc.Primary, rc); outcome == registryvocab.RewriteApplied {
		doc.Primary = edit.To
		edits = append(edits, edit)
	}
	if edit, outcome := rewriteFakeAssetField(doc.Secondary, rc); outcome == registryvocab.RewriteSkippedForeignHost {
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

// rewriteFakeSkipNoEdits reports a deliberate skip carrying no edits at all.
// A caller-supplied row may decline to touch the body without singling out any
// one value, and that must not be reported as if nothing rewritable had been
// found.
func rewriteFakeSkipNoEdits(body []byte, rc registryvocab.RewriteContext) registryvocab.RewriteResult {
	return registryvocab.RewriteResult{Body: body, Outcome: registryvocab.RewriteSkippedForeignHost}
}

// fakeManifestRow matches the same request shape as an index.json row
// matches, but tagged for an ecosystem no registered Strategy declares.
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

// The edit's LearnedPath must be admitted on a follow-up request that the
// route's static EnforcedPaths alone would refuse: the "learning" half of
// ADR 0047, pinned here independent of any real ecosystem.
func TestFakeRewriteRow_MatchesAndLearns(t *testing.T) {
	const assetBody = `{"asset":"https://widget.example.com/downloads/pkg-1.0.tar.gz"}`

	upstream := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index/manifest.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(assetBody))
		case "/downloads/pkg-1.0.tar.gz":
			_, _ = w.Write([]byte("package bytes"))
		default:
			http.NotFound(w, r)
		}
	})

	p, routes := newProxy(t, []registryvocab.RewriteRow{fakeManifestRow(rewriteFakeSingleAsset)}, []Route{{
		MatchHost:        "widget.example.com",
		EnforcedPaths:    []string{"/index"},
		EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "widget", Path: "/index"}},
		Upstream:         upstream.URL,
	}})
	prefix := routes[0].Prefix

	// The static enforced set is "/index" only, so "/downloads/..." must start
	// out refused, or a later 200 there would prove nothing.
	preReq := httptest.NewRequest(http.MethodGet, "/"+prefix+"/downloads/pkg-1.0.tar.gz", nil)
	preRR := serve(p, preReq)
	if preRR.Code != http.StatusForbidden {
		t.Fatalf("before learning: status = %d, want %d (refused)", preRR.Code, http.StatusForbidden)
	}

	manifestReq := httptest.NewRequest(http.MethodGet, "/"+prefix+"/index/manifest.json", nil)
	manifestReq.Host = "forwarder.example:9999"
	manifestRR := serve(p, manifestReq)
	if manifestRR.Code != http.StatusOK {
		t.Fatalf("manifest fetch: status = %d, want %d", manifestRR.Code, http.StatusOK)
	}
	wantBody := `{"asset":"http://forwarder.example:9999/` + prefix + `/downloads/pkg-1.0.tar.gz"}`
	if got := manifestRR.Body.String(); got != wantBody {
		t.Errorf("manifest body = %s, want %s", got, wantBody)
	}

	postReq := httptest.NewRequest(http.MethodGet, "/"+prefix+"/downloads/pkg-1.0.tar.gz", nil)
	postRR := serve(p, postReq)
	if postRR.Code != http.StatusOK {
		t.Fatalf("after learning: status = %d, want %d (admitted)", postRR.Code, http.StatusOK)
	}
	if got := postRR.Body.String(); got != "package bytes" {
		t.Errorf("after learning: body = %q, want %q", got, "package bytes")
	}
}

// A row declared for one ecosystem tag must never match a route base tagged
// for a different ecosystem, even when the row's method and path shape fit
// exactly. A bug here would let a rewrite row declared for one ecosystem
// rewrite another's response.
func TestFakeRewriteRow_EcosystemTagMismatchNeverMatches(t *testing.T) {
	const assetBody = `{"asset":"https://widget.example.com/downloads/pkg-1.0.tar.gz"}`

	upstream := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(assetBody))
	})

	// The route's subtree is tagged "widget" and the row below "gadget": same
	// method, same path shape, different ecosystem.
	row := fakeManifestRow(rewriteFakeSingleAsset)
	row.Ecosystem = "gadget"
	p, routes := newProxy(t, []registryvocab.RewriteRow{row}, []Route{{
		MatchHost:        "widget.example.com",
		EnforcedPaths:    []string{"/index"},
		EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "widget", Path: "/index"}},
		Upstream:         upstream.URL,
	}})
	prefix := routes[0].Prefix

	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/index/manifest.json", nil)
	req.Host = "forwarder.example:9999"
	rr := serve(p, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Body.String(); got != assetBody {
		t.Errorf("body = %s, want byte-identical to upstream's %s (row must not match a foreign ecosystem tag's base)", got, assetBody)
	}
}

// The result carries a list of edits, so a caller that only looked at the
// first one would silently regress once a row needs to rewrite more than one
// field.
func TestFakeRewriteRow_MultipleEditsAllLoggedAndAllLearned(t *testing.T) {
	const manifestBody = `{"primary":"https://widget.example.com/primary/pkg.tar.gz","secondary":"https://widget.example.com/mirror/pkg.tar.gz"}`

	upstream := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index/manifest.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(manifestBody))
		default:
			_, _ = w.Write([]byte("asset bytes for " + r.URL.Path))
		}
	})

	p, routes := newProxy(t, []registryvocab.RewriteRow{fakeManifestRow(rewriteFakeDualAssets)}, []Route{{
		MatchHost:        "widget.example.com",
		EnforcedPaths:    []string{"/index"},
		EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "widget", Path: "/index"}},
		Upstream:         upstream.URL,
	}})
	prefix := routes[0].Prefix

	logBuf := captureLog(t)

	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/index/manifest.json", nil)
	req.Host = "forwarder.example:9999"
	rr := serve(p, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	logged := logBuf.String()
	if got := strings.Count(logged, "widget manifest.json: rewrote"); got != 2 {
		t.Errorf("rewrote log line occurred %d times, want exactly 2 (one per edit): %q", got, logged)
	}

	for _, relPath := range []string{"/primary/pkg.tar.gz", "/mirror/pkg.tar.gz"} {
		followReq := httptest.NewRequest(http.MethodGet, "/"+prefix+relPath, nil)
		followRR := serve(p, followReq)
		if followRR.Code != http.StatusOK {
			t.Errorf("follow-up %s: status = %d, want %d (learned path must be admitted)", relPath, followRR.Code, http.StatusOK)
		}
	}
}

// A deliberate skip that names no edited value must still report itself as a
// skip, rather than falling through to the RewriteNone line and misreporting
// the row as having found nothing rewritable.
func TestFakeRewriteRow_SkipWithNoEditsLogsAsASkip(t *testing.T) {
	const manifestBody = `{"asset":"https://widget.example.com/downloads/pkg-1.0.tar.gz"}`

	upstream := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(manifestBody))
	})

	p, routes := newProxy(t, []registryvocab.RewriteRow{fakeManifestRow(rewriteFakeSkipNoEdits)}, []Route{{
		MatchHost:        "widget.example.com",
		EnforcedPaths:    []string{"/index"},
		EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "widget", Path: "/index"}},
		Upstream:         upstream.URL,
	}})
	prefix := routes[0].Prefix

	logBuf := captureLog(t)

	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/index/manifest.json", nil)
	req.Host = "forwarder.example:9999"
	rr := serve(p, req)

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

// Issue #3401's mixed-host metadata body gap: when one result carries both an applied
// edit and a declined (foreign-host) edit, nothing may be learned from the
// declined one. An empty-To edit's LearnedPath must never be normalized to
// "/" and admit the whole host.
func TestFakeRewriteRow_MixedAppliedAndDeclinedEdits(t *testing.T) {
	const manifestBody = `{"primary":"https://widget.example.com/primary/pkg.tar.gz","secondary":"https://cdn.example.com/mirror/pkg.tar.gz"}`

	upstream := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index/manifest.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(manifestBody))
		default:
			_, _ = w.Write([]byte("asset bytes for " + r.URL.Path))
		}
	})

	p, routes := newProxy(t, []registryvocab.RewriteRow{fakeManifestRow(rewriteFakeMixedAssets)}, []Route{{
		MatchHost:        "widget.example.com",
		EnforcedPaths:    []string{"/index"},
		EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "widget", Path: "/index"}},
		Upstream:         upstream.URL,
	}})
	prefix := routes[0].Prefix

	logBuf := captureLog(t)

	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/index/manifest.json", nil)
	req.Host = "forwarder.example:9999"
	rr := serve(p, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	wantBody := `{"primary":"http://forwarder.example:9999/` + prefix + `/primary/pkg.tar.gz","secondary":"https://cdn.example.com/mirror/pkg.tar.gz"}`
	if got := rr.Body.String(); got != wantBody {
		t.Errorf("body = %s, want %s (foreign-host field must stay byte-identical)", got, wantBody)
	}

	logged := logBuf.String()
	if !strings.Contains(logged, `"https://cdn.example.com/mirror/pkg.tar.gz" names a host other than the route's match-host, left unchanged`) {
		t.Errorf("declined edit not logged as a skip: %q", logged)
	}
	if strings.Contains(logged, "rewrote https://cdn.example.com") {
		t.Errorf("declined edit logged as if it had been rewritten: %q", logged)
	}

	primaryReq := httptest.NewRequest(http.MethodGet, "/"+prefix+"/primary/pkg.tar.gz", nil)
	primaryRR := serve(p, primaryReq)
	if primaryRR.Code != http.StatusOK {
		t.Errorf("applied edit's path: status = %d, want %d (learned path must be admitted)", primaryRR.Code, http.StatusOK)
	}

	// Nothing was learned from the declined edit, so its path must still be
	// refused rather than falling back to admitting the root subtree.
	mirrorReq := httptest.NewRequest(http.MethodGet, "/"+prefix+"/mirror/pkg.tar.gz", nil)
	mirrorRR := serve(p, mirrorReq)
	if mirrorRR.Code != http.StatusForbidden {
		t.Errorf("declined edit's path: status = %d, want %d (nothing learned from a declined edit)", mirrorRR.Code, http.StatusForbidden)
	}
}

// Both non-applied outcomes must relay the response body untouched: a
// RewriteSkippedForeignHost asset naming a host other than the route's own
// match-host, and a RewriteNone body with no recognizable asset field at all.
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
			upstream := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			})

			p, routes := newProxy(t, []registryvocab.RewriteRow{fakeManifestRow(rewriteFakeSingleAsset)}, []Route{{
				MatchHost:        "widget.example.com",
				EnforcedPaths:    []string{"/index"},
				EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "widget", Path: "/index"}},
				Upstream:         upstream.URL,
			}})
			prefix := routes[0].Prefix

			req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/index/manifest.json", nil)
			req.Host = "forwarder.example:9999"
			rr := serve(p, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
			}
			if got := rr.Body.String(); got != tc.body {
				t.Errorf("body = %s, want byte-identical to upstream's %s", got, tc.body)
			}
		})
	}
}

// A client that hangs up mid-stream closes the pipe and the upstream body, so
// the rewriter fails with a closed-pipe error. That is a routine disconnect,
// not a rewrite failure, and must not be logged as one.
func TestStreamRewrite_ClientHangupIsNotLoggedAsFailure(t *testing.T) {
	rewriterDone := make(chan error, 1)
	row := registryvocab.RewriteRow{
		Name:      "widget stream",
		Ecosystem: "widget",
		Method:    http.MethodGet,
		Matches: func(routeRelativePath, base string) bool {
			return routeRelativePath == registryvocab.JoinBase(base, "/stream.json")
		},
		RewriteStream: func(dst io.Writer, src io.Reader, _ registryvocab.RewriteContext, _ func(registryvocab.RewriteEdit)) error {
			_, err := io.Copy(dst, src)
			rewriterDone <- err
			return err
		},
	}

	upstream := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	p, routes := newProxy(t, []registryvocab.RewriteRow{row}, []Route{{
		MatchHost:        "widget.example.com",
		EnforcedPaths:    []string{"/"},
		EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "widget", Path: "/"}},
		Upstream:         upstream.URL,
	}})
	front := httptest.NewServer(p)
	t.Cleanup(front.Close)
	logBuf := &lockedBuffer{}
	prevOutput := log.Writer()
	log.SetOutput(logBuf)
	t.Cleanup(func() { log.SetOutput(prevOutput) })

	resp, err := http.Get(front.URL + "/" + routes[0].Prefix + "/stream.json")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if _, err := io.ReadFull(resp.Body, make([]byte, 1)); err != nil {
		t.Fatalf("read first byte: %v", err)
	}
	resp.Body.Close()

	select {
	case err := <-rewriterDone:
		if err == nil {
			t.Fatal("rewriter returned nil, want the hangup error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("rewriter never stopped after the client hung up")
	}
	// The failure line, if any, is written right after the rewriter returns.
	time.Sleep(100 * time.Millisecond)
	if logged := logBuf.String(); strings.Contains(logged, "failed") {
		t.Errorf("client hangup logged as a rewrite failure: %q", logged)
	}
}

// lockedBuffer is a log sink safe to read while the proxy's rewrite goroutine
// may still be writing.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// fakeStreamRow is a streaming row for the invented "widget" ecosystem that
// serves manifest.json under the route's base and hands the body to rewrite.
func fakeStreamRow(rewrite func(dst io.Writer, src io.Reader, rc registryvocab.RewriteContext, onEdit func(registryvocab.RewriteEdit)) error) registryvocab.RewriteRow {
	return registryvocab.RewriteRow{
		Name:      "widget stream",
		Ecosystem: "widget",
		Method:    http.MethodGet,
		Matches: func(routeRelativePath, base string) bool {
			return routeRelativePath == registryvocab.JoinBase(base, "/manifest.json")
		},
		RewriteStream: rewrite,
	}
}

func fakeStreamRoute(upstream string) Route {
	return Route{
		MatchHost:        "widget.example.com",
		EnforcedPaths:    []string{"/index"},
		EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: "widget", Path: "/index"}},
		Upstream:         upstream,
		Credential:       "sekret-stream-token",
	}
}

// A streamed rewrite well past the buffered path's cap is rewritten whole,
// drops the upstream Content-Length (the length changed and the response goes
// chunked), learns only from the applied edit, and logs the declined one as a
// skip without learning from it (issues #3401, #5003).
func TestStreamRewrite_LargeBodyRewrittenWithLearningAndNoContentLength(t *testing.T) {
	const primary = "https://widget.example.com/primary/pkg.tar.gz"
	const foreign = "https://cdn.example.com/mirror/pkg.tar.gz"
	pad := strings.Repeat("x", 4<<20)
	in := `{"pad":"` + pad + `","primary":"` + primary + `","secondary":"` + foreign + `"}`

	row := fakeStreamRow(func(dst io.Writer, src io.Reader, rc registryvocab.RewriteContext, onEdit func(registryvocab.RewriteEdit)) error {
		b, err := io.ReadAll(src)
		if err != nil {
			return err
		}
		to := "http://" + rc.Forwarder.Host + "/" + rc.Prefix + "/primary/pkg.tar.gz"
		onEdit(registryvocab.RewriteEdit{From: primary, To: to, LearnedPath: "/primary"})
		onEdit(registryvocab.RewriteEdit{From: foreign})
		_, err = io.WriteString(dst, strings.Replace(string(b), primary, to, 1))
		return err
	})
	upstream := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index/manifest.json" {
			w.Header().Set("Content-Type", "application/json")
		}
		_, _ = w.Write([]byte(in))
	})
	p, routes := newProxy(t, []registryvocab.RewriteRow{row}, []Route{fakeStreamRoute(upstream.URL)})
	prefix := routes[0].Prefix
	logBuf := captureLog(t)

	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/index/manifest.json", nil)
	req.Host = "forwarder.example:9999"
	rr := serve(p, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	want := strings.Replace(in, primary, "http://forwarder.example:9999/"+prefix+"/primary/pkg.tar.gz", 1)
	if got := rr.Body.String(); got != want {
		t.Errorf("body differs from the expected rewrite (len %d vs %d)", len(got), len(want))
	}
	if got := rr.Header().Get("Content-Length"); got != "" {
		t.Errorf("Content-Length = %q, want absent (streamed rewrite goes chunked)", got)
	}
	logged := logBuf.String()
	if !strings.Contains(logged, foreign) || strings.Contains(logged, "rewrote "+foreign) {
		t.Errorf("declined edit not logged as a skip: %.300q", logged)
	}
	if strings.Contains(logged, "sekret-stream-token") {
		t.Errorf("log output contained the credential: %.300q", logged)
	}

	if rr := serve(p, httptest.NewRequest(http.MethodGet, "/"+prefix+"/primary/pkg.tar.gz", nil)); rr.Code != http.StatusOK {
		t.Errorf("applied edit's path: status = %d, want %d (learned path must be admitted)", rr.Code, http.StatusOK)
	}
	if rr := serve(p, httptest.NewRequest(http.MethodGet, "/"+prefix+"/mirror/pkg.tar.gz", nil)); rr.Code != http.StatusForbidden {
		t.Errorf("declined edit's path: status = %d, want %d (nothing learned from a declined edit)", rr.Code, http.StatusForbidden)
	}
}

// A rewriter that abandons the stream partway has relayed the rest verbatim:
// the client gets the upstream's bytes unchanged and the abandonment is logged
// once, naming the row and path.
func TestStreamRewrite_AbandonedStreamRelaysUnchangedAndLogsOnce(t *testing.T) {
	in := `{"readme":"` + strings.Repeat("r", 2<<20) + `"}`
	row := fakeStreamRow(func(dst io.Writer, src io.Reader, _ registryvocab.RewriteContext, _ func(registryvocab.RewriteEdit)) error {
		if _, err := io.Copy(dst, src); err != nil {
			return err
		}
		return registryvocab.ErrRewriteAbandoned
	})
	upstream := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(in))
	})
	p, routes := newProxy(t, []registryvocab.RewriteRow{row}, []Route{fakeStreamRoute(upstream.URL)})
	prefix := routes[0].Prefix
	logBuf := captureLog(t)

	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/index/manifest.json", nil)
	req.Host = "forwarder.example:9999"
	rr := serve(p, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if rr.Body.String() != in {
		t.Errorf("body (len %d) differs from upstream's (len %d), want byte-identical", rr.Body.Len(), len(in))
	}
	want := "registryproxy: widget stream: stopped rewriting /" + prefix + "/index/manifest.json partway"
	if n := strings.Count(logBuf.String(), want); n != 1 {
		t.Errorf("log has %d %q lines, want exactly 1: %.400q", n, want, logBuf.String())
	}
}
