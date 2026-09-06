package ecosystem

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"spindrift.dev/launcher/internal/registryvocab"
)

// npmRewriteContext is the one route context every rewriteNpmPackument case
// below runs against: match-host registry.example.com, forwarded to a local
// port under prefix "r0". A case that needs a different host copies it and
// overrides MatchHost.
var npmRewriteContext = registryvocab.RewriteContext{
	MatchHost: "registry.example.com",
	Forwarder: &url.URL{Scheme: "http", Host: "127.0.0.1:9999"},
	Prefix:    "r0",
}

// npmPackument builds a minimal packument body with one dist.tarball per
// name->tarball pair given, so a test case can name however many versions
// it needs without hand-writing JSON.
func npmPackument(t *testing.T, tarballs map[string]string) []byte {
	t.Helper()
	versions := make(map[string]any, len(tarballs))
	for version, tarball := range tarballs {
		versions[version] = map[string]any{
			"dist": map[string]any{"tarball": tarball},
		}
	}
	body, err := json.Marshal(map[string]any{"versions": versions})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return body
}

// TestRewriteNpmPackument_AllSameHost verifies that every version's
// same-host dist.tarball is rewritten to the Forwarder, one edit each, with
// the route's prefix inserted and the tarball's own path preserved -- and
// that the edit order is deterministic (sorted by version name) despite Go's
// randomized map iteration.
func TestRewriteNpmPackument_AllSameHost(t *testing.T) {
	body := npmPackument(t, map[string]string{
		"2.0.0": "https://registry.example.com/pkg/-/pkg-2.0.0.tgz",
		"1.0.0": "https://registry.example.com/pkg/-/pkg-1.0.0.tgz",
	})
	rc := npmRewriteContext

	result := rewriteNpmPackument(body, rc)

	if result.Outcome != registryvocab.RewriteApplied {
		t.Fatalf("outcome = %v, want RewriteApplied", result.Outcome)
	}
	if len(result.Edits) != 2 {
		t.Fatalf("edits = %+v, want exactly two", result.Edits)
	}
	// Sorted by version name ("1.0.0" before "2.0.0"), not insertion order.
	wantFrom0 := "https://registry.example.com/pkg/-/pkg-1.0.0.tgz"
	wantTo0 := "http://127.0.0.1:9999/r0/pkg/-/pkg-1.0.0.tgz"
	if result.Edits[0].From != wantFrom0 || result.Edits[0].To != wantTo0 {
		t.Errorf("edit[0] = %+v, want From %q To %q", result.Edits[0], wantFrom0, wantTo0)
	}
	wantFrom1 := "https://registry.example.com/pkg/-/pkg-2.0.0.tgz"
	wantTo1 := "http://127.0.0.1:9999/r0/pkg/-/pkg-2.0.0.tgz"
	if result.Edits[1].From != wantFrom1 || result.Edits[1].To != wantTo1 {
		t.Errorf("edit[1] = %+v, want From %q To %q", result.Edits[1], wantFrom1, wantTo1)
	}

	var out map[string]any
	if err := json.Unmarshal(result.Body, &out); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	versions := out["versions"].(map[string]any)
	got1 := versions["1.0.0"].(map[string]any)["dist"].(map[string]any)["tarball"]
	got2 := versions["2.0.0"].(map[string]any)["dist"].(map[string]any)["tarball"]
	if got1 != wantTo0 {
		t.Errorf("versions[1.0.0].dist.tarball = %v, want %q", got1, wantTo0)
	}
	if got2 != wantTo1 {
		t.Errorf("versions[2.0.0].dist.tarball = %v, want %q", got2, wantTo1)
	}
}

// TestRewriteNpmPackument_HostMatchNormalization drives the same host
// comparison normalization rules rewriteCargoDL exercises: an explicit
// default port on either side, and a case difference, must still compare
// equal.
func TestRewriteNpmPackument_HostMatchNormalization(t *testing.T) {
	cases := []struct {
		name        string
		tarballHost string
		matchHost   string
	}{
		{name: "matchHost carries explicit default port", tarballHost: "registry.example.com", matchHost: "registry.example.com:443"},
		{name: "tarball carries explicit default port", tarballHost: "registry.example.com:443", matchHost: "registry.example.com"},
		{name: "case differs", tarballHost: "Registry.Example.COM", matchHost: "registry.example.com"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := npmPackument(t, map[string]string{
				"1.0.0": "https://" + tc.tarballHost + "/pkg/-/pkg-1.0.0.tgz",
			})
			rc := npmRewriteContext
			rc.MatchHost = tc.matchHost

			result := rewriteNpmPackument(body, rc)

			if result.Outcome != registryvocab.RewriteApplied {
				t.Errorf("outcome = %v, want RewriteApplied (tarballHost %q, matchHost %q)", result.Outcome, tc.tarballHost, tc.matchHost)
			}
		})
	}
}

// TestRewriteNpmPackument_AllForeignHost verifies a wholly cross-host (CDN)
// packument reports RewriteSkippedForeignHost, leaves the body byte-for-byte
// untouched, and carries From-only edits for the caller's skip log lines.
func TestRewriteNpmPackument_AllForeignHost(t *testing.T) {
	body := npmPackument(t, map[string]string{
		"1.0.0": "https://cdn.example.com/pkg/-/pkg-1.0.0.tgz",
		"2.0.0": "https://cdn.example.com/pkg/-/pkg-2.0.0.tgz",
	})
	rc := npmRewriteContext

	result := rewriteNpmPackument(body, rc)

	if result.Outcome != registryvocab.RewriteSkippedForeignHost {
		t.Fatalf("outcome = %v, want RewriteSkippedForeignHost", result.Outcome)
	}
	if string(result.Body) != string(body) {
		t.Errorf("out = %s, want byte-identical to input %s", result.Body, body)
	}
	if len(result.Edits) != 2 {
		t.Fatalf("edits = %+v, want exactly two", result.Edits)
	}
	for _, e := range result.Edits {
		if e.To != "" {
			t.Errorf("edit %+v: To = %q, want empty (declined value)", e, e.To)
		}
		if e.LearnedPath != "" {
			t.Errorf("edit %+v: LearnedPath = %q, want empty", e, e.LearnedPath)
		}
	}
}

// TestRewriteNpmPackument_Mixed verifies a packument holding both a
// same-host and a foreign-host tarball rewrites only the same-host one in
// the body, reports RewriteApplied (not a skip -- something was applied),
// and still carries the foreign one as a declined edit with an empty To.
func TestRewriteNpmPackument_Mixed(t *testing.T) {
	body := npmPackument(t, map[string]string{
		"1.0.0": "https://registry.example.com/pkg/-/pkg-1.0.0.tgz",
		"2.0.0": "https://cdn.example.com/pkg/-/pkg-2.0.0.tgz",
	})
	rc := npmRewriteContext

	result := rewriteNpmPackument(body, rc)

	if result.Outcome != registryvocab.RewriteApplied {
		t.Fatalf("outcome = %v, want RewriteApplied", result.Outcome)
	}
	if len(result.Edits) != 2 {
		t.Fatalf("edits = %+v, want exactly two", result.Edits)
	}

	wantAppliedTo := "http://127.0.0.1:9999/r0/pkg/-/pkg-1.0.0.tgz"
	if result.Edits[0].From != "https://registry.example.com/pkg/-/pkg-1.0.0.tgz" || result.Edits[0].To != wantAppliedTo {
		t.Errorf("edit[0] = %+v, want applied edit to %q", result.Edits[0], wantAppliedTo)
	}
	if result.Edits[1].From != "https://cdn.example.com/pkg/-/pkg-2.0.0.tgz" || result.Edits[1].To != "" {
		t.Errorf("edit[1] = %+v, want declined edit (empty To)", result.Edits[1])
	}

	var out map[string]any
	if err := json.Unmarshal(result.Body, &out); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	versions := out["versions"].(map[string]any)
	got1 := versions["1.0.0"].(map[string]any)["dist"].(map[string]any)["tarball"]
	if got1 != wantAppliedTo {
		t.Errorf("versions[1.0.0].dist.tarball = %v, want %q", got1, wantAppliedTo)
	}
	got2 := versions["2.0.0"].(map[string]any)["dist"].(map[string]any)["tarball"]
	if got2 != "https://cdn.example.com/pkg/-/pkg-2.0.0.tgz" {
		t.Errorf("versions[2.0.0].dist.tarball = %v, want untouched", got2)
	}
}

// TestRewriteNpmPackument_NotRewritten drives every case where
// rewriteNpmPackument must leave body byte-identical and report
// outcome=RewriteNone -- nothing recognizable to rewrite at all, distinct
// from the deliberate RewriteSkippedForeignHost tested above.
func TestRewriteNpmPackument_NotRewritten(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "not JSON", body: `not json at all`},
		{name: "trailing content after the JSON object", body: `{"versions":{}}{"junk":1}`},
		{name: "no versions key", body: `{"name":"pkg"}`},
		{name: "versions not an object", body: `{"versions":[1,2,3]}`},
		{name: "version not an object", body: `{"versions":{"1.0.0":"not an object"}}`},
		{name: "dist not an object", body: `{"versions":{"1.0.0":{"dist":"not an object"}}}`},
		{name: "tarball not a string", body: `{"versions":{"1.0.0":{"dist":{"tarball":123}}}}`},
		{name: "tarball not an absolute URL", body: `{"versions":{"1.0.0":{"dist":{"tarball":"/pkg/-/pkg-1.0.0.tgz"}}}}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			rc := npmRewriteContext

			result := rewriteNpmPackument(body, rc)

			if result.Outcome != registryvocab.RewriteNone {
				t.Fatalf("outcome = %v, want RewriteNone", result.Outcome)
			}
			if len(result.Edits) != 0 {
				t.Errorf("edits = %+v, want none", result.Edits)
			}
			if string(result.Body) != tc.body {
				t.Errorf("out = %s, want byte-identical to input %s", result.Body, tc.body)
			}
		})
	}
}

// TestRewriteNpmPackument_PreservesOtherFields verifies a numeric field
// elsewhere in the packument survives re-serialization as its exact digits
// -- the UseNumber contract -- and that fields outside the rewritten
// tarball are otherwise untouched.
func TestRewriteNpmPackument_PreservesOtherFields(t *testing.T) {
	body := []byte(`{"name":"pkg","dist-tags":{"latest":"1.0.0"},"some-count":9007199254740993,"versions":{"1.0.0":{"dist":{"tarball":"https://registry.example.com/pkg/-/pkg-1.0.0.tgz","shasum":"abc"}}}}`)
	rc := npmRewriteContext

	result := rewriteNpmPackument(body, rc)

	if result.Outcome != registryvocab.RewriteApplied {
		t.Fatalf("outcome = %v, want RewriteApplied", result.Outcome)
	}

	var out map[string]any
	if err := json.Unmarshal(result.Body, &out); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	if out["name"] != "pkg" {
		t.Errorf("name = %v, want pkg", out["name"])
	}

	// Round-trip the number through json.Number to check exact digits --
	// plain float64 unmarshal would lose precision on a value this large,
	// masking exactly the defect UseNumber exists to avoid.
	dec := json.NewDecoder(bytes.NewReader(result.Body))
	dec.UseNumber()
	var reDecoded map[string]any
	if err := dec.Decode(&reDecoded); err != nil {
		t.Fatalf("re-decode with UseNumber: %v", err)
	}
	if got := reDecoded["some-count"].(json.Number).String(); got != "9007199254740993" {
		t.Errorf("some-count = %s, want 9007199254740993", got)
	}
}

// TestRewriteNpmPackument_LearnedPath covers the same LearnedPath contract
// rewriteCargoDL's own test does: on RewriteApplied, the edit's LearnedPath
// carries the route-relative remainder the rewritten tarball's path was
// reduced to, with an empty path normalized to "/" (the root-subtree
// sentinel), not "".
func TestRewriteNpmPackument_LearnedPath(t *testing.T) {
	tests := []struct {
		name    string
		tarball string
		want    string
	}{
		{
			name:    "absolute path preserved verbatim",
			tarball: "https://registry.example.com/pkg/-/pkg-1.0.0.tgz",
			want:    "/pkg/-/pkg-1.0.0.tgz",
		},
		{
			name:    "bare host with no path at all normalizes to root, not empty string",
			tarball: "https://registry.example.com",
			want:    "/",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := npmPackument(t, map[string]string{"1.0.0": tc.tarball})
			rc := npmRewriteContext

			result := rewriteNpmPackument(body, rc)

			if result.Outcome != registryvocab.RewriteApplied {
				t.Fatalf("outcome = %v, want RewriteApplied", result.Outcome)
			}
			if len(result.Edits) != 1 {
				t.Fatalf("edits = %+v, want exactly one", result.Edits)
			}
			if result.Edits[0].LearnedPath != tc.want {
				t.Errorf("learnedPath = %q, want %q", result.Edits[0].LearnedPath, tc.want)
			}
		})
	}
}

// TestNpmRow_RewriteRows_PackumentMatches drives npmRow's declared
// RewriteRows entry: its Matches func, its Ecosystem/Method tags, and the
// matcher's path-shape rules -- unscoped and decoded-scoped packument paths
// match, a tarball path (more segments) never matches regardless of
// scoping, a root base is handled, and a trailing slash never matches.
func TestNpmRow_RewriteRows_PackumentMatches(t *testing.T) {
	rows := npmRow.RewriteRows
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	row := rows[0]

	if row.Ecosystem != nameNpm {
		t.Errorf("Ecosystem = %q, want %q", row.Ecosystem, nameNpm)
	}
	if row.Method != http.MethodGet {
		t.Errorf("Method = %q, want %q", row.Method, http.MethodGet)
	}

	cases := []struct {
		name    string
		path    string
		base    string
		matches bool
	}{
		{name: "root base unscoped name", path: "/pkg", base: "/", matches: true},
		{name: "non-root base unscoped name", path: "/r0/pkg", base: "/r0", matches: true},
		{name: "root base decoded scoped name", path: "/@scope/pkg", base: "/", matches: true},
		{name: "non-root base decoded scoped name", path: "/r0/@scope/pkg", base: "/r0", matches: true},
		{name: "unscoped tarball path does not match", path: "/pkg/-/pkg-1.0.0.tgz", base: "/", matches: false},
		{name: "scoped tarball path does not match", path: "/@scope/pkg/-/pkg-1.0.0.tgz", base: "/", matches: false},
		{name: "near-miss base prefix", path: "/r0foo/pkg", base: "/r0", matches: false},
		{name: "trailing slash does not match", path: "/pkg/", base: "/", matches: false},
		{name: "bare @ segment does not match", path: "/@", base: "/", matches: false},
		{name: "no package name at all", path: "/", base: "/", matches: false},
		{name: "tarball directory marker does not match", path: "/-", base: "/", matches: false},
		{name: "dot segment does not match", path: "/.", base: "/", matches: false},
		{name: "dot-dot segment does not match", path: "/..", base: "/", matches: false},
		{name: "scoped tarball directory marker does not match", path: "/@scope/-", base: "/", matches: false},
		{name: "scoped dot segment does not match", path: "/@scope/.", base: "/", matches: false},
		{name: "scoped dot-dot segment does not match", path: "/@scope/..", base: "/", matches: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := row.Matches(tc.path, tc.base); got != tc.matches {
				t.Errorf("Matches(%q, %q) = %v, want %v", tc.path, tc.base, got, tc.matches)
			}
		})
	}
}
