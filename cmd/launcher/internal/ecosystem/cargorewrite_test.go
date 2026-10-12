package ecosystem

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"spindrift.dev/launcher/internal/registryvocab"
)

// Every rewriteCargoDL case below runs against this one route context. A case
// that needs a different host copies it and overrides MatchHost.
var cargoRewriteContext = registryvocab.RewriteContext{
	MatchHost: "crates.example.com",
	Forwarder: &url.URL{Scheme: "http", Host: "127.0.0.1:9999"},
	Prefix:    "r0",
}

func TestRewriteCargoDL_MatchingHost(t *testing.T) {
	body := []byte(`{"dl":"https://crates.example.com/api/v1/crates","api":"https://crates.example.com"}`)
	rc := cargoRewriteContext

	result := rewriteCargoDL(body, rc)

	if result.Outcome != registryvocab.RewriteApplied {
		t.Fatalf("outcome = %v, want RewriteApplied", result.Outcome)
	}
	if len(result.Edits) != 1 {
		t.Fatalf("edits = %v, want exactly one", result.Edits)
	}
	wantFrom := "https://crates.example.com/api/v1/crates"
	if result.Edits[0].From != wantFrom {
		t.Errorf("from = %q, want %q", result.Edits[0].From, wantFrom)
	}
	wantTo := "http://127.0.0.1:9999/r0/api/v1/crates"
	if result.Edits[0].To != wantTo {
		t.Errorf("to = %q, want %q", result.Edits[0].To, wantTo)
	}
	wantOut := `{"api":"https://crates.example.com","dl":"http://127.0.0.1:9999/r0/api/v1/crates"}`
	if string(result.Body) != wantOut {
		t.Errorf("out = %s, want %s", result.Body, wantOut)
	}
}

// Rewriting a foreign-host dl would turn the proxy into an open relay for
// whatever host a dl happens to name. The outcome must be the dedicated
// RewriteSkippedForeignHost, not RewriteNone: this is the one skip case the
// caller logs, and the edit carries the untouched dl for that log line.
func TestRewriteCargoDL_DifferentHostLeftAlone(t *testing.T) {
	body := []byte(`{"dl":"https://cdn.example.com/api/v1/crates"}`)
	rc := cargoRewriteContext

	result := rewriteCargoDL(body, rc)

	if result.Outcome != registryvocab.RewriteSkippedForeignHost {
		t.Fatalf("outcome = %v, want RewriteSkippedForeignHost", result.Outcome)
	}
	if len(result.Edits) != 1 {
		t.Fatalf("edits = %v, want exactly one", result.Edits)
	}
	wantFrom := "https://cdn.example.com/api/v1/crates"
	if result.Edits[0].From != wantFrom {
		t.Errorf("from = %q, want %q", result.Edits[0].From, wantFrom)
	}
	if string(result.Body) != string(body) {
		t.Errorf("out = %s, want byte-identical to input %s", result.Body, body)
	}
}

func TestRewriteCargoDL_HostMatchNormalization(t *testing.T) {
	cases := []struct {
		name      string
		dlHost    string
		matchHost string
	}{
		{name: "matchHost carries explicit default port", dlHost: "crates.example.com", matchHost: "crates.example.com:443"},
		{name: "dl carries explicit default port", dlHost: "crates.example.com:443", matchHost: "crates.example.com"},
		{name: "case differs", dlHost: "Crates.Example.COM", matchHost: "crates.example.com"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"dl":"https://` + tc.dlHost + `/api/v1/crates"}`)
			rc := cargoRewriteContext
			rc.MatchHost = tc.matchHost

			result := rewriteCargoDL(body, rc)

			if result.Outcome != registryvocab.RewriteApplied {
				t.Errorf("outcome = %v, want RewriteApplied (dlHost %q, matchHost %q)", result.Outcome, tc.dlHost, tc.matchHost)
			}
		})
	}
}

// RewriteNone means nothing recognizable to rewrite at all, unlike the
// deliberate RewriteSkippedForeignHost above. The trailing-content case is the
// subtle one: a json.Decoder decodes just the first object and would silently
// drop the trailing bytes on re-serialization.
func TestRewriteCargoDL_NotRewritten(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "not JSON", body: `not json at all`},
		{name: "no dl field", body: `{"api":"https://crates.example.com"}`},
		{name: "dl not a string", body: `{"dl":123}`},
		{name: "trailing content after the JSON object", body: `{"dl":"https://crates.example.com/api/v1/crates"}{"junk":1}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			rc := cargoRewriteContext

			result := rewriteCargoDL(body, rc)

			if result.Outcome != registryvocab.RewriteNone {
				t.Fatalf("outcome = %v, want RewriteNone", result.Outcome)
			}
			if len(result.Edits) != 0 {
				t.Errorf("edits = %v, want none", result.Edits)
			}
			if string(result.Body) != tc.body {
				t.Errorf("out = %s, want byte-identical to input %s", result.Body, tc.body)
			}
		})
	}
}

// The fixture's large "some-count" is deliberate: a plain json.Unmarshal
// round-trips a JSON number through float64 and loses its precision.
func TestRewriteCargoDL_PreservesOtherFields(t *testing.T) {
	body := []byte(`{"dl":"https://crates.example.com/api/v1/crates","api":"https://crates.example.com","auth-required":true,"some-count":9007199254740993}`)
	rc := cargoRewriteContext

	result := rewriteCargoDL(body, rc)

	if result.Outcome != registryvocab.RewriteApplied {
		t.Fatalf("outcome = %v, want RewriteApplied", result.Outcome)
	}
	wantOut := `{"api":"https://crates.example.com","auth-required":true,"dl":"http://127.0.0.1:9999/r0/api/v1/crates","some-count":9007199254740993}`
	if string(result.Body) != wantOut {
		t.Errorf("out = %s, want %s", result.Body, wantOut)
	}
}

// This test pins issue #3257. A route's upstream is always a bare origin
// (registryproxy.New rejects any other kind), so the route-relative remainder
// is the dl's full absolute path on the upstream host, the same shape a
// Subtree's Path uses.
func TestRewriteCargoDL_LearnedPath(t *testing.T) {
	tests := []struct {
		name string
		dl   string
		want string
	}{
		{
			name: "absolute path preserved verbatim",
			dl:   "https://crates.example.com/api/v1/crates-a",
			want: "/api/v1/crates-a",
		},
		{
			name: "bare host with no path at all normalizes to root, not empty string",
			dl:   "https://crates.example.com",
			want: "/",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"dl":"` + tc.dl + `"}`)
			rc := cargoRewriteContext

			result := rewriteCargoDL(body, rc)

			if result.Outcome != registryvocab.RewriteApplied {
				t.Fatalf("outcome = %v, want RewriteApplied", result.Outcome)
			}
			if len(result.Edits) != 1 {
				t.Fatalf("edits = %v, want exactly one", result.Edits)
			}
			if result.Edits[0].LearnedPath != tc.want {
				t.Errorf("learnedPath = %q, want %q", result.Edits[0].LearnedPath, tc.want)
			}
		})
	}
}

// Cargo substitutes {crate}, {version}, {prefix}, {lowerprefix} and
// {sha256-checksum} in dl before fetching, so the rewritten dl must keep them
// as literal braces and the learned path must stop before the first one: the
// substituted download paths are what the route has to admit.
func TestRewriteCargoDL_Markers(t *testing.T) {
	tests := []struct {
		name        string
		dl          string
		wantTo      string
		wantLearned string
		admits      string
	}{
		{
			name:        "marker mid-segment",
			dl:          "https://crates.example.com/basic/o/r/cargo/{crate}-{version}.crate",
			wantTo:      "http://127.0.0.1:9999/r0/basic/o/r/cargo/{crate}-{version}.crate",
			wantLearned: "/basic/o/r/cargo",
			admits:      "/basic/o/r/cargo/serde-1.0.0.crate",
		},
		{
			name:        "multi-segment template",
			dl:          "https://crates.example.com/api/v1/crates/{prefix}/{crate}/{version}/{sha256-checksum}",
			wantTo:      "http://127.0.0.1:9999/r0/api/v1/crates/{prefix}/{crate}/{version}/{sha256-checksum}",
			wantLearned: "/api/v1/crates",
			admits:      "/api/v1/crates/se/serde/1.0.0/abc123",
		},
		{
			name:        "escaped segment before a marker",
			dl:          "https://crates.example.com/a%20b/{crate}",
			wantTo:      "http://127.0.0.1:9999/r0/a%20b/{crate}",
			wantLearned: "/a b",
			admits:      "/a b/serde",
		},
		{
			name:        "markers in the query",
			dl:          "https://crates.example.com/dl?c={crate}&v={version}",
			wantTo:      "http://127.0.0.1:9999/r0/dl?c={crate}&v={version}",
			wantLearned: "/dl",
			admits:      "/dl",
		},
		{
			name:        "marker in the first segment learns the whole host",
			dl:          "https://crates.example.com/{crate}/{version}/download",
			wantTo:      "http://127.0.0.1:9999/r0/{crate}/{version}/download",
			wantLearned: "/",
			admits:      "/serde/1.0.0/download",
		},
		{
			name:        "doubled slash before a marker",
			dl:          "https://crates.example.com/a//{crate}",
			wantTo:      "http://127.0.0.1:9999/r0/a//{crate}",
			wantLearned: "/a",
			admits:      "/a/serde",
		},
		{
			name:        "encoded marker in the query is kept verbatim",
			dl:          "https://crates.example.com/a/{crate}?q=%7Bcrate%7D",
			wantTo:      "http://127.0.0.1:9999/r0/a/{crate}?q=%7Bcrate%7D",
			wantLearned: "/a",
			admits:      "/a/serde",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"dl":"` + tc.dl + `"}`)
			rc := cargoRewriteContext

			result := rewriteCargoDL(body, rc)

			if result.Outcome != registryvocab.RewriteApplied {
				t.Fatalf("outcome = %v, want RewriteApplied", result.Outcome)
			}
			if len(result.Edits) != 1 {
				t.Fatalf("edits = %v, want exactly one", result.Edits)
			}
			if result.Edits[0].To != tc.wantTo {
				t.Errorf("to = %q, want %q", result.Edits[0].To, tc.wantTo)
			}
			var out map[string]string
			if err := json.Unmarshal(result.Body, &out); err != nil {
				t.Fatalf("body %s: %v", result.Body, err)
			}
			if out["dl"] != tc.wantTo {
				t.Errorf("body dl = %q, want %q", out["dl"], tc.wantTo)
			}
			if result.Edits[0].LearnedPath != tc.wantLearned {
				t.Errorf("learnedPath = %q, want %q", result.Edits[0].LearnedPath, tc.wantLearned)
			}
			if !(registryvocab.PathSet{result.Edits[0].LearnedPath}).Admits(tc.admits) {
				t.Errorf("PathSet{%q} does not admit %q", result.Edits[0].LearnedPath, tc.admits)
			}
		})
	}
}

// A dl that genuinely spells a marker percent-encoded is not a template and
// must not be turned into one.
func TestRewriteCargoDL_EncodedMarkerNotATemplate(t *testing.T) {
	body := []byte(`{"dl":"https://crates.example.com/x/%7Bcrate%7D"}`)

	result := rewriteCargoDL(body, cargoRewriteContext)

	if result.Outcome != registryvocab.RewriteApplied {
		t.Fatalf("outcome = %v, want RewriteApplied", result.Outcome)
	}
	if len(result.Edits) != 1 {
		t.Fatalf("edits = %v, want exactly one", result.Edits)
	}
	if want := "http://127.0.0.1:9999/r0/x/%7Bcrate%7D"; result.Edits[0].To != want {
		t.Errorf("to = %q, want %q", result.Edits[0].To, want)
	}
	if want := "/x/{crate}"; result.Edits[0].LearnedPath != want {
		t.Errorf("learnedPath = %q, want %q", result.Edits[0].LearnedPath, want)
	}
}

func TestCargoRow_RewriteRows_ConfigJSONMatches(t *testing.T) {
	rows := cargoRow.RewriteRows
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	row := rows[0]

	if row.Ecosystem != "cargo" {
		t.Errorf("Ecosystem = %q, want %q", row.Ecosystem, "cargo")
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
		{name: "root base bare config.json", path: "/config.json", base: "/", matches: true},
		{name: "non-root base config.json", path: "/index/config.json", base: "/index", matches: true},
		{name: "near-miss base prefix", path: "/indexfoo/config.json", base: "/index", matches: false},
		{name: "near-miss trailing suffix", path: "/config.json.bak", base: "/", matches: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := row.Matches(tc.path, tc.base); got != tc.matches {
				t.Errorf("Matches(%q, %q) = %v, want %v", tc.path, tc.base, got, tc.matches)
			}
		})
	}
}
