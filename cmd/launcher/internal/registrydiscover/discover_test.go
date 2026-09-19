package registrydiscover

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
)

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
	lookup := func(store Store, d ecosystem.Declaration) (bool, error) {
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

// Routes come back in extraction order, so these assertions are index-keyed.
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
	lookup := func(store Store, d ecosystem.Declaration) (bool, error) { return false, nil }
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
	lookup := func(store Store, d ecosystem.Declaration) (bool, error) { return false, nil }
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
	lookup := func(store Store, d ecosystem.Declaration) (bool, error) { return true, nil }
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

// A cargo-credentials lookup keys on RegistryName, so a declaration carrying
// none must skip that store entirely.
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
	lookup := func(store Store, d ecosystem.Declaration) (bool, error) {
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
	// cargo-credentials is still named as searched even though its lookup never
	// ran: it was configured and considered, just found inapplicable. A report
	// line must never name no stores at all.
	wantSearched := []string{"cargo-credentials", "netrc"}
	got := report.Unmatched[0].StoresSearched
	if len(got) != 2 || got[0] != wantSearched[0] || got[1] != wantSearched[1] {
		t.Errorf("report.Unmatched[0].StoresSearched = %v, want %v", got, wantSearched)
	}
}

func TestDiscover_CargoCredentialsOnlyStoreList_NamesStoreForNonCargoDeclaration(t *testing.T) {
	dir := t.TempDir()
	npmrc := "registry=https://npm.example.com/\n"
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// The only configured store is skipped as inapplicable, leaving nothing
	// else to search. The report must still name it rather than print an empty
	// "searched" list.
	stores := []Store{{Name: "cargo-credentials", Path: "/home/agent/.cargo/credentials.toml"}}
	lookup := func(store Store, d ecosystem.Declaration) (bool, error) {
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

func TestDiscover_NilStores_UnmatchedHostSearchedIsEmpty(t *testing.T) {
	dir := t.TempDir()
	npmrc := "registry=https://npm.example.com/\n"
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	lookup := func(store Store, d ecosystem.Declaration) (bool, error) {
		t.Fatalf("lookup called with no stores configured: %+v", d)
		return false, nil
	}
	probe := func(upstreamBaseURL string) string { return "bearer" }

	// An empty stores list has nothing to name, so unlike the
	// configured-but-inapplicable case above this empty result is correct, not
	// a firstMatch bug. The command layer owns the fix for the "empty report
	// line" symptom: it must never pass empty stores when a home directory is
	// available (see TestCmdRegistryDiscover_HomeDirUnavailable_ErrorsInsteadOfProceeding).
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

func TestDiscover_NoteFlowsToReportNoRegistry(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".cargo"), 0o755); err != nil {
		t.Fatal(err)
	}
	cargoConfig := "[net]\ngit-fetch-with-cli = true\n"
	if err := os.WriteFile(filepath.Join(dir, ".cargo", "config.toml"), []byte(cargoConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	lookup := func(store Store, d ecosystem.Declaration) (bool, error) { return false, nil }
	probe := func(upstreamBaseURL string) string { return "bearer" }

	routes, report, err := Discover(dir, nil, lookup, probe)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	if len(routes) != 0 {
		t.Errorf("routes = %+v, want none", routes)
	}
	want := []ecosystem.Note{{ConfigPath: ".cargo/config.toml", Ecosystem: "cargo"}}
	if len(report.NoRegistry) != 1 || report.NoRegistry[0] != want[0] {
		t.Errorf("report.NoRegistry = %+v, want %+v", report.NoRegistry, want)
	}
}

// A port-only registry URL normalizes to an empty host, and registryroutes.Parse
// rejects a route with an empty MatchHost (see runRegistryDiscover's invariant in
// registrydiscover.go). Such a URL must be reported the way Extract reports any
// other unusable one.
func TestDiscover_PortOnlyHostYieldsNoRouteReportedAsSkipped(t *testing.T) {
	dir := t.TempDir()
	npmrc := "registry=http://:8080/\n"
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	lookup := func(store Store, d ecosystem.Declaration) (bool, error) { return false, nil }
	probe := func(upstreamBaseURL string) string { return "bearer" }

	routes, report, err := Discover(dir, nil, lookup, probe)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	if len(routes) != 0 {
		t.Fatalf("routes = %+v, want none (a port-only host must never become a route)", routes)
	}
	want := []ecosystem.Note{{ConfigPath: ".npmrc", Ecosystem: "npm", Skipped: true}}
	if len(report.NoRegistry) != 1 || report.NoRegistry[0] != want[0] {
		t.Errorf("report.NoRegistry = %+v, want %+v", report.NoRegistry, want)
	}
}

// envPlaceholder maps every non-alphanumeric byte to "_", so a host with a "."
// and a host with a "-" in the same position collide onto one env var name, and
// a token an operator sets for one would silently apply to the other. Both hosts
// here go unmatched and must get distinct, deterministic CredentialValue names.
func TestDiscover_CollidingEnvPlaceholdersDisambiguated(t *testing.T) {
	dir := t.TempDir()
	npmrc := "registry=https://a.b.example.com/\n@scoped:registry=https://a-b.example.com/\n"
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	lookup := func(store Store, d ecosystem.Declaration) (bool, error) { return false, nil }
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

	// A second run over the same fixture confirms the names are stable across
	// runs; it does not by itself show independence from declaration order or
	// map iteration order, since neither the input nor its order changes here.
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

func TestDiscover_ThreeWayEnvPlaceholderCollisionDisambiguated(t *testing.T) {
	dir := t.TempDir()
	// The third host's base placeholder name is byte-identical to the first
	// host's post-suffix name, so this only regresses if disambiguation
	// checks suffixed names against the full table.
	const firstHost = "a.b.example.com"
	const secondHost = "a-b.example.com"
	const thirdHost = "a.b.example.com-faf0ca2b"
	if got, want := envPlaceholder(secondHost), envPlaceholder(firstHost); got != want {
		t.Fatalf("fixture premise broken: envPlaceholder(%q) = %q, want %q (= envPlaceholder(%q)); "+
			"the first two fixture hosts no longer fold to one placeholder name and need updating", secondHost, got, want, firstHost)
	}
	if got, want := envPlaceholder(thirdHost), envPlaceholder(firstHost)+"_"+hostHash(firstHost); got != want {
		t.Fatalf("fixture premise broken: envPlaceholder(%q) = %q, want %q (= envPlaceholder(%q)+\"_\"+hostHash(%q)); "+
			"the fixture hosts below no longer exercise the collision and need updating", thirdHost, got, want, firstHost, firstHost)
	}
	npmrc := fmt.Sprintf("registry=https://%s/\n"+
		"@scope1:registry=https://%s/\n"+
		"@scope2:registry=https://%s/\n", firstHost, secondHost, thirdHost)
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	lookup := func(store Store, d ecosystem.Declaration) (bool, error) { return false, nil }
	probe := func(upstreamBaseURL string) string { return "bearer" }

	routes, _, err := Discover(dir, nil, lookup, probe)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	if len(routes) != 3 {
		t.Fatalf("routes = %+v, want exactly 3", routes)
	}

	byHost := make(map[string]string, len(routes))
	for _, r := range routes {
		byHost[r.MatchHost] = r.CredentialValue
	}
	hosts := []string{firstHost, secondHost, thirdHost}
	// The third host's base name collides with the first host's own
	// post-suffix name, so the first host needs a second disambiguation
	// round on top of its first, hence the doubled suffix below.
	wantByHost := map[string]string{
		firstHost:  "SPINDRIFT_REGISTRY_CREDENTIAL_A_B_EXAMPLE_COM_FAF0CA2B_FAF0CA2B",
		secondHost: "SPINDRIFT_REGISTRY_CREDENTIAL_A_B_EXAMPLE_COM_38648AB0",
		thirdHost:  "SPINDRIFT_REGISTRY_CREDENTIAL_A_B_EXAMPLE_COM_FAF0CA2B_71C7F8AD",
	}
	seen := make(map[string]string, len(hosts))
	for _, h := range hosts {
		v, ok := byHost[h]
		if !ok {
			t.Fatalf("routes = %+v, missing host %q", routes, h)
		}
		if v != wantByHost[h] {
			t.Errorf("%s CredentialValue = %q, want %q", h, v, wantByHost[h])
		}
		if other, dup := seen[v]; dup {
			t.Errorf("hosts %q and %q got the same CredentialValue %q, want distinct names", h, other, v)
		}
		seen[v] = h
	}

	// A fixture declaring the same three hosts in a different line order,
	// including the relative order of a.b.example.com and a-b.example.com
	// (each host keeping its own scope key), confirms the names are
	// independent of declaration order, not merely repeatable across
	// identical re-runs of the same fixture: a scheme that picks a
	// bucket's "winner" by first-seen order would pass a same-order
	// re-run but fail here.
	dir2 := t.TempDir()
	npmrc2 := fmt.Sprintf("@scope1:registry=https://%s/\n"+
		"@scope2:registry=https://%s/\n"+
		"registry=https://%s/\n", secondHost, thirdHost, firstHost)
	if err := os.WriteFile(filepath.Join(dir2, ".npmrc"), []byte(npmrc2), 0o644); err != nil {
		t.Fatal(err)
	}
	routes2, _, err := Discover(dir2, nil, lookup, probe)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	byHost2 := make(map[string]string, len(routes2))
	for _, r := range routes2 {
		byHost2[r.MatchHost] = r.CredentialValue
	}
	for _, h := range hosts {
		if byHost2[h] != byHost[h] {
			t.Errorf("%s CredentialValue changed under permuted declaration order: %q vs %q", h, byHost2[h], byHost[h])
		}
	}
}

func TestDisambiguateEnvPlaceholders_BoundExhaustedNamesLexicallySmallest(t *testing.T) {
	// Two independent pairs of hosts that genuinely collide under hostHash
	// (found by brute force, not stubbed): each pair shares one
	// CredentialValue, so the round bound trips for real, with no injected
	// hash. The error must deterministically name the lexicographically
	// smaller of the two contested names, not whichever name Go's
	// randomized map iteration visits first.
	const aHost1, aHost2 = "host-322383.example.com", "host-139598.example.com"
	const bHost1, bHost2 = "host-322382.example.com", "host-139599.example.com"
	if hostHash(aHost1) != hostHash(aHost2) {
		t.Fatalf("fixture premise broken: hostHash(%q) = %q, hostHash(%q) = %q; "+
			"these fixture hosts no longer collide and need updating", aHost1, hostHash(aHost1), aHost2, hostHash(aHost2))
	}
	if hostHash(bHost1) != hostHash(bHost2) {
		t.Fatalf("fixture premise broken: hostHash(%q) = %q, hostHash(%q) = %q; "+
			"these fixture hosts no longer collide and need updating", bHost1, hostHash(bHost1), bHost2, hostHash(bHost2))
	}

	routes := []Route{
		{MatchHost: bHost1, CredentialSource: "env", CredentialValue: "SPINDRIFT_REGISTRY_CREDENTIAL_B"},
		{MatchHost: bHost2, CredentialSource: "env", CredentialValue: "SPINDRIFT_REGISTRY_CREDENTIAL_B"},
		{MatchHost: aHost1, CredentialSource: "env", CredentialValue: "SPINDRIFT_REGISTRY_CREDENTIAL_A"},
		{MatchHost: aHost2, CredentialSource: "env", CredentialValue: "SPINDRIFT_REGISTRY_CREDENTIAL_A"},
	}

	err := disambiguateEnvPlaceholders(routes)
	if err == nil {
		t.Fatalf("disambiguateEnvPlaceholders: want error on unresolvable collision, got nil (routes = %+v)", routes)
	}
	if !strings.Contains(err.Error(), "SPINDRIFT_REGISTRY_CREDENTIAL_A") {
		t.Errorf("error %q does not name the lexicographically smallest colliding placeholder", err.Error())
	}
	if strings.Contains(err.Error(), "SPINDRIFT_REGISTRY_CREDENTIAL_B") {
		t.Errorf("error %q names the larger colliding placeholder instead of the smallest", err.Error())
	}
}

// Discover must surface a disambiguation failure as an error, not hand back a
// routes table in which two hosts share one env var. The sibling bound test
// above exercises the error-message determinism against hand-built routes;
// this one proves Discover's own call site propagates that error rather than
// swallowing it.
func TestDiscover_BoundExhaustedPropagatesErrorAndReturnsNilRoutes(t *testing.T) {
	// A pair colliding twice over: same envPlaceholder (every label folds
	// to "A_") and, unlike any other fixture here, the same hostHash, so
	// no round of suffixing can separate them and the bound trips through
	// production code. Found by brute force over dot/dash variants of a
	// 19-label host, not stubbed; if either fold changes, re-run that
	// search rather than hand-editing the literals.
	const host1 = "a.a-a.a.a-a.a-a.a-a-a.a-a.a-a-a-a.a.a.example.com"
	const host2 = "a.a.a.a.a-a.a.a.a.a.a.a-a-a.a.a-a-a.a.example.com"
	if envPlaceholder(host1) != envPlaceholder(host2) {
		t.Fatalf("fixture premise broken: envPlaceholder(%q) = %q, envPlaceholder(%q) = %q; "+
			"these fixture hosts no longer fold to one placeholder name and need updating", host1, envPlaceholder(host1), host2, envPlaceholder(host2))
	}
	if hostHash(host1) != hostHash(host2) {
		t.Fatalf("fixture premise broken: hostHash(%q) = %q, hostHash(%q) = %q; "+
			"these fixture hosts no longer collide under hostHash and need updating", host1, hostHash(host1), host2, hostHash(host2))
	}

	dir := t.TempDir()
	npmrc := fmt.Sprintf("registry=https://%s/\n@scope1:registry=https://%s/\n", host1, host2)
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	lookup := func(store Store, d ecosystem.Declaration) (bool, error) { return false, nil }
	probe := func(upstreamBaseURL string) string { return "bearer" }

	routes, report, err := Discover(dir, nil, lookup, probe)
	if err == nil {
		t.Fatalf("Discover: want error on unresolvable env placeholder collision, got nil (routes = %+v)", routes)
	}
	// Both hosts get suffixed in lockstep, so the name still contested when
	// the bound trips is the base name carrying one suffix per round — the
	// base name alone would also match as a prefix and prove less.
	const wantContested = "SPINDRIFT_REGISTRY_CREDENTIAL_A_A_A_A_A_A_A_A_A_A_A_A_A_A_A_A_A_A_A_EXAMPLE_COM_C8995528_C8995528"
	if !strings.Contains(err.Error(), wantContested) {
		t.Errorf("Discover error %q does not name the still-contested placeholder %q", err.Error(), wantContested)
	}
	if routes != nil {
		t.Errorf("routes = %+v, want nil (Discover must not hand back a table where two hosts share one env var)", routes)
	}
	// Discover zeroes the report on this path too; pinning it keeps the
	// whole error-path return contract asserted, not just half of it.
	if len(report.Matched) != 0 || len(report.Unmatched) != 0 || len(report.NoRegistry) != 0 {
		t.Errorf("report = %+v, want the zero Report on the error path", report)
	}
}

func TestDiscover_ProbeGarbageFallsBackToBearer(t *testing.T) {
	dir := t.TempDir()
	npmrc := "registry=https://npm.example.com/\n"
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	lookup := func(store Store, d ecosystem.Declaration) (bool, error) { return false, nil }
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

func TestDiscover_ProbeBasicHonored(t *testing.T) {
	dir := t.TempDir()
	npmrc := "registry=https://npm.example.com/\n"
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	lookup := func(store Store, d ecosystem.Declaration) (bool, error) { return false, nil }
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

func TestDiscover_ProbeHeaderSchemeHonored(t *testing.T) {
	dir := t.TempDir()
	npmrc := "registry=https://npm.example.com/\n"
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	lookup := func(store Store, d ecosystem.Declaration) (bool, error) { return false, nil }
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

	lookup := func(store Store, d ecosystem.Declaration) (bool, error) { return false, nil }
	probe := func(upstreamBaseURL string) string { return "bearer" }

	routes, _, err := Discover(dir, nil, lookup, probe)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("routes = %+v, want exactly 1", routes)
	}
	// Extract's table order puts npm before yarn, so the npm declaration's
	// upstream URL wins for the shared host.
	if routes[0].UpstreamBaseURL != "https://shared.example.com/npm" {
		t.Errorf("routes[0].UpstreamBaseURL = %q, want the npm declaration's URL", routes[0].UpstreamBaseURL)
	}
}

// Route.RegistryName accompanies a cargo-credentials match only, so a cargo
// declaration matched to netrc must leave it empty.
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
	lookup := func(store Store, d ecosystem.Declaration) (bool, error) { return true, nil }
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

func TestDiscover_GradlePropertiesMatchSetsPropertyKeyToHost(t *testing.T) {
	dir := t.TempDir()
	npmrc := "registry=https://gradle.example.com/\n"
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	stores := []Store{{Name: "gradle-properties", Path: "/home/agent/gradle.properties"}}
	lookup := func(store Store, d ecosystem.Declaration) (bool, error) { return true, nil }
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

// registryvocab.HostKey normalizes host case and the default port, which is what
// makes these two declarations dedupe onto one route.
func TestDiscover_NormalizedHostDedupesCaseAndPort(t *testing.T) {
	dir := t.TempDir()
	npmrc := "registry=https://Shared.Example.com:8443/npm/\n@myorg:registry=https://shared.example.com/other/\n"
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	lookup := func(store Store, d ecosystem.Declaration) (bool, error) { return false, nil }
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
