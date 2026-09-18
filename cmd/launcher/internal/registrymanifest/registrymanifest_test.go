package registrymanifest

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/registryvocab"
)

// The launcher and the bind-registry verb must agree on this name
// (ADR 0045). Go gives no compile-time link between an os.Setenv call and
// a Getenv one, so a typo silently splits the two sides of the handoff.
func TestEnvVar(t *testing.T) {
	const want = "REGISTRY_PROXY_MANIFEST"
	if EnvVar != want {
		t.Fatalf("EnvVar = %q, want %q", EnvVar, want)
	}
}

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

// Encode relies on String() being ParseEndpoint's exact inverse to put the
// endpoint back into the manifest's JSON "endpoint" field.
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

// The manifest is shaped like ADR 0045's own example. The field-name
// assertions pin the wire contract the Box-side parser keys on.
func TestEncodeParse_RoundTrip(t *testing.T) {
	want := Manifest{
		Endpoint: NewUnixEndpoint("/registry-proxy.sock"),
		Routes: []Route{
			{
				Prefix:        "r0",
				UpstreamHost:  "artifactory.example.com",
				EnforcedPaths: []registryvocab.Subtree{{Ecosystem: "npm", Path: "/npm"}},
				Ecosystems:    registryvocab.RouteEcosystems{"npm": registryvocab.RouteDeclaration{"path": "/npm"}},
			},
		},
	}

	encoded, err := Encode(want)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	for _, field := range []string{`"endpoint"`, `"prefix"`, `"upstreamHost"`, `"enforcedPaths"`, `"ecosystem"`, `"ecosystems"`} {
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
		got.Routes[0].UpstreamHost != "artifactory.example.com" {
		t.Fatalf("Parse().Routes = %+v, want route r0/artifactory.example.com", got.Routes)
	}
	if len(got.Routes[0].EnforcedPaths) != 1 || got.Routes[0].EnforcedPaths[0] != (registryvocab.Subtree{Ecosystem: "npm", Path: "/npm"}) {
		t.Fatalf("Parse().Routes[0].EnforcedPaths = %+v, want [{npm /npm}]", got.Routes[0].EnforcedPaths)
	}
}

// Route.Ecosystems (issue #3403) must read back identical after a JSON
// marshal/unmarshal, not merely be present, because
// registryvocab.RouteEcosystems holds TOML values decoded into []any. The
// second route declares no Ecosystems block to pin the other half of the
// contract: omitted, never emitted empty.
func TestEncodeParse_RoundTrip_Ecosystems(t *testing.T) {
	want := Manifest{
		Endpoint: NewUnixEndpoint("/registry-proxy.sock"),
		Routes: []Route{
			{
				Prefix:       "r0",
				UpstreamHost: "artifactory.example.com",
				Ecosystems: registryvocab.RouteEcosystems{
					"gradle": registryvocab.RouteDeclaration{"path": "/maven"},
					"cargo":  registryvocab.RouteDeclaration{"registries": registryvocab.StringsValue([]string{"example-remote"})},
				},
			},
			{
				Prefix:       "r1",
				UpstreamHost: "artifactory.example.com",
			},
		},
	}

	encoded, err := Encode(want)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	var raw []map[string]any
	if err := json.Unmarshal([]byte(encoded), &struct {
		Routes *[]map[string]any `json:"routes"`
	}{Routes: &raw}); err != nil {
		t.Fatalf("Unmarshal(encoded routes): %v", err)
	}
	if _, ok := raw[0]["ecosystems"]; !ok {
		t.Fatalf("Encode() routes[0] = %s, want an \"ecosystems\" key", encoded)
	}
	if _, ok := raw[1]["ecosystems"]; ok {
		t.Fatalf("Encode() routes[1] = %s, want no \"ecosystems\" key (route declares nothing per-ecosystem)", encoded)
	}

	got, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got.Routes) != 2 {
		t.Fatalf("Parse().Routes = %+v, want 2 routes", got.Routes)
	}
	if gotPath := got.Routes[0].Ecosystems.Path("gradle"); gotPath != "/maven" {
		t.Errorf("Parse().Routes[0].Ecosystems.Path(\"gradle\") = %q, want %q", gotPath, "/maven")
	}
	wantRegistries := []string{"example-remote"}
	if gotRegistries := got.Routes[0].Ecosystems.Strings("cargo", "registries"); !reflect.DeepEqual(gotRegistries, wantRegistries) {
		t.Errorf("Parse().Routes[0].Ecosystems.Strings(\"cargo\", \"registries\") = %v, want %v", gotRegistries, wantRegistries)
	}
	if got.Routes[1].Ecosystems != nil {
		t.Errorf("Parse().Routes[1].Ecosystems = %v, want nil", got.Routes[1].Ecosystems)
	}
}

// Pinning the exact wire bytes (issue #3398) catches what a round trip
// cannot: Encode and Parse drifting together, or registryvocab.Subtree
// adding or renaming a JSON field. The second route's RegistryName pins
// that its json:"-" tag keeps it off the wire; the first route's
// Ecosystems block pins the per-ecosystem wire shape (issue #3404).
func TestEncode_ExactJSON(t *testing.T) {
	m := Manifest{
		Endpoint: NewUnixEndpoint("/registry-proxy.sock"),
		Routes: []Route{
			{
				Prefix:        "r0",
				UpstreamHost:  "artifactory.example.com",
				EnforcedPaths: []registryvocab.Subtree{{Ecosystem: "npm", Path: "/npm"}},
				Ecosystems: registryvocab.RouteEcosystems{
					"cargo": registryvocab.RouteDeclaration{"registries": registryvocab.StringsValue([]string{"example-remote"})},
				},
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
	want := `{"endpoint":"unix:///registry-proxy.sock","routes":[{"prefix":"r0","upstreamHost":"artifactory.example.com","enforcedPaths":[{"ecosystem":"npm","path":"/npm"}],"ecosystems":{"cargo":{"registries":["example-remote"]}}},{"prefix":"r1","upstreamHost":"artifactory.example.com","enforcedPaths":[{"ecosystem":"cargo","path":"/index"}]}]}`
	got, err := Encode(m)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if got != want {
		t.Fatalf("Encode() = %s, want %s", got, want)
	}
}

// An empty or unset env var is the documented "no manifest" case, where the
// verb stays silent. A present but malformed manifest must warn instead.
func TestParse_Absent(t *testing.T) {
	_, err := Parse("")
	if !errors.Is(err, ErrAbsent) {
		t.Fatalf("Parse(\"\"): err = %v, want ErrAbsent", err)
	}
}

// Malformed JSON must be a distinct, non-ErrAbsent error: the "manifest
// present but bad" case the verb warns on.
func TestParse_BadJSON(t *testing.T) {
	_, err := Parse("{not json")
	if err == nil {
		t.Fatalf("Parse: got nil error, want error")
	}
	if errors.Is(err, ErrAbsent) {
		t.Fatalf("Parse(bad JSON): err wraps ErrAbsent, want distinct error")
	}
}

// Parse must pass the underlying *EndpointError through, so the verb can
// warn naming the endpoint (ADR 0045) rather than reporting a bare JSON
// error.
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

// Defense in depth for issue #3142's reviewer finding. The launcher only
// mints a Prefix from [a-z0-9-] (registryproxy.isValidPrefix), but Parse
// must reject anything else on its own: the Box's shell-sourced GOPROXY
// export and the Groovy gradle init script's double-quoted GString would
// each interpolate an attacker-controlled character verbatim.
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

// The charset guard must not regress the no-prefix handling other callers
// depend on, such as runBindRegistryBindings' empty-prefix warn-and-skip.
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

// Each rejection must return an *EndpointError naming the offending raw
// string, so the bind-registry verb can warn with the endpoint identified
// rather than a bare "invalid" message.
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
