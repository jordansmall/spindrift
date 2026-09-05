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

// TestParseCargoSourceDecls_ReplaceWithOnlyNoDecl covers the repro's own
// [source.crates-io] shape: a stanza carrying only replace-with (no
// registry key) claims no URL and must yield no decl.
func TestParseCargoSourceDecls_ReplaceWithOnlyNoDecl(t *testing.T) {
	content := `[source.crates-io]
replace-with = "artifactory-remote"
`
	got := ParseCargoSourceDecls(content)
	if len(got) != 0 {
		t.Errorf("ParseCargoSourceDecls() = %+v, want empty", got)
	}
}

// TestParseCargoSourceDecls_HostileNameSkipped mirrors
// TestParseCargoRegistryDecls_HostileNameSkipped: a quoted TOML key must
// never reach a caller that turns it into a TOML table name.
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

// TestParseCargoSourceDecls_DedupedFirstWins covers a name repeated across
// two [source.NAME] headers: the first occurrence wins, mirroring
// ParseCargoRegistryDecls' contract.
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

// TestParseCargoSourceDecls_RegistriesTableNotMistaken covers a
// [registries.NAME] table with an index key: it must never be mistaken for
// a [source.NAME] table, even though both kinds of table can appear in the
// same repoConfig.
func TestParseCargoSourceDecls_RegistriesTableNotMistaken(t *testing.T) {
	content := `[registries.othercorp]
index = "sparse+https://cargo.example.test/index/"
`
	got := ParseCargoSourceDecls(content)
	if len(got) != 0 {
		t.Errorf("ParseCargoSourceDecls() = %+v, want empty", got)
	}
}

// TestCargoSourceReplacements_ReusesClaimingSourceName covers the issue's
// exact repro: the repo config's own [source.artifactory-remote] already
// claims the registries decl's index URL, so the upstream stanza must reuse
// that name instead of minting spindrift-upstream-artifactory-remote --
// minting a second [source.…] stanza on the same URL is a hard cargo error
// ("source ... already defined by ...").
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

// TestCargoSourceReplacements_NoClaimingSourceKeepsMintedName is the AC 2
// regression guard: a repo with only [registries.*] tables and no
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

// TestCargoSourceReplacements_GuardedNamesFallBackToMinted covers a source
// stanza whose name equals a table the home render already owns
// (crates-io, spindrift-registry-proxy, or a per-route
// spindrift-registry-proxy-<prefix>-<name>): reusing any of those would emit a
// duplicate TOML table within one file, so the minting site must fall back
// to the pre-existing minted name -- including its pre-existing collision --
// rather than drop the upstream.
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

// TestCargoSourceReplacements_RepoSourceNamedAfterMintedUpstreamIsGuarded
// covers the third class of home-owned name: a minted
// spindrift-upstream-<name> the render might itself emit. A repo
// [source.spindrift-upstream-b]
// claiming decl a's index URL used to make decl a's minting site adopt that
// same name, and decl b's own default mint is that same name too -- two
// upstreams sharing one SourceName render as the same [source.…] table
// twice in one file, a duplicate-table TOML error. Guarding every minted
// name up front makes the claim fall back to decl a's own mint instead.
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

	// The repo's own [source.spindrift-upstream-b] still collides on
	// aURL with home's now-separately-minted [source.spindrift-upstream-a]
	// once merged -- assertNoDuplicateCargoSourceTables's cross-file URL
	// check (gotcha 1) would flag that, but it is not this fix's target: a
	// repo that squats a reserved spindrift-upstream-<name> table under a
	// mismatched URL is already self-contradictory, and no reuse choice
	// here can make it mergeable. This fix's contract is narrower -- the
	// rendered home file alone must never carry the same [source.…] name
	// twice, the hard TOML parse error the finding names.
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
	want := CargoConfigTOML(27182, "r0", nil)
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
	if value, ok := ExportValue(got, "CARGO_REGISTRIES_SPINDRIFT_REGISTRY_PROXY_R1_TOKEN"); !ok || value != CargoPlaceholderToken {
		t.Errorf("ExportValue(r1 token) = (%q, %v), want (%q, true)", value, ok, CargoPlaceholderToken)
	}
	if value, ok := ExportValue(got, "CARGO_REGISTRIES_SPINDRIFT_REGISTRY_PROXY_R2_TOKEN"); !ok || value != CargoPlaceholderToken {
		t.Errorf("ExportValue(r2 token) = (%q, %v), want (%q, true)", value, ok, CargoPlaceholderToken)
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

// assertNoDuplicateCargoSourceTables pins AC 1 (issue #3248): cargo's
// [source.…] tables map a table name to a registry URL 1:1, so (a) homeConfig
// -- the rendered $CARGO_HOME/config.toml alone -- must never declare the
// same table name twice (a same-file duplicate table is a TOML error, not a
// merge question), and (b) once merged with the repo's own repoConfig, no
// two distinct table names may claim the same registry URL (cargo's
// URL->name uniqueness, gotcha 1). A [source.…] stanza with no registry key
// (e.g. crates-io's replace-with-only shape) claims no URL and is exempt
// from (b): the repo overriding home's own [source.crates-io] table is
// cargo's ordinary hierarchical config merge, not a URL collision.
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

// TestCargoRepoAwareConfig_RepoClaimingSourceNameRendersMergeableConfig is
// the issue's end-to-end repro: CargoRepoAwareConfig on a route whose
// UpstreamHost matches the repo-claimed registry, on a prefix other than the
// crates-io prefix, must reuse the repo's own [source.artifactory-remote]
// name rather than mint [source.spindrift-upstream-artifactory-remote] --
// the mint would be a second stanza on the same URL, which cargo rejects.
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
	// stanza -- the mint is the whole defect this issue fixes.
	if strings.Contains(got, "spindrift-upstream-") {
		t.Errorf("CargoRepoAwareConfig() content = %q, want no spindrift-upstream- table at all", got)
	}
}

// TestCargoRepoAwareConfig_TwoRegistriesOnOneIndexPath pins the render for
// two declared registries whose index URLs differ only in scheme: both
// resolve to the same local index URL, so the second reuses the proxy source
// the first minted, and the shared [source....]/[registries....] pair is
// rendered once. Emitting it per replacement instead would declare the same
// table name twice in one file -- a TOML error cargo refuses to parse at
// all, not a merge question.
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

	// Both upstream stanzas still render -- deduping the shared pair must not
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

// TestCargoRepoAwareConfig_NoDuplicateSourceTablesAcrossMerge is the AC 1
// regression: run assertNoDuplicateCargoSourceTables (a) over the repro
// case, where reuse is exercised, and (b) over a repo with no claiming
// [source.*] stanza, where the minted name is exercised instead -- the
// no-duplicate contract must hold either way the source name was chosen.
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

// TestCargoRepoAwareConfig_RepoCratesIOReplaceWithChainsToTheNamedRegistryRoute
// pins the emergent crates-io chain (issue #3248, gotcha 7) as an intended
// contract, not an accident: the repo's own [source.crates-io] replace-with
// = "artifactory-remote" overrides the home render's [source.crates-io]
// replace-with = "spindrift-registry-proxy" (in-tree config wins cargo's
// hierarchical merge), so a plain crates-io dependency chains
// crates-io -> artifactory-remote -> spindrift-registry-proxy-r1-artifactory-remote -> the r1
// Forwarder URL. That is the named-registry route's own prefix, not the
// crates-io route's (r0) -- plain crates-io traffic is meant to bypass the
// crates-io route entirely and ride the corporate route instead, because the
// repo declared crates-io served from that corporate remote.
func TestCargoRepoAwareConfig_RepoCratesIOReplaceWithChainsToTheNamedRegistryRoute(t *testing.T) {
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "crates.io"},
		{Prefix: "r1", UpstreamHost: "artifactory.example.test"},
	}

	got, _, _ := CargoRepoAwareConfig(27182, "r0", routes, cargoArtifactoryRepoConfig)

	// Link 1: the repo's own file overrides the home render's crates-io
	// replace-with -- asserted on repoConfig, since that link is cargo's
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
	// argument) -- the bypass landing on the *named-registry route's* prefix
	// is exactly the contract being pinned here.
	if !strings.Contains(got, "[source.spindrift-registry-proxy-r1-artifactory-remote]\nregistry = \"sparse+http://127.0.0.1:27182/r1/artifactory/api/cargo/remote/index/\"\n") {
		t.Errorf("CargoRepoAwareConfig() content = %q, want spindrift-registry-proxy-r1-artifactory-remote to terminate at the r1 Forwarder URL", got)
	}
}

// TestCargoRepoAwareConfig_NoSourceClaimMintsUnchanged is AC 2's end-to-end
// pin: a repo with only [registries.*] tables (no claiming [source.*]) must
// render the pre-#3248 minted spindrift-upstream-<name> output byte-for-byte.
// TestCargoSourceReplacements_NoClaimingSourceKeepsMintedName already pins
// the plan (SourceName stays minted) and
// TestCargoConfigTOMLWithReplacements_OneNamedRegistry already pins this
// exact byte output from a hand-built plan; this test is the missing link
// between them -- the same repoConfig text, run through the full
// CargoRepoAwareConfig entry point, must reach that same golden.
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

// TestCargoSourceReplacements_TwoRegistriesDistinctLocalURLs is
// issue #3256's headline acceptance criterion at the plan level: a
// route with two registries on its host must not fold them onto
// one local index URL (the legacy per-route grouping's bug) -- each
// registry's own upstream index path carries into its own local URL and its
// own minted proxy source, so the Forwarder's per-registry enforced subtree
// (issue #3256's derived path-set) has a URL to key off of.
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

// TestCargoSourceReplacements_NoIndexPathDegradesToRoutePrefixURL
// covers a host-rooted registry whose index carries no path at all (e.g. the
// host serves the index at its root): the per-registry local URL then
// degrades to the same shape a legacy route would render, since there is no
// path to embed.
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

// TestCargoSourceReplacements_ReusesCratesIOSourceWhenLocalURLsCoincide
// covers the one case a host-rooted registry's minted local URL can still
// collide with the crates-io replacement's own: a pathless index on a route
// sharing the crates-io render's own prefix. cargo's URL->source-name 1:1
// rule then requires reusing spindrift-registry-proxy rather than minting a
// second [source.…] stanza against the same URL.
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

// TestCargoConfigTOMLWithReplacements_TwoRegistries byte-pins the
// rendered content for a route's two registries: two distinct
// proxy-source/registries stanza pairs, one per registry, alongside their
// own upstream stanzas.
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

// TestCargoReplacementPlaceholders_TwoRegistries covers the
// placeholder side of the same two-registry plan: two distinct proxy
// sources must yield two distinct CARGO_REGISTRIES_<NAME>_TOKEN exports, not
// one folded export a shared local URL would have produced.
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

// TestCargoRepoAwareConfig_TwoRegistriesResolveThroughOneRoute is
// issue #3256's headline acceptance criterion end to end: a repo declaring
// two cargo registries on one host, routed through a single host-rooted
// route, must render two distinct proxy stanzas that each carry their own
// registry's real index URL -- not one route-wide fold-down.
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

// TestCargoRepoAwareConfig_CratesIOChainStillComposes covers issue
// #3248's crates-io-chained shape on a route: the repo replaces
// crates-io with its own named source, which this render must still chain
// onto the route's minted proxy source rather than treating host-rootedness
// as a reason to skip the reuse.
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
