package ecosystem

import (
	"net/http"
	"net/url"
	"testing"

	"spindrift.dev/launcher/internal/registryvocab"
)

// cargoRewriteContext is the one route context every rewriteCargoDL case
// below runs against: match-host crates.example.com, forwarded to a local
// port under prefix "r0". A case that needs a different host copies it and
// overrides MatchHost.
var cargoRewriteContext = registryvocab.RewriteContext{
	MatchHost: "crates.example.com",
	Forwarder: &url.URL{Scheme: "http", Host: "127.0.0.1:9999"},
	Prefix:    "r0",
}

// TestRewriteCargoDL_MatchingHost verifies that a config.json body whose "dl"
// names the route's own match-host is rewritten to the Forwarder, with the
// route's prefix inserted and the dl's own path preserved.
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

// TestRewriteCargoDL_DifferentHostLeftAlone verifies that a dl naming any
// host other than the route's match-host -- a CDN, a mirror -- is left
// exactly alone: rewriting it would turn the proxy into an open relay for
// whatever host a dl happens to name. The outcome is the dedicated
// RewriteSkippedForeignHost value, not merely "not rewritten" -- this is the
// one skip case the caller logs, distinct from RewriteNone (nothing
// recognizable to rewrite at all, e.g. no dl field), and the edit carries
// the untouched dl for that log line.
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

// TestRewriteCargoDL_HostMatchNormalization drives the host comparison's
// normalization rules: an explicit default port on either side, and a case
// difference, must still compare equal.
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

// TestRewriteCargoDL_NotRewritten drives the cases where rewriteCargoDL must
// leave body byte-identical and report outcome=RewriteNone -- nothing
// recognizable to rewrite at all, unlike the deliberate RewriteSkippedForeignHost
// (see TestRewriteCargoDL_DifferentHostLeftAlone), so the caller's log line
// for it names only the row, never a from/to value: a body that isn't JSON at all,
// one with no "dl" field, one whose "dl" isn't a string, and one that decodes
// as a single JSON object but carries trailing bytes after it -- a
// json.Decoder would otherwise decode just the first object and silently
// drop that trailing content on re-serialization.
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

// TestRewriteCargoDL_PreservesOtherFields verifies every other field of the
// config.json object -- including one whose value is a JSON number, which
// round-trips through float64 and loses precision under plain
// json.Unmarshal -- survives the rewrite unchanged.
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

// TestRewriteCargoDL_LearnedPath covers issue #3257: on a
// RewriteApplied outcome, the edit's LearnedPath carries the same
// route-relative remainder the rewritten dl's path was reduced to. A
// route's upstream is a bare origin (registryproxy.New rejects any other
// kind), so that remainder is the dl's full absolute path on the upstream
// host -- the same shape a Subtree's Path already uses.
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

// TestCargoRow_RewriteRows_ConfigJSONMatches drives cargoRow's declared
// RewriteRows entry: its Matches func must accept "/config.json" under a
// root base and "/index/config.json" under a non-root base, and reject a
// near-miss path that merely resembles the shape (no suffix-guessing).
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
