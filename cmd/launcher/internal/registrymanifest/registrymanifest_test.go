package registrymanifest

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/registryvocab"
)

// TestEnvVar pins the env var name the launcher and the bind-registry verb
// agree on (ADR 0045) -- a typo here silently splits the two sides of the
// handoff, since Go gives no compile-time link between a minted os.Setenv
// call and a Getenv one.
func TestEnvVar(t *testing.T) {
	const want = "REGISTRY_PROXY_MANIFEST"
	if EnvVar != want {
		t.Fatalf("EnvVar = %q, want %q", EnvVar, want)
	}
}

// TestParseEndpoint_Unix verifies the unix:// scheme parses into an Endpoint
// whose SocketPath recovers the exact path, with no scheme confusion.
func TestParseEndpoint_Unix(t *testing.T) {
	ep, err := ParseEndpoint("unix:///registry-proxy.sock")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ep.IsUnix() || ep.IsTCP() {
		t.Fatalf("ep = %+v, want IsUnix true, IsTCP false", ep)
	}
	if got := ep.SocketPath(); got != "/registry-proxy.sock" {
		t.Fatalf("SocketPath() = %q, want %q", got, "/registry-proxy.sock")
	}
}

// TestParseEndpoint_TCP verifies the tcp:// scheme parses into an Endpoint
// whose Host/Port accessors recover the host and port separately.
func TestParseEndpoint_TCP(t *testing.T) {
	ep, err := ParseEndpoint("tcp://host.docker.internal:27182")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ep.IsTCP() || ep.IsUnix() {
		t.Fatalf("ep = %+v, want IsTCP true, IsUnix false", ep)
	}
	if got := ep.Host(); got != "host.docker.internal" {
		t.Fatalf("Host() = %q, want %q", got, "host.docker.internal")
	}
	if got := ep.Port(); got != "27182" {
		t.Fatalf("Port() = %q, want %q", got, "27182")
	}
}

// TestEndpoint_String verifies String() is ParseEndpoint's exact inverse
// for both schemes -- Encode relies on this round trip to put the endpoint
// back into the manifest's JSON "endpoint" string field.
func TestEndpoint_String(t *testing.T) {
	cases := []string{
		"unix:///registry-proxy.sock",
		"tcp://host.docker.internal:27182",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			ep, err := ParseEndpoint(raw)
			if err != nil {
				t.Fatalf("ParseEndpoint(%q): %v", raw, err)
			}
			if got := ep.String(); got != raw {
				t.Fatalf("String() = %q, want %q", got, raw)
			}
		})
	}
}

// TestEncodeParse_RoundTrip verifies Encode/Parse are inverse over a
// manifest shaped like ADR 0045's own example, and that Encode's JSON field
// names match the wire contract the Box-side parser (a later slice) keys
// on: "endpoint", "prefix", "upstreamHost", "cargoRegistries".
func TestEncodeParse_RoundTrip(t *testing.T) {
	want := Manifest{
		Endpoint: NewUnixEndpoint("/registry-proxy.sock"),
		Routes: []Route{
			{
				Prefix:          "r0",
				UpstreamHost:    "artifactory.example.com",
				CargoRegistries: []string{"example-remote"},
				EnforcedPaths:   []registryvocab.Subtree{{Ecosystem: "npm", Path: "/npm"}},
			},
		},
	}

	encoded, err := Encode(want)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	for _, field := range []string{`"endpoint"`, `"prefix"`, `"upstreamHost"`, `"cargoRegistries"`, `"enforcedPaths"`, `"ecosystem"`} {
		if !strings.Contains(encoded, field) {
			t.Fatalf("Encode() = %s, missing field %s", encoded, field)
		}
	}

	got, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Endpoint.String() != want.Endpoint.String() {
		t.Fatalf("Parse().Endpoint = %v, want %v", got.Endpoint, want.Endpoint)
	}
	if len(got.Routes) != 1 || got.Routes[0].Prefix != "r0" ||
		got.Routes[0].UpstreamHost != "artifactory.example.com" ||
		len(got.Routes[0].CargoRegistries) != 1 || got.Routes[0].CargoRegistries[0] != "example-remote" {
		t.Fatalf("Parse().Routes = %+v, want route r0/artifactory.example.com/[example-remote]", got.Routes)
	}
	if len(got.Routes[0].EnforcedPaths) != 1 || got.Routes[0].EnforcedPaths[0] != (registryvocab.Subtree{Ecosystem: "npm", Path: "/npm"}) {
		t.Fatalf("Parse().Routes[0].EnforcedPaths = %+v, want [{npm /npm}]", got.Routes[0].EnforcedPaths)
	}
}

// TestEncode_ExactJSON pins the manifest's wire bytes exactly (issue #3398)
// -- not just a round trip, which would still pass if Encode and Parse
// drifted together -- so a future change to the shared registryvocab.Subtree
// type can't silently add or rename a JSON field. The second route's
// RegistryName pins that its json:"-" tag really keeps it off the wire.
func TestEncode_ExactJSON(t *testing.T) {
	m := Manifest{
		Endpoint: NewUnixEndpoint("/registry-proxy.sock"),
		Routes: []Route{
			{
				Prefix:          "r0",
				UpstreamHost:    "artifactory.example.com",
				CargoRegistries: []string{"example-remote"},
				EnforcedPaths:   []registryvocab.Subtree{{Ecosystem: "npm", Path: "/npm"}},
			},
			{
				Prefix:       "r1",
				UpstreamHost: "artifactory.example.com",
				EnforcedPaths: []registryvocab.Subtree{
					{Ecosystem: "cargo", Path: "/index", RegistryName: "mycorp"},
				},
			},
		},
	}
	want := `{"endpoint":"unix:///registry-proxy.sock","routes":[{"prefix":"r0","upstreamHost":"artifactory.example.com","cargoRegistries":["example-remote"],"enforcedPaths":[{"ecosystem":"npm","path":"/npm"}]},{"prefix":"r1","upstreamHost":"artifactory.example.com","enforcedPaths":[{"ecosystem":"cargo","path":"/index"}]}]}`
	got, err := Encode(m)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if got != want {
		t.Fatalf("Encode() = %s, want %s", got, want)
	}
}

// TestParse_Absent verifies Parse distinguishes an empty/unset env var --
// the documented "no manifest" case, where the verb (a later slice) must
// stay silent -- from a manifest present but malformed, which must warn.
func TestParse_Absent(t *testing.T) {
	_, err := Parse("")
	if !errors.Is(err, ErrAbsent) {
		t.Fatalf("Parse(\"\"): err = %v, want ErrAbsent", err)
	}
}

// TestParse_BadJSON verifies malformed JSON is a distinct, non-ErrAbsent
// error -- the "manifest present but bad" case the verb must warn on.
func TestParse_BadJSON(t *testing.T) {
	_, err := Parse("{not json")
	if err == nil {
		t.Fatalf("Parse: got nil error, want error")
	}
	if errors.Is(err, ErrAbsent) {
		t.Fatalf("Parse(bad JSON): err wraps ErrAbsent, want distinct error")
	}
}

// TestParse_BadEndpoint verifies a structurally valid manifest with an
// unusable endpoint surfaces the underlying *EndpointError through Parse,
// so the verb can warn naming the endpoint (ADR 0045) rather than a bare
// JSON error.
func TestParse_BadEndpoint(t *testing.T) {
	const doc = `{"endpoint":"ftp://nope","routes":[]}`
	_, err := Parse(doc)
	if err == nil {
		t.Fatalf("Parse: got nil error, want error")
	}
	if errors.Is(err, ErrAbsent) {
		t.Fatalf("Parse(bad endpoint): err wraps ErrAbsent, want distinct error")
	}
	var epErr *EndpointError
	if !errors.As(err, &epErr) {
		t.Fatalf("Parse(bad endpoint): err = %v, want to wrap *EndpointError", err)
	}
}

// TestParse_RejectsInvalidPrefixCharset covers the defense-in-depth guard
// (issue #3142's reviewer finding): the launcher only ever mints a Prefix
// from [a-z0-9-] (registryproxy.isValidPrefix), but Parse itself must still
// reject a manifest naming a Prefix outside that charset before it ever
// reaches the Box's shell-sourced GOPROXY export or the Groovy gradle init
// script's double-quoted GString, either of which would otherwise
// interpolate an attacker-controlled character verbatim.
func TestParse_RejectsInvalidPrefixCharset(t *testing.T) {
	const doc = `{"endpoint":"unix:///registry-proxy.sock","routes":[{"prefix":"r0$(id)","upstreamHost":"upstream.example"}]}`
	_, err := Parse(doc)
	if err == nil {
		t.Fatalf("Parse: got nil error, want error")
	}
	if errors.Is(err, ErrAbsent) {
		t.Fatalf("Parse(invalid prefix charset): err wraps ErrAbsent, want distinct error")
	}
	if !strings.Contains(err.Error(), "r0$(id)") {
		t.Fatalf("Parse(invalid prefix charset): error %q does not name the offending prefix", err.Error())
	}
}

// TestParse_AllowsEmptyPrefix verifies Parse's new charset guard doesn't
// regress the existing no-prefix handling other callers depend on (e.g.
// runBindRegistryBindings' own empty-prefix warn-and-skip) -- an empty
// Prefix must still parse successfully.
func TestParse_AllowsEmptyPrefix(t *testing.T) {
	const doc = `{"endpoint":"unix:///registry-proxy.sock","routes":[{"prefix":"","upstreamHost":"upstream.example"}]}`
	m, err := Parse(doc)
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if len(m.Routes) != 1 || m.Routes[0].Prefix != "" {
		t.Fatalf("Parse().Routes = %+v, want one route with empty Prefix", m.Routes)
	}
}

// TestParseEndpoint_Rejects covers every malformed input ParseEndpoint must
// reject: empty, unknown scheme, unix with an empty path, tcp with a
// missing host or port, and junk with no scheme separator at all. Each
// must return an *EndpointError naming the offending raw string, so a
// caller (the bind-registry verb, later) can warn with the endpoint
// identified rather than a bare "invalid" message.
func TestParseEndpoint_Rejects(t *testing.T) {
	cases := []string{
		"",
		"ftp:///registry-proxy.sock",
		"unix://",
		"tcp://host.docker.internal",
		"tcp://:27182",
		"not-an-endpoint-at-all",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			_, err := ParseEndpoint(raw)
			if err == nil {
				t.Fatalf("ParseEndpoint(%q): got nil error, want error", raw)
			}
			var epErr *EndpointError
			if !errors.As(err, &epErr) {
				t.Fatalf("ParseEndpoint(%q): err = %v, want *EndpointError", raw, err)
			}
			if !strings.Contains(err.Error(), raw) {
				t.Fatalf("ParseEndpoint(%q): error %q does not name the raw endpoint", raw, err.Error())
			}
		})
	}
}
