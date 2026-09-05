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
	rc := rewriteContext{matchHost: "crates.example.com", forwarder: &url.URL{Scheme: "http", Host: "127.0.0.1:9999"}, prefix: "r0", upstreamURL: &url.URL{}}

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

// TestRewriteCargoDL_PreservesUpstreamBasePath guards against issue #2854's
// path-double-join defect resurfacing one hop later (issue #3175's blocking
// review finding): a dl whose own path already carries the route's upstream
// base path (an Artifactory-style remote repo, say) must have that base
// path stripped before the route prefix goes on, so the rewritten dl is
// route-relative. Fetching that dl back through the proxy then re-joins the
// stripped remainder onto the route's upstream base path exactly once,
// rather than joining the un-stripped base path a second time on top of the
// base path already baked into the route's own upstream URL.
func TestRewriteCargoDL_PreservesUpstreamBasePath(t *testing.T) {
	body := []byte(`{"dl":"https://artifactory.example.com/artifactory/api/cargo/example-remote/api/v1/crates"}`)
	rc := rewriteContext{
		matchHost:   "artifactory.example.com",
		forwarder:   &url.URL{Scheme: "http", Host: "127.0.0.1:9999"},
		prefix:      "r0",
		upstreamURL: &url.URL{Path: "/artifactory/api/cargo/example-remote"},
	}

	result := rewriteCargoDL(body, rc)

	if result.outcome != rewriteApplied {
		t.Fatalf("outcome = %v, want rewriteApplied", result.outcome)
	}
	wantTo := "http://127.0.0.1:9999/r0/api/v1/crates"
	if result.to != wantTo {
		t.Errorf("to = %q, want %q", result.to, wantTo)
	}
	wantOut := `{"dl":"` + wantTo + `"}`
	if string(result.body) != wantOut {
		t.Errorf("out = %s, want %s", result.body, wantOut)
	}
}

// TestRewriteCargoDL_SegmentBoundaryNotStripped guards the boundary the
// blocking finding requires: an upstream base path of "/repo" must not be
// stripped from a dl path of "/repository/x" -- the two merely share a
// string prefix, not a path segment. Because the dl then has no
// route-relative form under this route's base path, the outcome is the
// dedicated rewriteSkippedOutsideBasePath, not a wrongly-stripped rewrite.
func TestRewriteCargoDL_SegmentBoundaryNotStripped(t *testing.T) {
	body := []byte(`{"dl":"https://crates.example.com/repository/x"}`)
	rc := rewriteContext{
		matchHost:   "crates.example.com",
		forwarder:   &url.URL{Scheme: "http", Host: "127.0.0.1:9999"},
		prefix:      "r0",
		upstreamURL: &url.URL{Path: "/repo"},
	}

	result := rewriteCargoDL(body, rc)

	if result.outcome != rewriteSkippedOutsideBasePath {
		t.Fatalf("outcome = %v, want rewriteSkippedOutsideBasePath", result.outcome)
	}
	if string(result.body) != string(body) {
		t.Errorf("out = %s, want byte-identical to input %s", result.body, body)
	}
}

// TestRewriteCargoDL_NotUnderBasePathLeftAlone verifies a dl whose host
// matches the route's match-host but whose path is not under the route's
// upstream base path at all is left byte-identical, with the dedicated
// rewriteSkippedOutsideBasePath outcome -- distinct from rewriteNone -- so
// the caller logs the deliberate skip.
func TestRewriteCargoDL_NotUnderBasePathLeftAlone(t *testing.T) {
	body := []byte(`{"dl":"https://artifactory.example.com/other-repo/api/v1/crates"}`)
	rc := rewriteContext{
		matchHost:   "artifactory.example.com",
		forwarder:   &url.URL{Scheme: "http", Host: "127.0.0.1:9999"},
		prefix:      "r0",
		upstreamURL: &url.URL{Path: "/artifactory/api/cargo/example-remote"},
	}

	result := rewriteCargoDL(body, rc)

	if result.outcome != rewriteSkippedOutsideBasePath {
		t.Fatalf("outcome = %v, want rewriteSkippedOutsideBasePath", result.outcome)
	}
	wantFrom := "https://artifactory.example.com/other-repo/api/v1/crates"
	if result.from != wantFrom {
		t.Errorf("from = %q, want %q", result.from, wantFrom)
	}
	if string(result.body) != string(body) {
		t.Errorf("out = %s, want byte-identical to input %s", result.body, body)
	}
}

// TestStripBasePath drives stripBasePath directly: exact segment-boundary
// matches strip, a bare string-prefix match (no segment boundary) does not.
func TestStripBasePath(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		base     string
		wantRest string
		wantOK   bool
	}{
		{name: "no base path at all", path: "/api/v1/crates", base: "", wantRest: "/api/v1/crates", wantOK: true},
		{name: "root base path", path: "/api/v1/crates", base: "/", wantRest: "/api/v1/crates", wantOK: true},
		{name: "exact match, nothing left over", path: "/repo", base: "/repo", wantRest: "", wantOK: true},
		{name: "base with trailing slash configured", path: "/repo/x", base: "/repo/", wantRest: "/x", wantOK: true},
		{name: "segment boundary respected", path: "/repo/x", base: "/repo", wantRest: "/x", wantOK: true},
		{name: "string-prefix but not a segment boundary", path: "/repository/x", base: "/repo", wantRest: "", wantOK: false},
		{name: "unrelated path", path: "/other-repo/x", base: "/repo", wantRest: "", wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rest, ok := stripBasePath(tc.path, tc.base)
			if ok != tc.wantOK || rest != tc.wantRest {
				t.Errorf("stripBasePath(%q, %q) = (%q, %v), want (%q, %v)", tc.path, tc.base, rest, ok, tc.wantRest, tc.wantOK)
			}
		})
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
	rc := rewriteContext{matchHost: "crates.example.com", forwarder: &url.URL{Scheme: "http", Host: "127.0.0.1:9999"}, prefix: "r0", upstreamURL: &url.URL{}}

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
			rc := rewriteContext{matchHost: tc.matchHost, forwarder: &url.URL{Scheme: "http", Host: "127.0.0.1:9999"}, prefix: "r0", upstreamURL: &url.URL{}}

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
			rc := rewriteContext{matchHost: "crates.example.com", forwarder: &url.URL{Scheme: "http", Host: "127.0.0.1:9999"}, prefix: "r0", upstreamURL: &url.URL{}}

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
	rc := rewriteContext{matchHost: "crates.example.com", forwarder: &url.URL{Scheme: "http", Host: "127.0.0.1:9999"}, prefix: "r0", upstreamURL: &url.URL{}}

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
// remainder (rest) the rewritten dl's path was reduced to. A host-rooted
// route always has an empty upstreamURL.Path (New rejects any other kind),
// so stripBasePath returns dl's path unchanged -- learnedPath is therefore
// the dl's full absolute path on the upstream host, the same shape
// CargoIndexBases entries already use.
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
				matchHost:   "crates.example.com",
				forwarder:   &url.URL{Scheme: "http", Host: "127.0.0.1:9999"},
				prefix:      "r0",
				upstreamURL: &url.URL{},
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

// TestFindResponseRewriteRow_LegacyRouteExactMatchOnly verifies a
// non-host-rooted route's row match is byte-identical to the pre-#3257
// behavior: only the literal "/config.json" matches, regardless of
// cargoIndexBases (which a legacy route never populates).
func TestFindResponseRewriteRow_LegacyRouteExactMatchOnly(t *testing.T) {
	rs := routeState{hostRooted: false}

	row, base := findResponseRewriteRow(http.MethodGet, "/config.json", rs)
	if row == nil {
		t.Fatal("row = nil, want a match on the literal path")
	}
	if base != "" {
		t.Errorf("base = %q, want empty for a legacy route match", base)
	}

	if row, _ := findResponseRewriteRow(http.MethodGet, "/other/config.json", rs); row != nil {
		t.Error("row matched a non-literal path on a legacy route, want no match")
	}
	if row, _ := findResponseRewriteRow(http.MethodHead, "/config.json", rs); row != nil {
		t.Error("row matched HEAD, want no row to name any method but GET")
	}
}

// TestFindResponseRewriteRow_HostRootedMatchesPerIndexBase verifies a
// host-rooted route's row match is keyed against membership in
// cargoIndexBases, not against the bare "/config.json" literal -- and that a
// path merely resembling "<base>/config.json" without exactly matching one
// of the declared bases is not a match (no suffix-guessing).
func TestFindResponseRewriteRow_HostRootedMatchesPerIndexBase(t *testing.T) {
	rs := routeState{hostRooted: true, cargoIndexBases: []string{"/index-a", "/index-b"}}

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
		t.Error("row matched the bare literal on a host-rooted route with no \"/\" base, want no match")
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
	rs := routeState{hostRooted: true, cargoIndexBases: []string{"/"}}

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
