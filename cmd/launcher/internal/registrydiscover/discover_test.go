package registrydiscover

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDiscover_CargoSingleRegistryMatchedRoute verifies that a single cargo
// registry declaration matched to a cargo-credentials store produces exactly
// one route (including RegistryName) and a matching report.Matched entry.
func TestDiscover_CargoSingleRegistryMatchedRoute(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".cargo"), 0o755); err != nil {
		t.Fatal(err)
	}
	cargoConfig := `
[registries.mycorp]
index = "sparse+https://cargo.example.com/index/"
`
	if err := os.WriteFile(filepath.Join(dir, ".cargo", "config.toml"), []byte(cargoConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	stores := []Store{{Name: "cargo-credentials", Path: "/home/agent/.cargo/credentials.toml"}}
	lookup := func(store Store, d Declared) (bool, error) {
		return store.Name == "cargo-credentials" && d.RegistryName == "mycorp", nil
	}
	probe := func(upstreamBaseURL string) string { return "bearer" }

	routes, report, err := Discover(dir, stores, lookup, probe)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("routes = %+v, want exactly 1", routes)
	}
	want := Route{
		MatchHost:        "cargo.example.com",
		UpstreamBaseURL:  "https://cargo.example.com/index",
		AuthScheme:       "bearer",
		CredentialSource: "cargo-credentials",
		CredentialValue:  "/home/agent/.cargo/credentials.toml",
		RegistryName:     "mycorp",
	}
	if routes[0] != want {
		t.Errorf("routes[0] = %+v, want %+v", routes[0], want)
	}
	wantMatched := []MatchedHost{{Host: "cargo.example.com", StoreName: "cargo-credentials", StorePath: "/home/agent/.cargo/credentials.toml"}}
	if len(report.Matched) != 1 || report.Matched[0] != wantMatched[0] {
		t.Errorf("report.Matched = %+v, want %+v", report.Matched, wantMatched)
	}
	if len(report.Unmatched) != 0 {
		t.Errorf("report.Unmatched = %+v, want none", report.Unmatched)
	}
	if len(report.NoRegistry) != 0 {
		t.Errorf("report.NoRegistry = %+v, want none", report.NoRegistry)
	}
}

// TestDiscover_TwoHostsAcrossFilesTwoRoutes verifies that declarations from
// two different config files (cargo, npm) naming two distinct hosts each
// produce their own route, in extraction order.
func TestDiscover_TwoHostsAcrossFilesTwoRoutes(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".cargo"), 0o755); err != nil {
		t.Fatal(err)
	}
	cargoConfig := `
[registries.mycorp]
index = "sparse+https://cargo.example.com/index/"
`
	if err := os.WriteFile(filepath.Join(dir, ".cargo", "config.toml"), []byte(cargoConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	npmrc := "registry=https://npm.example.com/\n"
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	stores := []Store{{Name: "netrc", Path: "/home/agent/.netrc"}}
	lookup := func(store Store, d Declared) (bool, error) { return false, nil }
	probe := func(upstreamBaseURL string) string { return "bearer" }

	routes, _, err := Discover(dir, stores, lookup, probe)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	if len(routes) != 2 {
		t.Fatalf("routes = %+v, want exactly 2", routes)
	}
	if routes[0].MatchHost != "cargo.example.com" {
		t.Errorf("routes[0].MatchHost = %q, want cargo.example.com", routes[0].MatchHost)
	}
	if routes[1].MatchHost != "npm.example.com" {
		t.Errorf("routes[1].MatchHost = %q, want npm.example.com", routes[1].MatchHost)
	}
}

// TestDiscover_UnmatchedHostEnvPlaceholder verifies that a host no configured
// store matches gets an "env" CredentialSource with a derived placeholder
// name, and lands in report.Unmatched naming every store searched.
func TestDiscover_UnmatchedHostEnvPlaceholder(t *testing.T) {
	dir := t.TempDir()
	npmrc := "registry=https://crates.acme.example/\n"
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	stores := []Store{
		{Name: "netrc", Path: "/home/agent/.netrc"},
		{Name: "npmrc", Path: "/home/agent/.npmrc"},
	}
	lookup := func(store Store, d Declared) (bool, error) { return false, nil }
	probe := func(upstreamBaseURL string) string { return "bearer" }

	routes, report, err := Discover(dir, stores, lookup, probe)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("routes = %+v, want exactly 1", routes)
	}
	if routes[0].CredentialSource != "env" {
		t.Errorf("routes[0].CredentialSource = %q, want env", routes[0].CredentialSource)
	}
	wantValue := "SPINDRIFT_REGISTRY_CREDENTIAL_CRATES_ACME_EXAMPLE"
	if routes[0].CredentialValue != wantValue {
		t.Errorf("routes[0].CredentialValue = %q, want %q", routes[0].CredentialValue, wantValue)
	}
	if len(report.Unmatched) != 1 {
		t.Fatalf("report.Unmatched = %+v, want exactly 1", report.Unmatched)
	}
	wantSearched := []string{"netrc", "npmrc"}
	got := report.Unmatched[0]
	if got.Host != "crates.acme.example" || len(got.StoresSearched) != 2 || got.StoresSearched[0] != wantSearched[0] || got.StoresSearched[1] != wantSearched[1] {
		t.Errorf("report.Unmatched[0] = %+v, want Host=crates.acme.example StoresSearched=%v", got, wantSearched)
	}
	if len(report.Matched) != 0 {
		t.Errorf("report.Matched = %+v, want none", report.Matched)
	}
}

// TestDiscover_StorePrecedenceEarlierStoreWins verifies that when multiple
// configured stores would all match a declaration, the earliest one in the
// stores slice wins the route's credential.
func TestDiscover_StorePrecedenceEarlierStoreWins(t *testing.T) {
	dir := t.TempDir()
	npmrc := "registry=https://npm.example.com/\n"
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	stores := []Store{
		{Name: "netrc", Path: "/home/agent/.netrc"},
		{Name: "npmrc", Path: "/home/agent/.npmrc"},
	}
	// Both stores would report a match; earlier in the slice must win.
	lookup := func(store Store, d Declared) (bool, error) { return true, nil }
	probe := func(upstreamBaseURL string) string { return "bearer" }

	routes, report, err := Discover(dir, stores, lookup, probe)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("routes = %+v, want exactly 1", routes)
	}
	if routes[0].CredentialSource != "netrc" || routes[0].CredentialValue != "/home/agent/.netrc" {
		t.Errorf("routes[0] credential = %q/%q, want netrc//home/agent/.netrc", routes[0].CredentialSource, routes[0].CredentialValue)
	}
	if len(report.Matched) != 1 || report.Matched[0].StoreName != "netrc" {
		t.Errorf("report.Matched = %+v, want netrc", report.Matched)
	}
}

// TestDiscover_CargoCredentialsSkippedWhenNoRegistryName verifies that a
// cargo-credentials store's lookup is never invoked for a non-cargo
// declaration (one with no RegistryName to key on), even though it's
// still named in the report as searched.
func TestDiscover_CargoCredentialsSkippedWhenNoRegistryName(t *testing.T) {
	dir := t.TempDir()
	npmrc := "registry=https://npm.example.com/\n"
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	stores := []Store{
		{Name: "cargo-credentials", Path: "/home/agent/.cargo/credentials.toml"},
		{Name: "netrc", Path: "/home/agent/.netrc"},
	}
	lookup := func(store Store, d Declared) (bool, error) {
		if store.Name == "cargo-credentials" {
			t.Fatalf("lookup called for cargo-credentials on a non-cargo declaration: %+v", d)
		}
		return false, nil
	}
	probe := func(upstreamBaseURL string) string { return "bearer" }

	routes, report, err := Discover(dir, stores, lookup, probe)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("routes = %+v, want exactly 1", routes)
	}
	if len(report.Unmatched) != 1 {
		t.Fatalf("report.Unmatched = %+v, want exactly 1", report.Unmatched)
	}
	// cargo-credentials is still named as searched even though its lookup was
	// never called -- it was configured and considered for this declaration,
	// just found inapplicable. A report line must never end up naming no
	// stores at all.
	wantSearched := []string{"cargo-credentials", "netrc"}
	got := report.Unmatched[0].StoresSearched
	if len(got) != 2 || got[0] != wantSearched[0] || got[1] != wantSearched[1] {
		t.Errorf("report.Unmatched[0].StoresSearched = %v, want %v", got, wantSearched)
	}
}

// TestDiscover_CargoCredentialsOnlyStoreList_NamesStoreForNonCargoDeclaration
// verifies that when cargo-credentials is the only configured store and the
// declaration is non-cargo, the report still names that store as searched
// rather than leaving StoresSearched empty.
func TestDiscover_CargoCredentialsOnlyStoreList_NamesStoreForNonCargoDeclaration(t *testing.T) {
	dir := t.TempDir()
	npmrc := "registry=https://npm.example.com/\n"
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// Only a cargo-credentials store is configured, but the declaration is
	// npm's -- cargo-credentials is skipped as inapplicable (no
	// RegistryName to key its lookup on), leaving nothing else to
	// search. The report must still name it rather than print an empty
	// "searched" list.
	stores := []Store{{Name: "cargo-credentials", Path: "/home/agent/.cargo/credentials.toml"}}
	lookup := func(store Store, d Declared) (bool, error) {
		t.Fatalf("lookup called for cargo-credentials on a non-cargo declaration: %+v", d)
		return false, nil
	}
	probe := func(upstreamBaseURL string) string { return "bearer" }

	_, report, err := Discover(dir, stores, lookup, probe)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	if len(report.Unmatched) != 1 {
		t.Fatalf("report.Unmatched = %+v, want exactly 1", report.Unmatched)
	}
	got := report.Unmatched[0].StoresSearched
	if len(got) != 1 || got[0] != "cargo-credentials" {
		t.Errorf("report.Unmatched[0].StoresSearched = %v, want [cargo-credentials]", got)
	}
}

// TestDiscover_NilStores_UnmatchedHostSearchedIsEmpty verifies that a genuine
// nil stores list (nothing configured at all) legitimately yields an empty
// StoresSearched, distinct from the configured-but-inapplicable-store case
// above.
func TestDiscover_NilStores_UnmatchedHostSearchedIsEmpty(t *testing.T) {
	dir := t.TempDir()
	npmrc := "registry=https://npm.example.com/\n"
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	lookup := func(store Store, d Declared) (bool, error) {
		t.Fatalf("lookup called with no stores configured: %+v", d)
		return false, nil
	}
	probe := func(upstreamBaseURL string) string { return "bearer" }

	// A genuinely empty stores list (e.g. the caller has none configured)
	// has nothing to name -- unlike the configured-but-inapplicable-store
	// case above, this is not a bug in firstMatch; the fix for the "empty
	// report line" symptom
	// this scenario used to cause is in the command layer, which must never
	// let stores be empty when a home directory is available (see
	// TestCmdRegistryDiscover_HomeDirUnavailable_ErrorsInsteadOfProceeding).
	_, report, err := Discover(dir, nil, lookup, probe)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	if len(report.Unmatched) != 1 {
		t.Fatalf("report.Unmatched = %+v, want exactly 1", report.Unmatched)
	}
	if len(report.Unmatched[0].StoresSearched) != 0 {
		t.Errorf("report.Unmatched[0].StoresSearched = %v, want empty", report.Unmatched[0].StoresSearched)
	}
}

// TestDiscover_NoteFlowsToReportNoRegistry checks that a config file present
// but naming no registry at all surfaces as a Report.NoRegistry entry.
func TestDiscover_NoteFlowsToReportNoRegistry(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".cargo"), 0o755); err != nil {
		t.Fatal(err)
	}
	cargoConfig := "[net]\ngit-fetch-with-cli = true\n"
	if err := os.WriteFile(filepath.Join(dir, ".cargo", "config.toml"), []byte(cargoConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	lookup := func(store Store, d Declared) (bool, error) { return false, nil }
	probe := func(upstreamBaseURL string) string { return "bearer" }

	routes, report, err := Discover(dir, nil, lookup, probe)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	if len(routes) != 0 {
		t.Errorf("routes = %+v, want none", routes)
	}
	want := []Note{{ConfigPath: ".cargo/config.toml", Ecosystem: "cargo"}}
	if len(report.NoRegistry) != 1 || report.NoRegistry[0] != want[0] {
		t.Errorf("report.NoRegistry = %+v, want %+v", report.NoRegistry, want)
	}
}

// TestDiscover_PortOnlyHostYieldsNoRouteReportedAsSkipped covers the finding
// that a port-only registry URL (e.g. .npmrc's "registry=http://:8080/")
// must never reach a Route: its normalized host is "", and a route with an
// empty MatchHost is exactly what registryroutes.Parse rejects (see
// runRegistryDiscover's invariant, registrydiscover.go). It must instead
// surface the same way Extract already reports any other unusable URL.
func TestDiscover_PortOnlyHostYieldsNoRouteReportedAsSkipped(t *testing.T) {
	dir := t.TempDir()
	npmrc := "registry=http://:8080/\n"
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	lookup := func(store Store, d Declared) (bool, error) { return false, nil }
	probe := func(upstreamBaseURL string) string { return "bearer" }

	routes, report, err := Discover(dir, nil, lookup, probe)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	if len(routes) != 0 {
		t.Fatalf("routes = %+v, want none (a port-only host must never become a route)", routes)
	}
	want := []Note{{ConfigPath: ".npmrc", Ecosystem: "npm", Skipped: true}}
	if len(report.NoRegistry) != 1 || report.NoRegistry[0] != want[0] {
		t.Errorf("report.NoRegistry = %+v, want %+v", report.NoRegistry, want)
	}
}

// TestDiscover_CollidingEnvPlaceholdersDisambiguated covers the finding
// that envPlaceholder maps every non-alphanumeric byte to "_", so distinct
// hosts differing only by "." vs "-" collide onto the same env var name --
// a token an operator sets for one host would silently also apply to the
// other. Both hosts here go unmatched and must end up with distinct,
// deterministic CredentialValue names.
func TestDiscover_CollidingEnvPlaceholdersDisambiguated(t *testing.T) {
	dir := t.TempDir()
	npmrc := "registry=https://a.b.example.com/\n@scoped:registry=https://a-b.example.com/\n"
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	lookup := func(store Store, d Declared) (bool, error) { return false, nil }
	probe := func(upstreamBaseURL string) string { return "bearer" }

	routes, _, err := Discover(dir, nil, lookup, probe)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	if len(routes) != 2 {
		t.Fatalf("routes = %+v, want exactly 2", routes)
	}

	const wantPrefix = "SPINDRIFT_REGISTRY_CREDENTIAL_A_B_EXAMPLE_COM_"
	byHost := make(map[string]string, len(routes))
	for _, r := range routes {
		byHost[r.MatchHost] = r.CredentialValue
	}
	dotValue, ok := byHost["a.b.example.com"]
	if !ok {
		t.Fatalf("routes = %+v, missing a.b.example.com", routes)
	}
	dashValue, ok := byHost["a-b.example.com"]
	if !ok {
		t.Fatalf("routes = %+v, missing a-b.example.com", routes)
	}
	if dotValue == dashValue {
		t.Errorf("colliding hosts got the same CredentialValue %q, want distinct names", dotValue)
	}
	if !strings.HasPrefix(dotValue, wantPrefix) {
		t.Errorf("a.b.example.com CredentialValue = %q, want prefix %q", dotValue, wantPrefix)
	}
	if !strings.HasPrefix(dashValue, wantPrefix) {
		t.Errorf("a-b.example.com CredentialValue = %q, want prefix %q", dashValue, wantPrefix)
	}

	// Re-run to confirm the disambiguated names are stable across calls
	// (i.e. keyed on the host string, not declaration order or map
	// iteration order).
	routes2, _, err := Discover(dir, nil, lookup, probe)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	byHost2 := make(map[string]string, len(routes2))
	for _, r := range routes2 {
		byHost2[r.MatchHost] = r.CredentialValue
	}
	if byHost2["a.b.example.com"] != dotValue {
		t.Errorf("a.b.example.com CredentialValue changed across runs: %q vs %q", byHost2["a.b.example.com"], dotValue)
	}
	if byHost2["a-b.example.com"] != dashValue {
		t.Errorf("a-b.example.com CredentialValue changed across runs: %q vs %q", byHost2["a-b.example.com"], dashValue)
	}
}

// TestDiscover_ProbeGarbageFallsBackToBearer verifies that a probe answer
// normalizeAuthScheme doesn't recognize falls back to "bearer" on the route.
func TestDiscover_ProbeGarbageFallsBackToBearer(t *testing.T) {
	dir := t.TempDir()
	npmrc := "registry=https://npm.example.com/\n"
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	lookup := func(store Store, d Declared) (bool, error) { return false, nil }
	probe := func(upstreamBaseURL string) string { return "not-a-real-scheme" }

	routes, _, err := Discover(dir, nil, lookup, probe)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("routes = %+v, want exactly 1", routes)
	}
	if routes[0].AuthScheme != "bearer" {
		t.Errorf("routes[0].AuthScheme = %q, want bearer", routes[0].AuthScheme)
	}
}

// TestDiscover_ProbeBasicHonored verifies that a probe answer of "basic" is
// carried onto the route's AuthScheme unchanged.
func TestDiscover_ProbeBasicHonored(t *testing.T) {
	dir := t.TempDir()
	npmrc := "registry=https://npm.example.com/\n"
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	lookup := func(store Store, d Declared) (bool, error) { return false, nil }
	probe := func(upstreamBaseURL string) string { return "basic" }

	routes, _, err := Discover(dir, nil, lookup, probe)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("routes = %+v, want exactly 1", routes)
	}
	if routes[0].AuthScheme != "basic" {
		t.Errorf("routes[0].AuthScheme = %q, want basic", routes[0].AuthScheme)
	}
}

// TestDiscover_ProbeHeaderSchemeHonored verifies that a valid
// "header:<Name>" probe answer is carried onto the route's AuthScheme
// unchanged.
func TestDiscover_ProbeHeaderSchemeHonored(t *testing.T) {
	dir := t.TempDir()
	npmrc := "registry=https://npm.example.com/\n"
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	lookup := func(store Store, d Declared) (bool, error) { return false, nil }
	probe := func(upstreamBaseURL string) string { return "header:X-JFrog-Art-Api" }

	routes, _, err := Discover(dir, nil, lookup, probe)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("routes = %+v, want exactly 1", routes)
	}
	if routes[0].AuthScheme != "header:X-JFrog-Art-Api" {
		t.Errorf("routes[0].AuthScheme = %q, want header:X-JFrog-Art-Api", routes[0].AuthScheme)
	}
}

// TestDiscover_SameHostTwoFilesOneRoute verifies that two config files
// declaring the same host dedupe to a single route, with the earlier
// extraction (npm before yarn) winning the upstream URL.
func TestDiscover_SameHostTwoFilesOneRoute(t *testing.T) {
	dir := t.TempDir()
	npmrc := "registry=https://shared.example.com/npm/\n"
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}
	yarnrc := "npmRegistryServer: https://shared.example.com/yarn/\n"
	if err := os.WriteFile(filepath.Join(dir, ".yarnrc.yml"), []byte(yarnrc), 0o644); err != nil {
		t.Fatal(err)
	}

	lookup := func(store Store, d Declared) (bool, error) { return false, nil }
	probe := func(upstreamBaseURL string) string { return "bearer" }

	routes, _, err := Discover(dir, nil, lookup, probe)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("routes = %+v, want exactly 1", routes)
	}
	// npm is extracted before yarn (Extract's table order), so the npm
	// declaration's upstream URL wins for the shared host.
	if routes[0].UpstreamBaseURL != "https://shared.example.com/npm" {
		t.Errorf("routes[0].UpstreamBaseURL = %q, want the npm declaration's URL", routes[0].UpstreamBaseURL)
	}
}

// TestDiscover_RegistryNameOnlySetForCargoCredentialsMatch verifies that a
// cargo declaration matched to a non-cargo-credentials store (netrc here)
// leaves Route.RegistryName empty -- that field is a cargo-credentials-only
// companion, not a general property of cargo declarations.
func TestDiscover_RegistryNameOnlySetForCargoCredentialsMatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".cargo"), 0o755); err != nil {
		t.Fatal(err)
	}
	cargoConfig := `
[registries.mycorp]
index = "sparse+https://cargo.example.com/index/"
`
	if err := os.WriteFile(filepath.Join(dir, ".cargo", "config.toml"), []byte(cargoConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	stores := []Store{{Name: "netrc", Path: "/home/agent/.netrc"}}
	lookup := func(store Store, d Declared) (bool, error) { return true, nil }
	probe := func(upstreamBaseURL string) string { return "bearer" }

	routes, _, err := Discover(dir, stores, lookup, probe)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("routes = %+v, want exactly 1", routes)
	}
	if routes[0].CredentialSource != "netrc" {
		t.Fatalf("routes[0].CredentialSource = %q, want netrc", routes[0].CredentialSource)
	}
	if routes[0].RegistryName != "" {
		t.Errorf("routes[0].RegistryName = %q, want empty (companion of cargo-credentials only)", routes[0].RegistryName)
	}
}

// TestDiscover_GradlePropertiesMatchSetsPropertyKeyToHost verifies that a
// declaration matched to a gradle-properties store gets Route.PropertyKey set
// to the normalized host.
func TestDiscover_GradlePropertiesMatchSetsPropertyKeyToHost(t *testing.T) {
	dir := t.TempDir()
	npmrc := "registry=https://gradle.example.com/\n"
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	stores := []Store{{Name: "gradle-properties", Path: "/home/agent/gradle.properties"}}
	lookup := func(store Store, d Declared) (bool, error) { return true, nil }
	probe := func(upstreamBaseURL string) string { return "bearer" }

	routes, _, err := Discover(dir, stores, lookup, probe)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("routes = %+v, want exactly 1", routes)
	}
	if routes[0].PropertyKey != "gradle.example.com" {
		t.Errorf("routes[0].PropertyKey = %q, want gradle.example.com", routes[0].PropertyKey)
	}
}

// TestDiscover_NormalizedHostDedupesCaseAndPort verifies that two
// declarations differing only in host case and an explicit default port
// dedupe to the same route via registryvocab.HostKey normalization.
func TestDiscover_NormalizedHostDedupesCaseAndPort(t *testing.T) {
	dir := t.TempDir()
	npmrc := "registry=https://Shared.Example.com:8443/npm/\n@myorg:registry=https://shared.example.com/other/\n"
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	lookup := func(store Store, d Declared) (bool, error) { return false, nil }
	probe := func(upstreamBaseURL string) string { return "bearer" }

	routes, _, err := Discover(dir, nil, lookup, probe)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("routes = %+v, want exactly 1 (case/port variants of the same host)", routes)
	}
	if routes[0].MatchHost != "shared.example.com" {
		t.Errorf("routes[0].MatchHost = %q, want shared.example.com", routes[0].MatchHost)
	}
}
