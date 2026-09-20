package ecosystem

import (
	"fmt"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/registryvocab"
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

// A [registries.NAME] table with no `index` assignment must yield no decl at
// all, not a decl with an empty Index.
func TestParseCargoRegistryDecls_NoIndexLine(t *testing.T) {
	content := `[registries.othercorp]
token = "irrelevant"
`
	got := ParseCargoRegistryDecls(content)
	if len(got) != 0 {
		t.Errorf("ParseCargoRegistryDecls() = %+v, want empty", got)
	}
}

// The untrusted-name guard: a quoted TOML key carrying shell metacharacters
// must never reach a caller that could turn it into a shell-sourced env var
// name.
func TestParseCargoRegistryDecls_HostileNameSkipped(t *testing.T) {
	content := `[registries."evil; rm -rf /"]
index = "sparse+https://cargo.example.test/index/"
`
	got := ParseCargoRegistryDecls(content)
	if len(got) != 0 {
		t.Errorf("ParseCargoRegistryDecls() = %+v, want empty (hostile name must be skipped)", got)
	}
}

// A naive quote-trim keeps a legal-TOML trailing comment inside the value,
// which still host-matches its route and renders verbatim into the
// [source....] registry stanza, so cargo sees a URL that matches nothing and
// the replacement silently never binds.
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

// A single-quoted TOML literal string is as legal as a basic one.
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

// Each index value that is not a well-formed TOML string must yield no decl
// at all. The first index line in a section wins, so a malformed one must not
// fall through to a later, well-formed one.
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

func TestParseCargoSourceDecls_SingleStanza(t *testing.T) {
	content := `[source.othercorp]
registry = "sparse+https://cargo.example.test/index/"
`
	want := []CargoSourceDecl{{Name: "othercorp", Registry: "sparse+https://cargo.example.test/index/"}}

	got := ParseCargoSourceDecls(content)

	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("ParseCargoSourceDecls() = %+v, want %+v", got, want)
	}
}

// The repro's own [source.crates-io] shape: a stanza carrying only
// replace-with, with no registry key, claims no URL and must yield no decl.
func TestParseCargoSourceDecls_ReplaceWithOnlyNoDecl(t *testing.T) {
	content := `[source.crates-io]
replace-with = "artifactory-remote"
`
	got := ParseCargoSourceDecls(content)
	if len(got) != 0 {
		t.Errorf("ParseCargoSourceDecls() = %+v, want empty", got)
	}
}

// A quoted TOML key must never reach a caller that turns it into a TOML table
// name.
func TestParseCargoSourceDecls_HostileNameSkipped(t *testing.T) {
	content := `[source."evil; rm -rf /"]
registry = "sparse+https://cargo.example.test/index/"
`
	got := ParseCargoSourceDecls(content)
	if len(got) != 0 {
		t.Errorf("ParseCargoSourceDecls() = %+v, want empty (hostile name must be skipped)", got)
	}
}

func TestParseCargoSourceDecls_TrailingComment(t *testing.T) {
	content := `[source.othercorp]
registry = "sparse+https://cargo.example.test/index/" # note
`
	want := CargoSourceDecl{Name: "othercorp", Registry: "sparse+https://cargo.example.test/index/"}

	got := ParseCargoSourceDecls(content)

	if len(got) != 1 || got[0] != want {
		t.Errorf("ParseCargoSourceDecls() = %+v, want %+v", got, []CargoSourceDecl{want})
	}
}

func TestParseCargoSourceDecls_LiteralString(t *testing.T) {
	content := `[source.othercorp]
registry = 'sparse+https://cargo.example.test/index/'
`
	want := CargoSourceDecl{Name: "othercorp", Registry: "sparse+https://cargo.example.test/index/"}

	got := ParseCargoSourceDecls(content)

	if len(got) != 1 || got[0] != want {
		t.Errorf("ParseCargoSourceDecls() = %+v, want %+v", got, []CargoSourceDecl{want})
	}
}

// A name repeated across two [source.NAME] headers keeps its first
// occurrence, mirroring ParseCargoRegistryDecls' contract.
func TestParseCargoSourceDecls_DedupedFirstWins(t *testing.T) {
	content := `[source.othercorp]
registry = "sparse+https://first.example.test/index/"

[source.othercorp]
registry = "sparse+https://second.example.test/index/"
`
	got := ParseCargoSourceDecls(content)
	if len(got) != 1 || got[0].Registry != "sparse+https://first.example.test/index/" {
		t.Errorf("ParseCargoSourceDecls() = %+v, want the first occurrence only", got)
	}
}

// A [registries.NAME] table with an index key must never be mistaken for a
// [source.NAME] table, even though both kinds of table can appear in the same
// repoConfig.
func TestParseCargoSourceDecls_RegistriesTableNotMistaken(t *testing.T) {
	content := `[registries.othercorp]
index = "sparse+https://cargo.example.test/index/"
`
	got := ParseCargoSourceDecls(content)
	if len(got) != 0 {
		t.Errorf("ParseCargoSourceDecls() = %+v, want empty", got)
	}
}

// The issue's exact repro: the repo config's own [source.artifactory-remote]
// already claims the registries decl's index URL, so the upstream stanza must
// reuse that name instead of minting spindrift-upstream-artifactory-remote.
// A second [source.…] stanza on the same URL is a hard cargo error ("source
// ... already defined by ...").
func TestCargoSourceReplacements_ReusesClaimingSourceName(t *testing.T) {
	const port = 27182
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "crates.io"},
		{Prefix: "r1", UpstreamHost: "artifactory.example.test"},
	}
	repoConfig := `[registries.artifactory-remote]
index = "sparse+https://artifactory.example.test/artifactory/api/cargo/remote/index/"

[source.crates-io]
replace-with = "artifactory-remote"

[source.artifactory-remote]
registry = "sparse+https://artifactory.example.test/artifactory/api/cargo/remote/index/"
`

	got, warnings := CargoSourceReplacements(port, "r0", routes, repoConfig)

	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	if len(got) != 1 || len(got[0].Upstreams) != 1 {
		t.Fatalf("CargoSourceReplacements() = %+v, want exactly one replacement with one upstream", got)
	}
	up := got[0].Upstreams[0]
	if up.SourceName != "artifactory-remote" {
		t.Errorf("SourceName = %q, want reused %q", up.SourceName, "artifactory-remote")
	}
	if up.IndexURL != "sparse+https://artifactory.example.test/artifactory/api/cargo/remote/index/" {
		t.Errorf("IndexURL = %q, want the registries decl's index", up.IndexURL)
	}
}

// The AC 2 regression guard: a repo with only [registries.*] tables and no
// [source.*] stanza claiming the URL must keep the pre-#3248 minted name.
func TestCargoSourceReplacements_NoClaimingSourceKeepsMintedName(t *testing.T) {
	const port = 27182
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "crates.io"},
		{Prefix: "r1", UpstreamHost: "cargo.example.test"},
	}
	repoConfig := `[registries.othercorp]
index = "sparse+https://cargo.example.test/index/"
`

	got, _ := CargoSourceReplacements(port, "r0", routes, repoConfig)

	if len(got) != 1 || len(got[0].Upstreams) != 1 || got[0].Upstreams[0].SourceName != "spindrift-upstream-othercorp" {
		t.Fatalf("CargoSourceReplacements() = %+v, want the minted name unchanged", got)
	}
}

// Reusing a source name the home render already owns (crates-io,
// spindrift-registry-proxy, or a per-route
// spindrift-registry-proxy-<prefix>-<name>) would emit a duplicate TOML table
// within one file, so the minting site falls back to the pre-existing minted
// name, its pre-existing collision included, rather than drop the upstream.
func TestCargoSourceReplacements_GuardedNamesFallBackToMinted(t *testing.T) {
	const port = 27182
	for _, guarded := range []string{"crates-io", "spindrift-registry-proxy", "spindrift-registry-proxy-r1-othercorp"} {
		t.Run(guarded, func(t *testing.T) {
			routes := []registrymanifest.Route{
				{Prefix: "r0", UpstreamHost: "crates.io"},
				{Prefix: "r1", UpstreamHost: "cargo.example.test"},
			}
			repoConfig := `[registries.othercorp]
index = "sparse+https://cargo.example.test/index/"

[source.` + guarded + `]
registry = "sparse+https://cargo.example.test/index/"
`

			got, _ := CargoSourceReplacements(port, "r0", routes, repoConfig)

			if len(got) != 1 || len(got[0].Upstreams) != 1 || got[0].Upstreams[0].SourceName != "spindrift-upstream-othercorp" {
				t.Fatalf("CargoSourceReplacements() = %+v, want the minted name (guard rejected %q)", got, guarded)
			}
		})
	}
}

// The third class of home-owned name is a minted spindrift-upstream-<name>
// the render might itself emit. A repo [source.spindrift-upstream-b] claiming
// decl a's index URL used to make decl a adopt that name, which decl b mints
// by default too, rendering the same [source.…] table twice in one file.
// Guarding every minted name up front falls back to decl a's own mint.
func TestCargoSourceReplacements_RepoSourceNamedAfterMintedUpstreamIsGuarded(t *testing.T) {
	const port = 27182
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "crates.io"},
		{Prefix: "r1", UpstreamHost: "cargo.example.test"},
	}
	repoConfig := `[registries.a]
index = "sparse+https://cargo.example.test/a/"

[registries.b]
index = "sparse+https://cargo.example.test/b/"

[source.spindrift-upstream-b]
registry = "sparse+https://cargo.example.test/a/"
`

	got, warnings := CargoSourceReplacements(port, "r0", routes, repoConfig)
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	if len(got) != 2 {
		t.Fatalf("CargoSourceReplacements() = %+v, want two replacements", got)
	}
	names := map[string]bool{}
	for _, rep := range got {
		for _, up := range rep.Upstreams {
			names[up.SourceName] = true
		}
	}
	if !names["spindrift-upstream-a"] {
		t.Errorf("CargoSourceReplacements() = %+v, want decl a to keep its minted name spindrift-upstream-a", got)
	}

	// The repo's own [source.spindrift-upstream-b] still collides on aURL with
	// home's separately-minted [source.spindrift-upstream-a] once merged, which
	// assertNoDuplicateCargoSourceTables's cross-file URL check (gotcha 1) would
	// flag. That is not this fix's target. Its contract is narrower: the
	// rendered home file alone must never carry the same [source.…] name twice.
	rendered, _, warnings := CargoRepoAwareConfig(port, "r0", routes, repoConfig)
	if len(warnings) != 0 {
		t.Errorf("CargoRepoAwareConfig() warnings = %v, want none", warnings)
	}
	homeNameCount := make(map[string]int)
	for _, occ := range scanCargoNamedTableOccurrences(rendered, "source.", "registry") {
		homeNameCount[occ.name]++
	}
	for name, count := range homeNameCount {
		if count > 1 {
			t.Errorf("home config declares [source.%s] %d times, want at most once", name, count)
		}
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
	if rep.ProxySource != "spindrift-registry-proxy-r1-othercorp" {
		t.Errorf("ProxySource = %q, want %q", rep.ProxySource, "spindrift-registry-proxy-r1-othercorp")
	}
	if rep.LocalIndexURL != "sparse+http://127.0.0.1:27182/r1/index/" {
		t.Errorf("LocalIndexURL = %q, want %q", rep.LocalIndexURL, "sparse+http://127.0.0.1:27182/r1/index/")
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

// When a route's cargo block declares a non-empty registries list, only names
// in that list get stanzas, even though another host-matching decl exists in
// repoConfig.
func TestCargoSourceReplacements_DeclaredListRestrictsStanzas(t *testing.T) {
	const port = 27182
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "crates.io"},
		{Prefix: "r1", UpstreamHost: "cargo.example.test", Ecosystems: CargoRouteBlock("declared-registry")},
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

// The drift case the retired ApplyNoopContent diagnostic used to catch: the
// route declares a registry, the repo config declares it too, but its index
// points at some other host, so nothing binds. Silence here would fail a
// network-less cargo build with no signal at all.
func TestCargoSourceReplacements_DeclaredNameOnWrongHostWarns(t *testing.T) {
	const port = 27182
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "crates.io"},
		{Prefix: "r1", UpstreamHost: "cargo.example.test", Ecosystems: CargoRouteBlock("acme")},
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

// A manifest naming a registry the repo config never declares at all.
func TestCargoSourceReplacements_DeclaredNameAbsentWarns(t *testing.T) {
	const port = 27182
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "crates.io"},
		{Prefix: "r1", UpstreamHost: "cargo.example.test", Ecosystems: CargoRouteBlock("present", "absent")},
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

// A declared registry whose index value the TOML value parser rejects never
// reaches the host match, so only the declared-name sweep can report it.
func TestCargoSourceReplacements_DeclaredNameMalformedIndexWarns(t *testing.T) {
	const port = 27182
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "crates.io"},
		{Prefix: "r1", UpstreamHost: "cargo.example.test", Ecosystems: CargoRouteBlock("acme")},
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

// When two declared names share one real index URL, the second collapses into
// the first's stanza and genuinely binds through it, so it is not unsatisfied
// and must not warn.
func TestCargoSourceReplacements_DeclaredNameDedupedIsNotAMiss(t *testing.T) {
	const port = 27182
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "crates.io"},
		{Prefix: "r1", UpstreamHost: "cargo.example.test", Ecosystems: CargoRouteBlock("first-name", "second-name")},
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

// A route declaring no cargo registries declares nothing, so nothing can go
// unsatisfied, even when the repo config matches none of its host.
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

// The absent half of the filter: a route carrying no cargo block at all
// restricts nothing, so every host-matching decl in the repo config binds and
// no name goes unsatisfied.
func TestCargoSourceReplacements_NoCargoBlockBindsEveryHostMatch(t *testing.T) {
	const port = 27182
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "crates.io"},
		{Prefix: "r1", UpstreamHost: "cargo.example.test"},
	}
	repoConfig := `[registries.first-registry]
index = "sparse+https://cargo.example.test/first-index/"

[registries.second-registry]
index = "sparse+https://cargo.example.test/second-index/"
`

	got, warnings := CargoSourceReplacements(port, "r0", routes, repoConfig)

	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	var names []string
	for _, rep := range got {
		for _, up := range rep.Upstreams {
			names = append(names, up.SourceName)
		}
	}
	if len(names) != 2 || names[0] != "spindrift-upstream-first-registry" || names[1] != "spindrift-upstream-second-registry" {
		t.Errorf("upstream source names = %v, want both host-matching decls bound", names)
	}
}

// When a route's only block belongs to an ecosystem cargo has no notion of,
// the lookup misses, so the route behaves exactly like one carrying no block
// at all rather than erroring or restricting anything.
func TestCargoSourceReplacements_UnknownEcosystemBlockIgnored(t *testing.T) {
	const port = 27182
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "crates.io"},
		{
			Prefix:       "r1",
			UpstreamHost: "cargo.example.test",
			Ecosystems:   registryvocab.RouteEcosystems{"pypi": registryvocab.RouteDeclaration{"path": "/pypi"}},
		},
	}
	repoConfig := `[registries.othercorp]
index = "sparse+https://cargo.example.test/index/"
`

	got, warnings := CargoSourceReplacements(port, "r0", routes, repoConfig)

	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	if len(got) != 1 || len(got[0].Upstreams) != 1 || got[0].Upstreams[0].SourceName != "spindrift-upstream-othercorp" {
		t.Fatalf("CargoSourceReplacements() = %+v, want the host-matching decl bound", got)
	}
}

// Cargo maps a URL to a source name 1:1, so two registry names pointing at
// the same real index URL must collapse into one upstream stanza, keeping the
// first name's stanza in repo-config appearance order.
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

// A registry name that fails cargoBareKeyPattern must never produce an
// Upstream, since it would otherwise flow into a rendered TOML table name or
// a shell-sourced env var name.
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

// Two routes naming registries under distinct upstream hosts must each
// produce their own replacement, in manifest route order.
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

// registrymanifest.Route.UpstreamHost is minted as u.Host (box.go), which
// keeps a port when the upstream URL has one, so the decl's parsed host must
// keep it too or a ported upstream never matches its route.
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

// An empty replacements slice must return CargoConfigTOML's own output
// byte-for-byte, since that is the pre-#3201 render every existing caller and
// test still pins.
func TestCargoConfigTOMLWithReplacements_EmptyPlanPassthrough(t *testing.T) {
	got := CargoConfigTOMLWithReplacements(27182, "r0", nil)
	want := CargoConfigTOML(27182, "r0", nil)
	if got != want {
		t.Errorf("CargoConfigTOMLWithReplacements() = %q, want %q (byte-identical to CargoConfigTOML)", got, want)
	}
}

// The route's prefix differs from the crates-io prefix, so it mints its own
// proxy source.
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

// The spindrift-registry-proxy [source....] stanza is already in
// CargoConfigTOML's base render, so the replacement block must emit only its
// [registries....] entry, never a second [source.spindrift-registry-proxy].
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

// With two Upstreams on one route, each gets its own
// [source.spindrift-upstream-…] stanza, but the [source.…]/[registries.…]
// proxy pair is emitted once, after both upstreams.
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

// Two named-registry routes each mint their own proxy source, and both blocks
// appear in the replacements slice's order.
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
	if value, ok := ExportValue(got, "CARGO_REGISTRIES_SPINDRIFT_REGISTRY_PROXY_R1_TOKEN"); !ok || value != CargoPlaceholderToken {
		t.Errorf("ExportValue(r1 token) = (%q, %v), want (%q, true)", value, ok, CargoPlaceholderToken)
	}
	if value, ok := ExportValue(got, "CARGO_REGISTRIES_SPINDRIFT_REGISTRY_PROXY_R2_TOKEN"); !ok || value != CargoPlaceholderToken {
		t.Errorf("ExportValue(r2 token) = (%q, %v), want (%q, true)", value, ok, CargoPlaceholderToken)
	}
}

// Two replacements sharing one ProxySource (the reuse case) must yield the
// placeholder export once, not twice.
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

// assertNoDuplicateCargoSourceTables pins AC 1 (issue #3248): homeConfig alone
// must never declare the same [source.…] table name twice (a same-file
// duplicate table is a TOML error, not a merge question), and once merged with
// repoConfig no two table names may claim the same registry URL (gotcha 1). A
// stanza with no registry key claims no URL and is exempt from that second check.
func assertNoDuplicateCargoSourceTables(t *testing.T, repoConfig, homeConfig string) {
	t.Helper()

	homeOccurrences := scanCargoNamedTableOccurrences(homeConfig, "source.", "registry")
	homeNameCount := make(map[string]int)
	for _, occ := range homeOccurrences {
		homeNameCount[occ.name]++
	}
	for name, count := range homeNameCount {
		if count > 1 {
			t.Errorf("home config declares [source.%s] %d times, want at most once", name, count)
		}
	}

	nameByURL := make(map[string]string)
	merged := append(scanCargoNamedTableOccurrences(repoConfig, "source.", "registry"), homeOccurrences...)
	for _, occ := range merged {
		if occ.value == "" {
			continue
		}
		if existing, ok := nameByURL[occ.value]; ok && existing != occ.name {
			t.Errorf("registry URL %q is claimed by both [source.%s] and [source.%s]", occ.value, existing, occ.name)
			continue
		}
		nameByURL[occ.value] = occ.name
	}
}

// cargoArtifactoryRepoConfig is the issue's exact repro repo config: a
// corporate registry declared under [registries.*] and re-claimed under its
// own [source.*] name, with the repo also routing crates-io through it.
const cargoArtifactoryRepoConfig = `[registries.artifactory-remote]
index = "sparse+https://artifactory.example.test/artifactory/api/cargo/remote/index/"

[source.crates-io]
replace-with = "artifactory-remote"

[source.artifactory-remote]
registry = "sparse+https://artifactory.example.test/artifactory/api/cargo/remote/index/"
`

// The issue's end-to-end repro: on a route whose UpstreamHost matches the
// repo-claimed registry, on a prefix other than the crates-io prefix, the
// render must reuse the repo's own [source.artifactory-remote] name rather
// than mint [source.spindrift-upstream-artifactory-remote], which would be a
// second stanza on the same URL that cargo rejects.
func TestCargoRepoAwareConfig_RepoClaimingSourceNameRendersMergeableConfig(t *testing.T) {
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "crates.io"},
		{Prefix: "r1", UpstreamHost: "artifactory.example.test"},
	}

	got, _, warnings := CargoRepoAwareConfig(27182, "r0", routes, cargoArtifactoryRepoConfig)

	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}

	want := `[source.crates-io]
replace-with = "spindrift-registry-proxy"

[source.spindrift-registry-proxy]
registry = "sparse+http://127.0.0.1:27182/r0/"

[registry]
global-credential-providers = ["cargo:token"]

[source.artifactory-remote]
registry = "sparse+https://artifactory.example.test/artifactory/api/cargo/remote/index/"
replace-with = "spindrift-registry-proxy-r1-artifactory-remote"

[source.spindrift-registry-proxy-r1-artifactory-remote]
registry = "sparse+http://127.0.0.1:27182/r1/artifactory/api/cargo/remote/index/"

[registries.spindrift-registry-proxy-r1-artifactory-remote]
index = "sparse+http://127.0.0.1:27182/r1/artifactory/api/cargo/remote/index/"
`
	if got != want {
		t.Errorf("CargoRepoAwareConfig() content = %q, want %q", got, want)
	}

	// Redundant against the golden above on today's render, but stated as its
	// own claim so a future golden churn cannot quietly reintroduce a minted
	// stanza. The mint is the whole defect this issue fixes.
	if strings.Contains(got, "spindrift-upstream-") {
		t.Errorf("CargoRepoAwareConfig() content = %q, want no spindrift-upstream- table at all", got)
	}
}

// Two declared registries whose index URLs differ only in scheme resolve to
// the same local index URL, so the second reuses the proxy source the first
// minted and the shared [source....]/[registries....] pair renders once.
// Emitting it per replacement would declare the same table name twice in one
// file, a TOML error cargo refuses to parse at all.
func TestCargoRepoAwareConfig_TwoRegistriesOnOneIndexPath(t *testing.T) {
	repoConfig := `[registries.othercorp]
index = "http://cargo.example.test/other-index/"

[registries.other]
index = "sparse+https://cargo.example.test/other-index/"
`
	routes := []registrymanifest.Route{{Prefix: "r1", UpstreamHost: "cargo.example.test"}}

	got, exports, _ := CargoRepoAwareConfig(27182, "r0", routes, repoConfig)

	assertNoDuplicateCargoSourceTables(t, repoConfig, got)

	if n := strings.Count(got, "[registries.spindrift-registry-proxy-r1-othercorp]"); n != 1 {
		t.Errorf("CargoRepoAwareConfig() content declares [registries.spindrift-registry-proxy-r1-othercorp] %d times, want exactly 1: %q", n, got)
	}

	// Both upstream stanzas still render: deduping the shared pair must not
	// swallow the second replacement's own source stanza with it.
	for _, want := range []string{
		"[source.spindrift-upstream-othercorp]\nregistry = \"http://cargo.example.test/other-index/\"\nreplace-with = \"spindrift-registry-proxy-r1-othercorp\"\n",
		"[source.spindrift-upstream-other]\nregistry = \"sparse+https://cargo.example.test/other-index/\"\nreplace-with = \"spindrift-registry-proxy-r1-othercorp\"\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("CargoRepoAwareConfig() content = %q, want it to contain %q", got, want)
		}
	}

	if len(exports) != 1 || exports[0].Name != "CARGO_REGISTRIES_SPINDRIFT_REGISTRY_PROXY_R1_OTHERCORP_TOKEN" {
		t.Errorf("CargoRepoAwareConfig() exports = %+v, want one export for the shared proxy source", exports)
	}
}

// The AC 1 regression, run over the repro case, where reuse is exercised, and
// over a repo with no claiming [source.*] stanza, where the minted name is
// exercised instead. The no-duplicate contract must hold either way the source
// name was chosen.
func TestCargoRepoAwareConfig_NoDuplicateSourceTablesAcrossMerge(t *testing.T) {
	t.Run("reused claiming name", func(t *testing.T) {
		routes := []registrymanifest.Route{
			{Prefix: "r0", UpstreamHost: "crates.io"},
			{Prefix: "r1", UpstreamHost: "artifactory.example.test"},
		}
		got, _, _ := CargoRepoAwareConfig(27182, "r0", routes, cargoArtifactoryRepoConfig)
		assertNoDuplicateCargoSourceTables(t, cargoArtifactoryRepoConfig, got)
	})

	t.Run("minted name, no claim", func(t *testing.T) {
		repoConfig := `[registries.othercorp]
index = "sparse+https://cargo.example.test/index/"
`
		routes := []registrymanifest.Route{
			{Prefix: "r0", UpstreamHost: "crates.io"},
			{Prefix: "r1", UpstreamHost: "cargo.example.test"},
		}
		got, _, _ := CargoRepoAwareConfig(27182, "r0", routes, repoConfig)
		assertNoDuplicateCargoSourceTables(t, repoConfig, got)
	})
}

// Pins the emergent crates-io chain (issue #3248, gotcha 7) as an intended
// contract, not an accident: the repo's own [source.crates-io] replace-with
// overrides the home render's, because in-tree config wins cargo's
// hierarchical merge. Plain crates-io traffic is meant to bypass the crates-io
// route (r0) and ride the corporate route (r1) the repo declared it served from.
func TestCargoRepoAwareConfig_RepoCratesIOReplaceWithChainsToTheNamedRegistryRoute(t *testing.T) {
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "crates.io"},
		{Prefix: "r1", UpstreamHost: "artifactory.example.test"},
	}

	got, _, _ := CargoRepoAwareConfig(27182, "r0", routes, cargoArtifactoryRepoConfig)

	// Link 1: the repo's own file overrides the home render's crates-io
	// replace-with. Asserted on repoConfig, since that link is cargo's
	// file-merge behavior, not something CargoRepoAwareConfig renders.
	if !strings.Contains(cargoArtifactoryRepoConfig, "[source.crates-io]\nreplace-with = \"artifactory-remote\"\n") {
		t.Fatalf("repro repoConfig lost its crates-io override -- test fixture drifted")
	}

	// Link 2: artifactory-remote (the reused name) replaces to the r1 proxy
	// source, not the crates-io route's (spindrift-registry-proxy).
	if !strings.Contains(got, "[source.artifactory-remote]\nregistry = \"sparse+https://artifactory.example.test/artifactory/api/cargo/remote/index/\"\nreplace-with = \"spindrift-registry-proxy-r1-artifactory-remote\"\n") {
		t.Errorf("CargoRepoAwareConfig() content = %q, want artifactory-remote to replace-with spindrift-registry-proxy-r1-artifactory-remote", got)
	}

	// Link 3: the r1 proxy source's registry is r1's own Forwarder URL, not
	// r0's (the crates-io route's prefix passed in as this render's prefix
	// argument). The bypass landing on the named-registry route's prefix is
	// exactly the contract being pinned here.
	if !strings.Contains(got, "[source.spindrift-registry-proxy-r1-artifactory-remote]\nregistry = \"sparse+http://127.0.0.1:27182/r1/artifactory/api/cargo/remote/index/\"\n") {
		t.Errorf("CargoRepoAwareConfig() content = %q, want spindrift-registry-proxy-r1-artifactory-remote to terminate at the r1 Forwarder URL", got)
	}
}

// AC 2's end-to-end pin: a repo with only [registries.*] tables and no
// claiming [source.*] must render the pre-#3248 minted
// spindrift-upstream-<name> output byte-for-byte. Other tests pin the plan and
// pin this byte output from a hand-built plan; this is the missing link, the
// same repoConfig run through the full CargoRepoAwareConfig entry point.
func TestCargoRepoAwareConfig_NoSourceClaimMintsUnchanged(t *testing.T) {
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "crates.io"},
		{Prefix: "r1", UpstreamHost: "cargo.example.test"},
	}
	repoConfig := `[registries.othercorp]
index = "sparse+https://cargo.example.test/index/"
`

	got, _, warnings := CargoRepoAwareConfig(27182, "r0", routes, repoConfig)

	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}

	want := `[source.crates-io]
replace-with = "spindrift-registry-proxy"

[source.spindrift-registry-proxy]
registry = "sparse+http://127.0.0.1:27182/r0/"

[registry]
global-credential-providers = ["cargo:token"]

[source.spindrift-upstream-othercorp]
registry = "sparse+https://cargo.example.test/index/"
replace-with = "spindrift-registry-proxy-r1-othercorp"

[source.spindrift-registry-proxy-r1-othercorp]
registry = "sparse+http://127.0.0.1:27182/r1/index/"

[registries.spindrift-registry-proxy-r1-othercorp]
index = "sparse+http://127.0.0.1:27182/r1/index/"
`
	if got != want {
		t.Errorf("CargoRepoAwareConfig() content = %q, want %q", got, want)
	}
}

// Issue #3256's headline acceptance criterion at the plan level: a route with
// two registries on its host must not fold them onto one local index URL (the
// legacy per-route grouping's bug). Each registry's own upstream index path
// carries into its own local URL and minted proxy source, so the Forwarder's
// per-registry enforced subtree has a URL to key off of.
func TestCargoSourceReplacements_TwoRegistriesDistinctLocalURLs(t *testing.T) {
	const port = 27182
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "crates.io"},
		{Prefix: "r1", UpstreamHost: "artifactory.example.test"},
	}
	repoConfig := `[registries.artifactory-internal]
index = "sparse+https://artifactory.example.test/artifactory/api/cargo/internal"

[registries.artifactory-remote]
index = "sparse+https://artifactory.example.test/artifactory/api/cargo/remote"
`

	got, warnings := CargoSourceReplacements(port, "r0", routes, repoConfig)

	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	if len(got) != 2 {
		t.Fatalf("CargoSourceReplacements() = %+v, want two replacements (one per registry)", got)
	}

	internal, remote := got[0], got[1]

	if internal.LocalIndexURL != "sparse+http://127.0.0.1:27182/r1/artifactory/api/cargo/internal/" {
		t.Errorf("internal.LocalIndexURL = %q, want the internal index's own path embedded", internal.LocalIndexURL)
	}
	if remote.LocalIndexURL != "sparse+http://127.0.0.1:27182/r1/artifactory/api/cargo/remote/" {
		t.Errorf("remote.LocalIndexURL = %q, want the remote index's own path embedded", remote.LocalIndexURL)
	}
	if internal.LocalIndexURL == remote.LocalIndexURL {
		t.Fatalf("both registries share LocalIndexURL %q -- the two-registry fold-down bug", internal.LocalIndexURL)
	}
	if internal.ProxySource == remote.ProxySource {
		t.Errorf("both registries share ProxySource %q, want distinct minted names", internal.ProxySource)
	}
	if internal.Prefix != "r1" || remote.Prefix != "r1" {
		t.Errorf("Prefix = [%q, %q], want both %q", internal.Prefix, remote.Prefix, "r1")
	}

	if len(internal.Upstreams) != 1 || internal.Upstreams[0].IndexURL != "sparse+https://artifactory.example.test/artifactory/api/cargo/internal" {
		t.Errorf("internal.Upstreams = %+v, want the real internal index URL byte-for-byte", internal.Upstreams)
	}
	if len(remote.Upstreams) != 1 || remote.Upstreams[0].IndexURL != "sparse+https://artifactory.example.test/artifactory/api/cargo/remote" {
		t.Errorf("remote.Upstreams = %+v, want the real remote index URL byte-for-byte", remote.Upstreams)
	}
}

// When a host-rooted registry's index carries no path at all (the host serves
// the index at its root), the per-registry local URL degrades to the same
// shape a legacy route would render, since there is no path to embed.
func TestCargoSourceReplacements_NoIndexPathDegradesToRoutePrefixURL(t *testing.T) {
	const port = 27182
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "crates.io"},
		{Prefix: "r1", UpstreamHost: "cargo.example.test"},
	}
	repoConfig := `[registries.othercorp]
index = "sparse+https://cargo.example.test"
`

	got, warnings := CargoSourceReplacements(port, "r0", routes, repoConfig)

	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	if len(got) != 1 {
		t.Fatalf("CargoSourceReplacements() = %+v, want exactly one replacement", got)
	}
	if got[0].LocalIndexURL != "sparse+http://127.0.0.1:27182/r1/" {
		t.Errorf("LocalIndexURL = %q, want the pathless degrade %q", got[0].LocalIndexURL, "sparse+http://127.0.0.1:27182/r1/")
	}
}

// The one case a host-rooted registry's minted local URL can still collide
// with the crates-io replacement's own: a pathless index on a route sharing
// the crates-io render's own prefix. Cargo's URL to source-name 1:1 rule then
// requires reusing spindrift-registry-proxy rather than minting a second
// [source.…] stanza against the same URL.
func TestCargoSourceReplacements_ReusesCratesIOSourceWhenLocalURLsCoincide(t *testing.T) {
	const port = 27182
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "cargo.example.test"},
	}
	repoConfig := `[registries.othercorp]
index = "sparse+https://cargo.example.test"
`

	got, _ := CargoSourceReplacements(port, "r0", routes, repoConfig)

	if len(got) != 1 {
		t.Fatalf("CargoSourceReplacements() = %+v, want exactly one replacement", got)
	}
	if got[0].ProxySource != registryProxySourceName {
		t.Errorf("ProxySource = %q, want reused %q", got[0].ProxySource, registryProxySourceName)
	}
}

// A route's two registries render two distinct proxy-source/registries stanza
// pairs, one per registry, alongside their own upstream stanzas.
func TestCargoConfigTOMLWithReplacements_TwoRegistries(t *testing.T) {
	replacements := []CargoSourceReplacement{
		{
			Prefix:        "r1",
			ProxySource:   "spindrift-registry-proxy-r1-artifactory-internal",
			LocalIndexURL: "sparse+http://127.0.0.1:27182/r1/artifactory/api/cargo/internal/",
			Upstreams: []CargoUpstreamSource{
				{SourceName: "spindrift-upstream-artifactory-internal", IndexURL: "sparse+https://artifactory.example.test/artifactory/api/cargo/internal"},
			},
		},
		{
			Prefix:        "r1",
			ProxySource:   "spindrift-registry-proxy-r1-artifactory-remote",
			LocalIndexURL: "sparse+http://127.0.0.1:27182/r1/artifactory/api/cargo/remote/",
			Upstreams: []CargoUpstreamSource{
				{SourceName: "spindrift-upstream-artifactory-remote", IndexURL: "sparse+https://artifactory.example.test/artifactory/api/cargo/remote"},
			},
		},
	}

	got := CargoConfigTOMLWithReplacements(27182, "r0", replacements)

	want := CargoConfigTOML(27182, "r0", nil) + `
[registry]
global-credential-providers = ["cargo:token"]

[source.spindrift-upstream-artifactory-internal]
registry = "sparse+https://artifactory.example.test/artifactory/api/cargo/internal"
replace-with = "spindrift-registry-proxy-r1-artifactory-internal"

[source.spindrift-registry-proxy-r1-artifactory-internal]
registry = "sparse+http://127.0.0.1:27182/r1/artifactory/api/cargo/internal/"

[registries.spindrift-registry-proxy-r1-artifactory-internal]
index = "sparse+http://127.0.0.1:27182/r1/artifactory/api/cargo/internal/"

[source.spindrift-upstream-artifactory-remote]
registry = "sparse+https://artifactory.example.test/artifactory/api/cargo/remote"
replace-with = "spindrift-registry-proxy-r1-artifactory-remote"

[source.spindrift-registry-proxy-r1-artifactory-remote]
registry = "sparse+http://127.0.0.1:27182/r1/artifactory/api/cargo/remote/"

[registries.spindrift-registry-proxy-r1-artifactory-remote]
index = "sparse+http://127.0.0.1:27182/r1/artifactory/api/cargo/remote/"
`
	if got != want {
		t.Errorf("CargoConfigTOMLWithReplacements() = %q, want %q", got, want)
	}
}

// The placeholder side of the same two-registry plan: two distinct proxy
// sources must yield two distinct CARGO_REGISTRIES_<NAME>_TOKEN exports, not
// the one folded export a shared local URL would have produced.
func TestCargoReplacementPlaceholders_TwoRegistries(t *testing.T) {
	replacements := []CargoSourceReplacement{
		{ProxySource: "spindrift-registry-proxy-r1-artifactory-internal"},
		{ProxySource: "spindrift-registry-proxy-r1-artifactory-remote"},
	}

	got := CargoReplacementPlaceholders(replacements)

	if len(got) != 2 {
		t.Fatalf("CargoReplacementPlaceholders() = %+v, want two exports", got)
	}
	if got[0].Name != "CARGO_REGISTRIES_SPINDRIFT_REGISTRY_PROXY_R1_ARTIFACTORY_INTERNAL_TOKEN" {
		t.Errorf("got[0].Name = %q, want the internal proxy source's var name", got[0].Name)
	}
	if got[1].Name != "CARGO_REGISTRIES_SPINDRIFT_REGISTRY_PROXY_R1_ARTIFACTORY_REMOTE_TOKEN" {
		t.Errorf("got[1].Name = %q, want the remote proxy source's var name", got[1].Name)
	}
}

// Issue #3256's headline acceptance criterion end to end: a repo declaring two
// cargo registries on one host, routed through a single host-rooted route,
// must render two distinct proxy stanzas that each carry their own registry's
// real index URL, not one route-wide fold-down.
func TestCargoRepoAwareConfig_TwoRegistriesResolveThroughOneRoute(t *testing.T) {
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "crates.io"},
		{Prefix: "r1", UpstreamHost: "artifactory.example.test"},
	}
	repoConfig := `[registries.artifactory-internal]
index = "sparse+https://artifactory.example.test/artifactory/api/cargo/internal"

[registries.artifactory-remote]
index = "sparse+https://artifactory.example.test/artifactory/api/cargo/remote"
`

	got, exports, warnings := CargoRepoAwareConfig(27182, "r0", routes, repoConfig)

	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	if !strings.Contains(got, `[source.spindrift-upstream-artifactory-internal]
registry = "sparse+https://artifactory.example.test/artifactory/api/cargo/internal"
replace-with = "spindrift-registry-proxy-r1-artifactory-internal"`) {
		t.Errorf("CargoRepoAwareConfig() content = %q, want the internal registry's own upstream/replace-with stanza", got)
	}
	if !strings.Contains(got, `[source.spindrift-upstream-artifactory-remote]
registry = "sparse+https://artifactory.example.test/artifactory/api/cargo/remote"
replace-with = "spindrift-registry-proxy-r1-artifactory-remote"`) {
		t.Errorf("CargoRepoAwareConfig() content = %q, want the remote registry's own upstream/replace-with stanza", got)
	}
	if !strings.Contains(got, `[registries.spindrift-registry-proxy-r1-artifactory-internal]
index = "sparse+http://127.0.0.1:27182/r1/artifactory/api/cargo/internal/"`) {
		t.Errorf("CargoRepoAwareConfig() content = %q, want the internal registry's own [registries.…] index", got)
	}
	if !strings.Contains(got, `[registries.spindrift-registry-proxy-r1-artifactory-remote]
index = "sparse+http://127.0.0.1:27182/r1/artifactory/api/cargo/remote/"`) {
		t.Errorf("CargoRepoAwareConfig() content = %q, want the remote registry's own [registries.…] index", got)
	}
	if len(exports) != 2 {
		t.Errorf("exports = %+v, want two placeholder token exports (one per registry)", exports)
	}
}

// Issue #3248's crates-io-chained shape on a route: the repo replaces
// crates-io with its own named source, which this render must still chain onto
// the route's minted proxy source rather than treating host-rootedness as a
// reason to skip the reuse.
func TestCargoRepoAwareConfig_CratesIOChainStillComposes(t *testing.T) {
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "crates.io"},
		{Prefix: "r1", UpstreamHost: "artifactory.example.test"},
	}

	got, _, warnings := CargoRepoAwareConfig(27182, "r0", routes, cargoArtifactoryRepoConfig)

	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	if !strings.Contains(got, `[source.artifactory-remote]
registry = "sparse+https://artifactory.example.test/artifactory/api/cargo/remote/index/"
replace-with = "spindrift-registry-proxy-r1-artifactory-remote"`) {
		t.Errorf("CargoRepoAwareConfig() content = %q, want the repo-claimed source name to chain onto the minted per-registry proxy source", got)
	}
	if !strings.Contains(got, `[source.spindrift-registry-proxy-r1-artifactory-remote]
registry = "sparse+http://127.0.0.1:27182/r1/artifactory/api/cargo/remote/index/"`) {
		t.Errorf("CargoRepoAwareConfig() content = %q, want the minted proxy source to terminate at the registry's own local index URL", got)
	}
}

func TestCargoConfigTOML_ExactContent(t *testing.T) {
	got := CargoConfigTOML(27182, "r0", nil)
	want := `[source.crates-io]
replace-with = "spindrift-registry-proxy"

[source.spindrift-registry-proxy]
registry = "sparse+http://127.0.0.1:27182/r0/"
`
	if got != want {
		t.Errorf("CargoConfigTOML(27182, %q) = %q, want %q", "r0", got, want)
	}
}

func TestCargoConfigTOML_PortInterpolated(t *testing.T) {
	for _, port := range []int{9999, 12345} {
		got := CargoConfigTOML(port, "r0", nil)
		want := fmt.Sprintf(`[source.crates-io]
replace-with = "spindrift-registry-proxy"

[source.spindrift-registry-proxy]
registry = "sparse+http://127.0.0.1:%d/r0/"
`, port)
		if got != want {
			t.Errorf("CargoConfigTOML(%d, %q) = %q, want %q", port, "r0", got, want)
		}
	}
}

// The route prefix, not just the port, lands in the rendered registry URL
// (issue #3142).
func TestCargoConfigTOML_PrefixInterpolated(t *testing.T) {
	got := CargoConfigTOML(27182, "artifactory-cargo", nil)
	want := `[source.crates-io]
replace-with = "spindrift-registry-proxy"

[source.spindrift-registry-proxy]
registry = "sparse+http://127.0.0.1:27182/artifactory-cargo/"
`
	if got != want {
		t.Errorf("CargoConfigTOML(27182, %q) = %q, want %q", "artifactory-cargo", got, want)
	}
}

func TestRouteLocalURL(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		want   string
	}{
		{
			name:   "prefixed route",
			prefix: "r0",
			want:   "http://127.0.0.1:27182/r0",
		},
		{
			name:   "empty prefix",
			prefix: "",
			want:   "http://127.0.0.1:27182/",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RouteLocalURL(tc.prefix, 27182)
			if got != tc.want {
				t.Errorf("RouteLocalURL(%q, 27182) = %q, want %q", tc.prefix, got, tc.want)
			}
		})
	}
}

func TestCargoRegistryEnvVarName(t *testing.T) {
	cases := []struct {
		name         string
		registryName string
		want         string
	}{
		{
			name:         "plain name",
			registryName: "othercorp",
			want:         "CARGO_REGISTRIES_OTHERCORP_TOKEN",
		},
		{
			name:         "dashed name maps dashes to underscores",
			registryName: "my-registry",
			want:         "CARGO_REGISTRIES_MY_REGISTRY_TOKEN",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CargoRegistryEnvVarName(tc.registryName)
			if got != tc.want {
				t.Errorf("CargoRegistryEnvVarName(%q) = %q, want %q", tc.registryName, got, tc.want)
			}
		})
	}
}

func TestParseCargoRegistryConfig_SingleRegistrySparseIndex(t *testing.T) {
	content := `
[registries.mycorp]
index = "sparse+https://cargo.example.com/index/"
`
	decls, namedAny, err := cargoRow.ConfigParser(content)
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if !namedAny {
		t.Error("namedAny = false, want true")
	}
	if len(decls) != 1 {
		t.Fatalf("decls = %+v, want exactly 1", decls)
	}
	want := Declaration{Host: "cargo.example.com", UpstreamBaseURL: "https://cargo.example.com/index", RegistryName: "mycorp"}
	if decls[0] != want {
		t.Errorf("decls[0] = %+v, want %+v", decls[0], want)
	}
}

// A config with no [registries.*] table at all yields namedAny false, which is
// distinct from "named but unusable".
func TestParseCargoRegistryConfig_NoRegistryTableYieldsNamedAnyFalse(t *testing.T) {
	content := `
[net]
git-fetch-with-cli = true
`
	decls, namedAny, err := cargoRow.ConfigParser(content)
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 0 {
		t.Fatalf("decls = %+v, want none", decls)
	}
	if namedAny {
		t.Error("namedAny = true, want false (no [registries.*] table at all)")
	}
}

// Unparseable TOML must come back as an error, not a silently empty result.
func TestParseCargoRegistryConfig_MalformedTOMLIsError(t *testing.T) {
	_, _, err := cargoRow.ConfigParser("this is not [ valid toml")
	if err == nil {
		t.Fatal("ConfigParser: expected error for malformed TOML, got nil")
	}
}

// A "file://" index URL is skipped, since it is not an absolute http(s) URL,
// but still sets namedAny true: the file named a registry, just not a usable
// one.
func TestParseCargoRegistryConfig_FileSchemeIndexNamedButUnusable(t *testing.T) {
	content := `
[registries.mirror]
index = "file:///srv/mirror"
`
	decls, namedAny, err := cargoRow.ConfigParser(content)
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 0 {
		t.Fatalf("decls = %+v, want none", decls)
	}
	if !namedAny {
		t.Error("namedAny = false, want true (a non-http index must not read as \"named nothing\")")
	}
}

// Multiple [registries.NAME] tables come back sorted by registry name,
// independent of their order in the file, because map iteration is randomized.
func TestParseCargoRegistryConfig_TwoRegistriesSortedByName(t *testing.T) {
	content := `
[registries.zeta]
index = "sparse+https://zeta.example.com/index/"

[registries.alpha]
index = "sparse+https://alpha.example.com/index/"
`
	decls, _, err := cargoRow.ConfigParser(content)
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 2 {
		t.Fatalf("decls = %+v, want exactly 2", decls)
	}
	if decls[0].RegistryName != "alpha" || decls[1].RegistryName != "zeta" {
		t.Errorf("decls order = [%s, %s], want [alpha, zeta]", decls[0].RegistryName, decls[1].RegistryName)
	}
}

// The pure-hook contract: the parser never sets Ecosystem or ConfigPath, since
// stamping both is the walker's job.
func TestParseCargoRegistryConfig_NeverStampsEcosystemOrConfigPath(t *testing.T) {
	content := `
[registries.mycorp]
index = "sparse+https://cargo.example.com/index/"
`
	decls, _, err := cargoRow.ConfigParser(content)
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("decls = %+v, want exactly 1", decls)
	}
	if decls[0].Ecosystem != "" || decls[0].ConfigPath != "" {
		t.Errorf("decls[0] = %+v, want Ecosystem and ConfigPath both unset", decls[0])
	}
}

func TestParseCargoRegistryConfig_UnrelatedKeyIgnored(t *testing.T) {
	content := `
[registries.mycorp]
token = "secret"
`
	decls, namedAny, err := cargoRow.ConfigParser(content)
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 0 {
		t.Fatalf("decls = %+v, want none (no index key)", decls)
	}
	if !namedAny {
		t.Error("namedAny = false, want true (a [registries.*] table was still named)")
	}
}
