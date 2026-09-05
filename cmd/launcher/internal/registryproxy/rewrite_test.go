package registryproxy

import (
	"net/http"
	"net/url"
	"testing"
)

// TestRewriteCargoDL_MatchingHost verifies that a config.json body whose "dl"
// names the route's own match-host is rewritten to the Forwarder, with the
// route's prefix inserted and the dl's own path preserved.
func TestRewriteCargoDL_MatchingHost(t *testing.T) {
	body := []byte(`{"dl":"https://crates.example.com/api/v1/crates","api":"https://crates.example.com"}`)
	rc := rewriteContext{matchHost: "crates.example.com", forwarder: &url.URL{Scheme: "http", Host: "127.0.0.1:9999"}, prefix: "r0"}

	result := rewriteCargoDL(body, rc)

	if result.outcome != rewriteApplied {
		t.Fatalf("outcome = %v, want rewriteApplied", result.outcome)
	}
	wantFrom := "https://crates.example.com/api/v1/crates"
	if result.from != wantFrom {
		t.Errorf("from = %q, want %q", result.from, wantFrom)
	}
	wantTo := "http://127.0.0.1:9999/r0/api/v1/crates"
	if result.to != wantTo {
		t.Errorf("to = %q, want %q", result.to, wantTo)
	}
	wantOut := `{"api":"https://crates.example.com","dl":"http://127.0.0.1:9999/r0/api/v1/crates"}`
	if string(result.body) != wantOut {
		t.Errorf("out = %s, want %s", result.body, wantOut)
	}
}

// TestRewriteCargoDL_DifferentHostLeftAlone verifies that a dl naming any
// host other than the route's match-host -- a CDN, a mirror -- is left
// exactly alone: rewriting it would turn the proxy into an open relay for
// whatever host a dl happens to name. The outcome is the dedicated
// rewriteSkippedForeignHost value, not merely "not rewritten" -- this is the
// one skip case the caller logs, distinct from rewriteNone (nothing
// recognizable to rewrite at all, e.g. no dl field), and from carries the
// untouched dl for that log line.
func TestRewriteCargoDL_DifferentHostLeftAlone(t *testing.T) {
	body := []byte(`{"dl":"https://cdn.example.com/api/v1/crates"}`)
	rc := rewriteContext{matchHost: "crates.example.com", forwarder: &url.URL{Scheme: "http", Host: "127.0.0.1:9999"}, prefix: "r0"}

	result := rewriteCargoDL(body, rc)

	if result.outcome != rewriteSkippedForeignHost {
		t.Fatalf("outcome = %v, want rewriteSkippedForeignHost", result.outcome)
	}
	wantFrom := "https://cdn.example.com/api/v1/crates"
	if result.from != wantFrom {
		t.Errorf("from = %q, want %q", result.from, wantFrom)
	}
	if string(result.body) != string(body) {
		t.Errorf("out = %s, want byte-identical to input %s", result.body, body)
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
			rc := rewriteContext{matchHost: tc.matchHost, forwarder: &url.URL{Scheme: "http", Host: "127.0.0.1:9999"}, prefix: "r0"}

			result := rewriteCargoDL(body, rc)

			if result.outcome != rewriteApplied {
				t.Errorf("outcome = %v, want rewriteApplied (dlHost %q, matchHost %q)", result.outcome, tc.dlHost, tc.matchHost)
			}
		})
	}
}

// TestRewriteCargoDL_NotRewritten drives the cases where rewriteCargoDL must
// leave body byte-identical and report outcome=rewriteNone -- nothing
// recognizable to rewrite at all, unlike the deliberate rewriteSkippedForeignHost
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
			rc := rewriteContext{matchHost: "crates.example.com", forwarder: &url.URL{Scheme: "http", Host: "127.0.0.1:9999"}, prefix: "r0"}

			result := rewriteCargoDL(body, rc)

			if result.outcome != rewriteNone {
				t.Fatalf("outcome = %v, want rewriteNone", result.outcome)
			}
			if string(result.body) != tc.body {
				t.Errorf("out = %s, want byte-identical to input %s", result.body, tc.body)
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
	rc := rewriteContext{matchHost: "crates.example.com", forwarder: &url.URL{Scheme: "http", Host: "127.0.0.1:9999"}, prefix: "r0"}

	result := rewriteCargoDL(body, rc)

	if result.outcome != rewriteApplied {
		t.Fatalf("outcome = %v, want rewriteApplied", result.outcome)
	}
	wantOut := `{"api":"https://crates.example.com","auth-required":true,"dl":"http://127.0.0.1:9999/r0/api/v1/crates","some-count":9007199254740993}`
	if string(result.body) != wantOut {
		t.Errorf("out = %s, want %s", result.body, wantOut)
	}
}

// TestRewriteCargoDL_LearnedPath covers issue #3257: on a
// rewriteApplied outcome, learnedPath carries the same route-relative
// remainder the rewritten dl's path was reduced to. A route's upstream is a
// bare origin (New rejects any other kind), so that remainder is the dl's
// full absolute path on the upstream host -- the same shape CargoIndexBases
// entries already use.
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
			rc := rewriteContext{
				matchHost: "crates.example.com",
				forwarder: &url.URL{Scheme: "http", Host: "127.0.0.1:9999"},
				prefix:    "r0",
			}

			result := rewriteCargoDL(body, rc)

			if result.outcome != rewriteApplied {
				t.Fatalf("outcome = %v, want rewriteApplied", result.outcome)
			}
			if result.learnedPath != tc.want {
				t.Errorf("learnedPath = %q, want %q", result.learnedPath, tc.want)
			}
		})
	}
}

// TestFindResponseRewriteRow_HostRootedMatchesPerIndexBase verifies a
// host-rooted route's row match is keyed against membership in
// cargoIndexBases, not against the bare "/config.json" literal -- and that a
// path merely resembling "<base>/config.json" without exactly matching one
// of the declared bases is not a match (no suffix-guessing).
func TestFindResponseRewriteRow_HostRootedMatchesPerIndexBase(t *testing.T) {
	rs := routeState{cargoIndexBases: []string{"/index-a", "/index-b"}}

	row, base := findResponseRewriteRow(http.MethodGet, "/index-a/config.json", rs)
	if row == nil {
		t.Fatal("row = nil, want a match under /index-a")
	}
	if base != "/index-a" {
		t.Errorf("base = %q, want %q", base, "/index-a")
	}

	row, base = findResponseRewriteRow(http.MethodGet, "/index-b/config.json", rs)
	if row == nil {
		t.Fatal("row = nil, want a match under /index-b")
	}
	if base != "/index-b" {
		t.Errorf("base = %q, want %q", base, "/index-b")
	}

	if row, _ := findResponseRewriteRow(http.MethodGet, "/config.json", rs); row != nil {
		t.Error("row matched the bare literal on a route with no \"/\" base, want no match")
	}
	if row, _ := findResponseRewriteRow(http.MethodGet, "/index-a/config-json-wannabe", rs); row != nil {
		t.Error("row matched a path merely resembling <base>/config.json, want no suffix-guessing")
	}
	if row, _ := findResponseRewriteRow(http.MethodGet, "/index-c/config.json", rs); row != nil {
		t.Error("row matched an undeclared index base, want no match")
	}
}

// TestFindResponseRewriteRow_HostRootedRootBaseMeansBareLiteral verifies a
// host-rooted route's cargoIndexBases entry of "/" (the whole-host
// sentinel) combines with row.path as the bare literal itself, not
// "//config.json".
func TestFindResponseRewriteRow_HostRootedRootBaseMeansBareLiteral(t *testing.T) {
	rs := routeState{cargoIndexBases: []string{"/"}}

	row, base := findResponseRewriteRow(http.MethodGet, "/config.json", rs)
	if row == nil {
		t.Fatal("row = nil, want a match on the bare literal for a \"/\" base")
	}
	if base != "/" {
		t.Errorf("base = %q, want %q", base, "/")
	}

	if row, _ := findResponseRewriteRow(http.MethodGet, "//config.json", rs); row != nil {
		t.Error("row matched \"//config.json\", want the \"/\" base never to double the leading slash")
	}
}
