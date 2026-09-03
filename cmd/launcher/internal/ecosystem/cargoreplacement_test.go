package ecosystem

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/registrymanifest"
)

func TestParseCargoRegistryDecls_SingleRegistry(t *testing.T) {
	content := `[registries.othercorp]
index = "sparse+https://cargo.example.test/index/"
`
	want := []CargoRegistryDecl{{Name: "othercorp", Index: "sparse+https://cargo.example.test/index/"}}

	got := ParseCargoRegistryDecls(content)

	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("ParseCargoRegistryDecls() = %+v, want %+v", got, want)
	}
}

// TestParseCargoRegistryDecls_NoIndexLine covers a [registries.NAME] table
// with no `index` assignment at all -- it must yield no decl, not a decl
// with an empty Index.
func TestParseCargoRegistryDecls_NoIndexLine(t *testing.T) {
	content := `[registries.othercorp]
token = "irrelevant"
`
	got := ParseCargoRegistryDecls(content)
	if len(got) != 0 {
		t.Errorf("ParseCargoRegistryDecls() = %+v, want empty", got)
	}
}

// TestParseCargoRegistryDecls_HostileNameSkipped covers the untrusted-name
// guard: a quoted TOML key carrying shell metacharacters must never reach a
// caller that could turn it into a shell-sourced env var name.
func TestParseCargoRegistryDecls_HostileNameSkipped(t *testing.T) {
	content := `[registries."evil; rm -rf /"]
index = "sparse+https://cargo.example.test/index/"
`
	got := ParseCargoRegistryDecls(content)
	if len(got) != 0 {
		t.Errorf("ParseCargoRegistryDecls() = %+v, want empty (hostile name must be skipped)", got)
	}
}

// TestParseCargoRegistryDecls_TrailingComment covers a legal-TOML trailing
// comment on the index line. A naive quote-trim keeps the comment inside the
// value, which still host-matches its route and renders verbatim into the
// [source....] registry stanza -- cargo then sees a URL that matches nothing
// and the replacement silently never binds.
func TestParseCargoRegistryDecls_TrailingComment(t *testing.T) {
	content := `[registries.othercorp]
index = "sparse+https://cargo.example.test/index/" # note
`
	want := CargoRegistryDecl{Name: "othercorp", Index: "sparse+https://cargo.example.test/index/"}

	got := ParseCargoRegistryDecls(content)

	if len(got) != 1 || got[0] != want {
		t.Errorf("ParseCargoRegistryDecls() = %+v, want %+v", got, []CargoRegistryDecl{want})
	}
}

// TestParseCargoRegistryDecls_LiteralString covers TOML's other string form:
// a single-quoted literal string is as legal as a basic one.
func TestParseCargoRegistryDecls_LiteralString(t *testing.T) {
	content := `[registries.othercorp]
index = 'sparse+https://cargo.example.test/index/'
`
	want := CargoRegistryDecl{Name: "othercorp", Index: "sparse+https://cargo.example.test/index/"}

	got := ParseCargoRegistryDecls(content)

	if len(got) != 1 || got[0] != want {
		t.Errorf("ParseCargoRegistryDecls() = %+v, want %+v", got, []CargoRegistryDecl{want})
	}
}

// TestParseCargoRegistryDecls_MalformedIndexRejected covers every shape of
// index value that is not a well-formed TOML string. Each must yield no decl
// at all -- and, since the first index line in a section wins, a malformed
// one must not fall through to a later, well-formed one.
func TestParseCargoRegistryDecls_MalformedIndexRejected(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"unquoted", `sparse+https://cargo.example.test/index/`},
		{"unterminated", `"sparse+https://cargo.example.test/index/`},
		{"trailing junk", `"sparse+https://cargo.example.test/index/" oops`},
		{"mismatched quotes", `"sparse+https://cargo.example.test/index/'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := "[registries.othercorp]\nindex = " + tc.value + "\n" +
				`index = "sparse+https://cargo.example.test/index/"` + "\n"

			got := ParseCargoRegistryDecls(content)
			if len(got) != 0 {
				t.Errorf("ParseCargoRegistryDecls() = %+v, want empty", got)
			}
		})
	}
}

func TestCargoSourceReplacements_SingleRegistry(t *testing.T) {
	const port = 27182
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "crates.io"},
		{Prefix: "r1", UpstreamHost: "cargo.example.test"},
	}
	repoConfig := `[registries.othercorp]
index = "sparse+https://cargo.example.test/index/"
`

	got, warnings := CargoSourceReplacements(port, "r0", routes, repoConfig)

	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	if len(got) != 1 {
		t.Fatalf("CargoSourceReplacements() = %+v, want exactly one replacement", got)
	}
	rep := got[0]
	if rep.Prefix != "r1" {
		t.Errorf("Prefix = %q, want %q", rep.Prefix, "r1")
	}
	if rep.ProxySource != "spindrift-registry-proxy-r1" {
		t.Errorf("ProxySource = %q, want %q", rep.ProxySource, "spindrift-registry-proxy-r1")
	}
	if rep.LocalIndexURL != "sparse+http://127.0.0.1:27182/r1/" {
		t.Errorf("LocalIndexURL = %q, want %q", rep.LocalIndexURL, "sparse+http://127.0.0.1:27182/r1/")
	}
	if len(rep.Upstreams) != 1 {
		t.Fatalf("Upstreams = %+v, want exactly one", rep.Upstreams)
	}
	up := rep.Upstreams[0]
	if up.SourceName != "spindrift-upstream-othercorp" {
		t.Errorf("SourceName = %q, want %q", up.SourceName, "spindrift-upstream-othercorp")
	}
	if up.IndexURL != "sparse+https://cargo.example.test/index/" {
		t.Errorf("IndexURL = %q, want %q", up.IndexURL, "sparse+https://cargo.example.test/index/")
	}
}

// TestCargoSourceReplacements_DeclaredListRestrictsStanzas covers a route
// that declares a non-empty CargoRegistries list: only names in that list
// get stanzas, even though another host-matching decl exists in repoConfig.
func TestCargoSourceReplacements_DeclaredListRestrictsStanzas(t *testing.T) {
	const port = 27182
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "crates.io"},
		{Prefix: "r1", UpstreamHost: "cargo.example.test", CargoRegistries: []string{"declared-registry"}},
	}
	repoConfig := `[registries.declared-registry]
index = "sparse+https://cargo.example.test/declared-index/"

[registries.undeclared-registry]
index = "sparse+https://cargo.example.test/undeclared-index/"
`

	got, warnings := CargoSourceReplacements(port, "r0", routes, repoConfig)

	if len(got) != 1 || len(got[0].Upstreams) != 1 || got[0].Upstreams[0].SourceName != "spindrift-upstream-declared-registry" {
		t.Fatalf("CargoSourceReplacements() = %+v, want exactly the declared-registry upstream", got)
	}

	want := `==> WARNING: cargo registry "undeclared-registry"`
	found := false
	for _, w := range warnings {
		if strings.HasPrefix(w, want) && strings.Contains(w, `"r1"`) {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want one naming undeclared-registry and route prefix r1", warnings)
	}
}

// TestCargoSourceReplacements_DeclaredNameOnWrongHostWarns covers the
// drift case the retired ApplyNoopContent diagnostic used to catch: the
// route declares a registry, the repo config declares it too, but its index
// points at some other host, so nothing binds. Silence here would fail a
// network-less cargo build with no signal at all.
func TestCargoSourceReplacements_DeclaredNameOnWrongHostWarns(t *testing.T) {
	const port = 27182
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "crates.io"},
		{Prefix: "r1", UpstreamHost: "cargo.example.test", CargoRegistries: []string{"acme"}},
	}
	repoConfig := `[registries.acme]
index = "sparse+https://moved.example.test/index/"
`

	got, warnings := CargoSourceReplacements(port, "r0", routes, repoConfig)

	if len(got) != 0 {
		t.Errorf("CargoSourceReplacements() = %+v, want no replacements", got)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one", warnings)
	}
	w := warnings[0]
	if !strings.HasPrefix(w, "==> WARNING: ") || !strings.Contains(w, `"acme"`) || !strings.Contains(w, `"r1"`) {
		t.Errorf("warning = %q, want a ==> WARNING: line naming acme and route prefix r1", w)
	}
}

// TestCargoSourceReplacements_DeclaredNameAbsentWarns covers a manifest
// naming a registry the repo config never declares at all.
func TestCargoSourceReplacements_DeclaredNameAbsentWarns(t *testing.T) {
	const port = 27182
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "crates.io"},
		{Prefix: "r1", UpstreamHost: "cargo.example.test", CargoRegistries: []string{"present", "absent"}},
	}
	repoConfig := `[registries.present]
index = "sparse+https://cargo.example.test/index/"
`

	got, warnings := CargoSourceReplacements(port, "r0", routes, repoConfig)

	if len(got) != 1 || len(got[0].Upstreams) != 1 || got[0].Upstreams[0].SourceName != "spindrift-upstream-present" {
		t.Fatalf("CargoSourceReplacements() = %+v, want exactly the present upstream", got)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one", warnings)
	}
	if !strings.Contains(warnings[0], `"absent"`) || !strings.Contains(warnings[0], `"r1"`) {
		t.Errorf("warning = %q, want it to name absent and route prefix r1", warnings[0])
	}
}

// TestCargoSourceReplacements_DeclaredNameMalformedIndexWarns covers a
// declared registry whose index value the TOML value parser rejects: the
// decl never reaches the host match, so only the declared-name sweep can
// report it.
func TestCargoSourceReplacements_DeclaredNameMalformedIndexWarns(t *testing.T) {
	const port = 27182
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "crates.io"},
		{Prefix: "r1", UpstreamHost: "cargo.example.test", CargoRegistries: []string{"acme"}},
	}
	repoConfig := `[registries.acme]
index = "sparse+https://cargo.example.test/index/" garbage
`

	got, warnings := CargoSourceReplacements(port, "r0", routes, repoConfig)

	if len(got) != 0 {
		t.Errorf("CargoSourceReplacements() = %+v, want no replacements", got)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one", warnings)
	}
	if !strings.Contains(warnings[0], `"acme"`) {
		t.Errorf("warning = %q, want it to name acme", warnings[0])
	}
}

// TestCargoSourceReplacements_DeclaredNameDedupedIsNotAMiss covers two
// declared names sharing one real index URL: the second collapses into the
// first's stanza and genuinely binds through it, so it is not unsatisfied
// and must not warn.
func TestCargoSourceReplacements_DeclaredNameDedupedIsNotAMiss(t *testing.T) {
	const port = 27182
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "crates.io"},
		{Prefix: "r1", UpstreamHost: "cargo.example.test", CargoRegistries: []string{"first-name", "second-name"}},
	}
	repoConfig := `[registries.first-name]
index = "sparse+https://cargo.example.test/index/"

[registries.second-name]
index = "sparse+https://cargo.example.test/index/"
`

	got, warnings := CargoSourceReplacements(port, "r0", routes, repoConfig)

	if len(got) != 1 || len(got[0].Upstreams) != 1 {
		t.Fatalf("CargoSourceReplacements() = %+v, want exactly one deduped upstream", got)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none (a deduped name still binds)", warnings)
	}
}

// TestCargoSourceReplacements_EmptyDeclaredListNeverWarns covers a route
// declaring no cargo registries at all: it declares nothing, so nothing can
// go unsatisfied, even when the repo config matches none of its host.
func TestCargoSourceReplacements_EmptyDeclaredListNeverWarns(t *testing.T) {
	const port = 27182
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "crates.io"},
		{Prefix: "r1", UpstreamHost: "cargo.example.test"},
	}
	repoConfig := `[registries.acme]
index = "sparse+https://moved.example.test/index/"
`

	got, warnings := CargoSourceReplacements(port, "r0", routes, repoConfig)

	if len(got) != 0 {
		t.Errorf("CargoSourceReplacements() = %+v, want no replacements", got)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
}

// TestCargoSourceReplacements_DedupeSameIndexURL covers two registry names
// that both point at the same real index URL: cargo maps URL -> source name
// 1:1, so this must collapse into one upstream stanza, keeping the first
// name's stanza (repo-config appearance order).
func TestCargoSourceReplacements_DedupeSameIndexURL(t *testing.T) {
	const port = 27182
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "crates.io"},
		{Prefix: "r1", UpstreamHost: "cargo.example.test"},
	}
	repoConfig := `[registries.first-name]
index = "sparse+https://cargo.example.test/index/"

[registries.second-name]
index = "sparse+https://cargo.example.test/index/"
`

	got, _ := CargoSourceReplacements(port, "r0", routes, repoConfig)

	if len(got) != 1 || len(got[0].Upstreams) != 1 {
		t.Fatalf("CargoSourceReplacements() = %+v, want exactly one deduped upstream", got)
	}
	if got[0].Upstreams[0].SourceName != "spindrift-upstream-first-name" {
		t.Errorf("SourceName = %q, want first occurrence %q", got[0].Upstreams[0].SourceName, "spindrift-upstream-first-name")
	}
}

// TestCargoSourceReplacements_HostileNameSkipped covers a registry name
// that fails cargoBareKeyPattern: it must never produce an Upstream, since
// it would otherwise flow into a rendered TOML table name / shell-sourced
// env var name.
func TestCargoSourceReplacements_HostileNameSkipped(t *testing.T) {
	const port = 27182
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "crates.io"},
		{Prefix: "r1", UpstreamHost: "cargo.example.test"},
	}
	repoConfig := `[registries."evil; rm -rf /"]
index = "sparse+https://cargo.example.test/index/"
`

	got, warnings := CargoSourceReplacements(port, "r0", routes, repoConfig)

	if len(got) != 0 {
		t.Errorf("CargoSourceReplacements() = %+v, want no replacements (hostile name skipped)", got)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
}

// TestCargoSourceReplacements_ProxySourceReuse covers the case a named-
// registry route's own prefix equals routes[0]'s (the crates-io
// replacement's own prefix): its local index URL then coincides with the
// crates-io replacement's, and cargo's URL -> source name 1:1 mapping means
// this must reuse spindrift-registry-proxy rather than mint a second
// [source.…] stanza carrying the same URL.
func TestCargoSourceReplacements_ProxySourceReuse(t *testing.T) {
	const port = 27182
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "cargo.example.test"},
	}
	repoConfig := `[registries.othercorp]
index = "sparse+https://cargo.example.test/index/"
`

	got, _ := CargoSourceReplacements(port, "r0", routes, repoConfig)

	if len(got) != 1 {
		t.Fatalf("CargoSourceReplacements() = %+v, want exactly one replacement", got)
	}
	if got[0].ProxySource != "spindrift-registry-proxy" {
		t.Errorf("ProxySource = %q, want reused %q", got[0].ProxySource, "spindrift-registry-proxy")
	}
	if got[0].LocalIndexURL != "sparse+http://127.0.0.1:27182/r0/" {
		t.Errorf("LocalIndexURL = %q, want %q", got[0].LocalIndexURL, "sparse+http://127.0.0.1:27182/r0/")
	}
}

// TestCargoSourceReplacements_TwoRoutes covers a manifest with two routes
// each naming registries under distinct upstream hosts: both must produce
// their own replacement, in manifest route order.
func TestCargoSourceReplacements_TwoRoutes(t *testing.T) {
	const port = 27182
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "crates.io"},
		{Prefix: "r1", UpstreamHost: "corp-a.example.test"},
		{Prefix: "r2", UpstreamHost: "corp-b.example.test"},
	}
	repoConfig := `[registries.corp-a]
index = "sparse+https://corp-a.example.test/index/"

[registries.corp-b]
index = "sparse+https://corp-b.example.test/index/"
`

	got, warnings := CargoSourceReplacements(port, "r0", routes, repoConfig)

	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	if len(got) != 2 {
		t.Fatalf("CargoSourceReplacements() = %+v, want two replacements", got)
	}
	if got[0].Prefix != "r1" || got[1].Prefix != "r2" {
		t.Errorf("route order = [%q, %q], want [r1, r2] (manifest order)", got[0].Prefix, got[1].Prefix)
	}
}

// TestCargoSourceReplacements_PortedUpstreamHostMatches covers an upstream
// URL with an explicit port: registrymanifest.Route.UpstreamHost is minted
// as u.Host (box.go), which keeps a port when the upstream URL has one, so
// the decl's parsed host must too or a ported upstream never matches its
// route.
func TestCargoSourceReplacements_PortedUpstreamHostMatches(t *testing.T) {
	const port = 27182
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "crates.io"},
		{Prefix: "r1", UpstreamHost: "cargo.example.test:8443"},
	}
	repoConfig := `[registries.othercorp]
index = "sparse+https://cargo.example.test:8443/index/"
`

	got, warnings := CargoSourceReplacements(port, "r0", routes, repoConfig)

	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	if len(got) != 1 {
		t.Fatalf("CargoSourceReplacements() = %+v, want exactly one replacement", got)
	}
	if got[0].Prefix != "r1" {
		t.Errorf("Prefix = %q, want %q", got[0].Prefix, "r1")
	}
}

// TestCargoConfigTOMLWithReplacements_EmptyPlanPassthrough covers the no-op
// case: an empty replacements slice must return CargoConfigTOML's own
// output byte-for-byte, since that's the pre-#3201 render every existing
// caller/test still pins.
func TestCargoConfigTOMLWithReplacements_EmptyPlanPassthrough(t *testing.T) {
	got := CargoConfigTOMLWithReplacements(27182, "r0", nil)
	want := CargoConfigTOML(27182, "r0")
	if got != want {
		t.Errorf("CargoConfigTOMLWithReplacements() = %q, want %q (byte-identical to CargoConfigTOML)", got, want)
	}
}

// TestCargoConfigTOMLWithReplacements_OneNamedRegistry byte-pins the
// rendered content for one named-registry route minting its own proxy
// source (route.Prefix != the crates-io prefix).
func TestCargoConfigTOMLWithReplacements_OneNamedRegistry(t *testing.T) {
	replacements := []CargoSourceReplacement{
		{
			Prefix:        "r1",
			ProxySource:   "spindrift-registry-proxy-r1",
			LocalIndexURL: "sparse+http://127.0.0.1:27182/r1/",
			Upstreams: []CargoUpstreamSource{
				{SourceName: "spindrift-upstream-othercorp", IndexURL: "sparse+https://cargo.example.test/index/"},
			},
		},
	}

	got := CargoConfigTOMLWithReplacements(27182, "r0", replacements)

	want := `[source.crates-io]
replace-with = "spindrift-registry-proxy"

[source.spindrift-registry-proxy]
registry = "sparse+http://127.0.0.1:27182/r0/"

[registry]
global-credential-providers = ["cargo:token"]

[source.spindrift-upstream-othercorp]
registry = "sparse+https://cargo.example.test/index/"
replace-with = "spindrift-registry-proxy-r1"

[source.spindrift-registry-proxy-r1]
registry = "sparse+http://127.0.0.1:27182/r1/"

[registries.spindrift-registry-proxy-r1]
index = "sparse+http://127.0.0.1:27182/r1/"
`
	if got != want {
		t.Errorf("CargoConfigTOMLWithReplacements() = %q, want %q", got, want)
	}
}

// TestCargoConfigTOMLWithReplacements_ReusedProxySource covers the
// spindrift-registry-proxy reuse case: its [source....] stanza is already in
// CargoConfigTOML's base render, so the replacement block must emit only
// its [registries....] entry, never a second [source.spindrift-registry-proxy].
func TestCargoConfigTOMLWithReplacements_ReusedProxySource(t *testing.T) {
	replacements := []CargoSourceReplacement{
		{
			Prefix:        "r0",
			ProxySource:   "spindrift-registry-proxy",
			LocalIndexURL: "sparse+http://127.0.0.1:27182/r0/",
			Upstreams: []CargoUpstreamSource{
				{SourceName: "spindrift-upstream-othercorp", IndexURL: "sparse+https://cargo.example.test/index/"},
			},
		},
	}

	got := CargoConfigTOMLWithReplacements(27182, "r0", replacements)

	want := `[source.crates-io]
replace-with = "spindrift-registry-proxy"

[source.spindrift-registry-proxy]
registry = "sparse+http://127.0.0.1:27182/r0/"

[registry]
global-credential-providers = ["cargo:token"]

[source.spindrift-upstream-othercorp]
registry = "sparse+https://cargo.example.test/index/"
replace-with = "spindrift-registry-proxy"

[registries.spindrift-registry-proxy]
index = "sparse+http://127.0.0.1:27182/r0/"
`
	if got != want {
		t.Errorf("CargoConfigTOMLWithReplacements() = %q, want %q", got, want)
	}
	if strings.Count(got, "[source.spindrift-registry-proxy]") != 1 {
		t.Errorf("CargoConfigTOMLWithReplacements() contains %d [source.spindrift-registry-proxy] stanzas, want exactly 1", strings.Count(got, "[source.spindrift-registry-proxy]"))
	}
}

// TestCargoConfigTOMLWithReplacements_TwoUpstreamsOneProxySource covers one
// route with two Upstreams: each gets its own [source.spindrift-upstream-…]
// stanza, but the [source.…]/[registries.…] proxy pair is emitted once,
// after both upstreams.
func TestCargoConfigTOMLWithReplacements_TwoUpstreamsOneProxySource(t *testing.T) {
	replacements := []CargoSourceReplacement{
		{
			Prefix:        "r1",
			ProxySource:   "spindrift-registry-proxy-r1",
			LocalIndexURL: "sparse+http://127.0.0.1:27182/r1/",
			Upstreams: []CargoUpstreamSource{
				{SourceName: "spindrift-upstream-corp-a", IndexURL: "sparse+https://corp-a.example.test/index/"},
				{SourceName: "spindrift-upstream-corp-b", IndexURL: "sparse+https://corp-b.example.test/index/"},
			},
		},
	}

	got := CargoConfigTOMLWithReplacements(27182, "r0", replacements)

	want := `[source.crates-io]
replace-with = "spindrift-registry-proxy"

[source.spindrift-registry-proxy]
registry = "sparse+http://127.0.0.1:27182/r0/"

[registry]
global-credential-providers = ["cargo:token"]

[source.spindrift-upstream-corp-a]
registry = "sparse+https://corp-a.example.test/index/"
replace-with = "spindrift-registry-proxy-r1"

[source.spindrift-upstream-corp-b]
registry = "sparse+https://corp-b.example.test/index/"
replace-with = "spindrift-registry-proxy-r1"

[source.spindrift-registry-proxy-r1]
registry = "sparse+http://127.0.0.1:27182/r1/"

[registries.spindrift-registry-proxy-r1]
index = "sparse+http://127.0.0.1:27182/r1/"
`
	if got != want {
		t.Errorf("CargoConfigTOMLWithReplacements() = %q, want %q", got, want)
	}
}

// TestCargoConfigTOMLWithReplacements_TwoRoutes covers two named-registry
// routes each minting their own proxy source: both blocks appear, in the
// replacements slice's order.
func TestCargoConfigTOMLWithReplacements_TwoRoutes(t *testing.T) {
	replacements := []CargoSourceReplacement{
		{
			Prefix:        "r1",
			ProxySource:   "spindrift-registry-proxy-r1",
			LocalIndexURL: "sparse+http://127.0.0.1:27182/r1/",
			Upstreams: []CargoUpstreamSource{
				{SourceName: "spindrift-upstream-corp-a", IndexURL: "sparse+https://corp-a.example.test/index/"},
			},
		},
		{
			Prefix:        "r2",
			ProxySource:   "spindrift-registry-proxy-r2",
			LocalIndexURL: "sparse+http://127.0.0.1:27182/r2/",
			Upstreams: []CargoUpstreamSource{
				{SourceName: "spindrift-upstream-corp-b", IndexURL: "sparse+https://corp-b.example.test/index/"},
			},
		},
	}

	got := CargoConfigTOMLWithReplacements(27182, "r0", replacements)

	want := `[source.crates-io]
replace-with = "spindrift-registry-proxy"

[source.spindrift-registry-proxy]
registry = "sparse+http://127.0.0.1:27182/r0/"

[registry]
global-credential-providers = ["cargo:token"]

[source.spindrift-upstream-corp-a]
registry = "sparse+https://corp-a.example.test/index/"
replace-with = "spindrift-registry-proxy-r1"

[source.spindrift-registry-proxy-r1]
registry = "sparse+http://127.0.0.1:27182/r1/"

[registries.spindrift-registry-proxy-r1]
index = "sparse+http://127.0.0.1:27182/r1/"

[source.spindrift-upstream-corp-b]
registry = "sparse+https://corp-b.example.test/index/"
replace-with = "spindrift-registry-proxy-r2"

[source.spindrift-registry-proxy-r2]
registry = "sparse+http://127.0.0.1:27182/r2/"

[registries.spindrift-registry-proxy-r2]
index = "sparse+http://127.0.0.1:27182/r2/"
`
	if got != want {
		t.Errorf("CargoConfigTOMLWithReplacements() = %q, want %q", got, want)
	}
}

func TestCargoReplacementPlaceholders(t *testing.T) {
	replacements := []CargoSourceReplacement{
		{Prefix: "r1", ProxySource: "spindrift-registry-proxy-r1"},
		{Prefix: "r2", ProxySource: "spindrift-registry-proxy-r2"},
	}

	got := CargoReplacementPlaceholders(replacements)

	if len(got) != 2 {
		t.Fatalf("CargoReplacementPlaceholders() = %+v, want two exports", got)
	}
	if value, ok := exportValue(got, "CARGO_REGISTRIES_SPINDRIFT_REGISTRY_PROXY_R1_TOKEN"); !ok || value != CargoPlaceholderToken {
		t.Errorf("exportValue(r1 token) = (%q, %v), want (%q, true)", value, ok, CargoPlaceholderToken)
	}
	if value, ok := exportValue(got, "CARGO_REGISTRIES_SPINDRIFT_REGISTRY_PROXY_R2_TOKEN"); !ok || value != CargoPlaceholderToken {
		t.Errorf("exportValue(r2 token) = (%q, %v), want (%q, true)", value, ok, CargoPlaceholderToken)
	}
}

// TestCargoReplacementPlaceholders_DedupesReusedProxySource covers two
// replacements sharing one ProxySource (the reuse case): the placeholder
// export must appear once, not twice.
func TestCargoReplacementPlaceholders_DedupesReusedProxySource(t *testing.T) {
	replacements := []CargoSourceReplacement{
		{Prefix: "r0", ProxySource: "spindrift-registry-proxy"},
		{Prefix: "r1", ProxySource: "spindrift-registry-proxy"},
	}

	got := CargoReplacementPlaceholders(replacements)

	if len(got) != 1 {
		t.Fatalf("CargoReplacementPlaceholders() = %+v, want one deduped export", got)
	}
}
