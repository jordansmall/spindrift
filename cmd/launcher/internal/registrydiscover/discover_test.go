package registrydiscover

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/credresolver"
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystemtest/synthetic"
	"spindrift.dev/launcher/internal/registryroutes"
	"spindrift.dev/launcher/internal/testutil"
)

func noMatch(Store, ecosystem.Declaration) (bool, error)     { return false, nil }
func alwaysMatch(Store, ecosystem.Declaration) (bool, error) { return true, nil }

func probeAs(scheme string) Probe {
	return func(string) ProbeResult { return ProbeResult{Scheme: scheme} }
}

// discoverSynthetic runs Discover over the synthetic Strategy alone.
func discoverSynthetic(t *testing.T, dir string, stores []Store, lookup Lookup, probe Probe) ([]Route, Report) {
	t.Helper()
	routes, report, err := Discover(dir, syntheticRows, stores, lookup, probe)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	return routes, report
}

func TestDiscover_SingleRegistryMatchedRoute(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFixture(t, dir, cfgPath, "main = https://registry.example.com/index/\n")

	stores := []Store{{Name: "netrc", Path: "/home/agent/.netrc"}}
	routes, report := discoverSynthetic(t, dir, stores, alwaysMatch, probeAs("bearer"))
	if len(routes) != 1 {
		t.Fatalf("routes = %+v, want exactly 1", routes)
	}
	want := Route{
		MatchHost:        "registry.example.com",
		UpstreamBaseURL:  "https://registry.example.com/index",
		AuthScheme:       "bearer",
		CredentialSource: "netrc",
		CredentialValue:  "/home/agent/.netrc",
	}
	if !reflect.DeepEqual(routes[0], want) {
		t.Errorf("routes[0] = %+v, want %+v", routes[0], want)
	}
	wantMatched := []MatchedHost{{Host: "registry.example.com", StoreName: "netrc", StorePath: "/home/agent/.netrc"}}
	if !slices.Equal(report.Matched, wantMatched) {
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
	twin := synthetic.Twin("synthetic-twin")
	testutil.WriteFixture(t, dir, cfgPath, "main = https://first.example.com/index/\n")
	testutil.WriteFixture(t, dir, twin.InTreeConfigPath, "main = https://second.example.com/\n")

	rows := []ecosystem.Strategy{syntheticRows[0], twin}
	stores := []Store{{Name: "netrc", Path: "/home/agent/.netrc"}}
	routes, _, err := Discover(dir, rows, stores, noMatch, probeAs("bearer"))
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	if len(routes) != 2 {
		t.Fatalf("routes = %+v, want exactly 2", routes)
	}
	if routes[0].MatchHost != "first.example.com" {
		t.Errorf("routes[0].MatchHost = %q, want first.example.com", routes[0].MatchHost)
	}
	if routes[1].MatchHost != "second.example.com" {
		t.Errorf("routes[1].MatchHost = %q, want second.example.com", routes[1].MatchHost)
	}
}

func TestDiscover_UnmatchedHostEnvPlaceholder(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFixture(t, dir, cfgPath, "main = https://registry.acme.example/\n")

	stores := []Store{
		{Name: "netrc", Path: "/home/agent/.netrc"},
		{Name: "other-store", Path: "/home/agent/other"},
	}
	routes, report := discoverSynthetic(t, dir, stores, noMatch, probeAs("bearer"))
	if len(routes) != 1 {
		t.Fatalf("routes = %+v, want exactly 1", routes)
	}
	if routes[0].CredentialSource != "env" {
		t.Errorf("routes[0].CredentialSource = %q, want env", routes[0].CredentialSource)
	}
	wantValue := "SPINDRIFT_REGISTRY_CREDENTIAL_REGISTRY_ACME_EXAMPLE"
	if routes[0].CredentialValue != wantValue {
		t.Errorf("routes[0].CredentialValue = %q, want %q", routes[0].CredentialValue, wantValue)
	}
	if len(report.Unmatched) != 1 {
		t.Fatalf("report.Unmatched = %+v, want exactly 1", report.Unmatched)
	}
	wantSearched := []string{"netrc", "other-store"}
	got := report.Unmatched[0]
	if got.Host != "registry.acme.example" || !slices.Equal(got.StoresSearched, wantSearched) {
		t.Errorf("report.Unmatched[0] = %+v, want Host=registry.acme.example StoresSearched=%v", got, wantSearched)
	}
	if len(report.Matched) != 0 {
		t.Errorf("report.Matched = %+v, want none", report.Matched)
	}
}

func TestDiscover_StorePrecedenceEarlierStoreWins(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFixture(t, dir, cfgPath, "main = https://registry.example.com/\n")

	stores := []Store{
		{Name: "netrc", Path: "/home/agent/.netrc"},
		{Name: "other-store", Path: "/home/agent/other"},
	}
	// Both stores would report a match; earlier in the slice must win.
	row := synthetic.Strategy
	row.CredentialStores = []string{"netrc", "other-store"}
	routes, report, err := Discover(dir, []ecosystem.Strategy{row}, stores, alwaysMatch, probeAs("bearer"))
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

// A store outside the declaration's Strategy is never queried, so its lookup must
// not run for that declaration.
func TestDiscover_StoreOutsideRowSkippedForDeclaration(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFixture(t, dir, cfgPath, "main = https://registry.example.com/\n")

	stores := []Store{
		{Name: "outside-store", Path: "/home/agent/outside"},
		{Name: "netrc", Path: "/home/agent/.netrc"},
	}
	lookup := func(store Store, d ecosystem.Declaration) (bool, error) {
		if store.Name == "outside-store" {
			t.Fatalf("lookup called for outside-store on a declaration outside its row: %+v", d)
		}
		return false, nil
	}

	_, report := discoverSynthetic(t, dir, stores, lookup, probeAs("bearer"))
	if len(report.Unmatched) != 1 {
		t.Fatalf("report.Unmatched = %+v, want exactly 1", report.Unmatched)
	}
	// outside-store is still named as searched even though its lookup never
	// ran: it was configured and considered, just found inapplicable. A report
	// line must never name no stores at all.
	wantSearched := []string{"outside-store", "netrc"}
	got := report.Unmatched[0].StoresSearched
	if !slices.Equal(got, wantSearched) {
		t.Errorf("report.Unmatched[0].StoresSearched = %v, want %v", got, wantSearched)
	}
}

func TestDiscover_OutsideRowOnlyStoreList_NamesStoreForDeclaration(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFixture(t, dir, cfgPath, "main = https://registry.example.com/\n")

	// The only configured store is skipped as inapplicable, leaving nothing
	// else to search. The report must still name it rather than print an empty
	// "searched" list.
	stores := []Store{{Name: "outside-store", Path: "/home/agent/outside"}}
	lookup := func(store Store, d ecosystem.Declaration) (bool, error) {
		t.Fatalf("lookup called for outside-store on a declaration outside its row: %+v", d)
		return false, nil
	}

	_, report := discoverSynthetic(t, dir, stores, lookup, probeAs("bearer"))
	if len(report.Unmatched) != 1 {
		t.Fatalf("report.Unmatched = %+v, want exactly 1", report.Unmatched)
	}
	got := report.Unmatched[0].StoresSearched
	if !slices.Equal(got, []string{"outside-store"}) {
		t.Errorf("report.Unmatched[0].StoresSearched = %v, want [outside-store]", got)
	}
}

// A declaration carrying a RegistryName must not widen which stores are
// queried: the Strategy's store set alone decides.
func TestFirstMatch_StoreOutsideRowSkippedForDeclarationWithRegistryName(t *testing.T) {
	stores := []Store{
		{Name: "outside-store", Path: "/home/agent/outside"},
		{Name: "netrc", Path: "/home/agent/.netrc"},
	}
	lookup := func(store Store, d ecosystem.Declaration) (bool, error) {
		if store.Name == "outside-store" {
			t.Fatalf("lookup called for outside-store on a declaration outside its row: %+v", d)
		}
		return false, nil
	}
	d := ecosystem.Declaration{Ecosystem: synthetic.Strategy.Name, Host: "registry.example.com", RegistryName: "myorg"}

	_, searched, found := firstMatch(stores, ecosystem.CredentialStores(syntheticRows), lookup, d)
	if found {
		t.Errorf("firstMatch found = true, want false")
	}
	if want := []string{"outside-store", "netrc"}; !slices.Equal(searched, want) {
		t.Errorf("firstMatch searched = %v, want %v", searched, want)
	}
}

// The gate reads the injected Strategy-to-store map, not a hardcoded rule: a store
// the Strategy leaves out is named but never queried, and an ecosystem with no Strategy
// queries nothing.
func TestFirstMatch_QueriesOnlyStoresInRowSet(t *testing.T) {
	stores := []Store{
		{Name: "netrc", Path: "/home/agent/.netrc"},
		{Name: "other-store", Path: "/home/agent/other"},
	}
	byEcosystem := map[string][]string{"rowA": {"netrc"}}

	for _, tc := range []struct {
		ecosystem   string
		wantQueried []string
	}{
		{"rowA", []string{"netrc"}},
		{"rowB", nil},
	} {
		var queried []string
		lookup := func(store Store, _ ecosystem.Declaration) (bool, error) {
			queried = append(queried, store.Name)
			return false, nil
		}
		d := ecosystem.Declaration{Ecosystem: tc.ecosystem, Host: "reg.example.com"}

		_, searched, _ := firstMatch(stores, byEcosystem, lookup, d)
		if want := []string{"netrc", "other-store"}; !slices.Equal(searched, want) {
			t.Errorf("%s: searched = %v, want %v", tc.ecosystem, searched, want)
		}
		if !slices.Equal(queried, tc.wantQueried) {
			t.Errorf("%s: queried = %v, want %v", tc.ecosystem, queried, tc.wantQueried)
		}
	}
}

func TestDiscover_NilStores_UnmatchedHostSearchedIsEmpty(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFixture(t, dir, cfgPath, "main = https://registry.example.com/\n")

	lookup := func(store Store, d ecosystem.Declaration) (bool, error) {
		t.Fatalf("lookup called with no stores configured: %+v", d)
		return false, nil
	}

	// An empty stores list has nothing to name, so unlike the
	// configured-but-inapplicable case above this empty result is correct, not
	// a firstMatch bug. The command layer owns the fix for the "empty report
	// line" symptom: it must never pass empty stores when a home directory is
	// available (see TestCmdRegistryDiscover_HomeDirUnavailable_ErrorsInsteadOfProceeding).
	_, report := discoverSynthetic(t, dir, nil, lookup, probeAs("bearer"))
	if len(report.Unmatched) != 1 {
		t.Fatalf("report.Unmatched = %+v, want exactly 1", report.Unmatched)
	}
	if len(report.Unmatched[0].StoresSearched) != 0 {
		t.Errorf("report.Unmatched[0].StoresSearched = %v, want empty", report.Unmatched[0].StoresSearched)
	}
}

func TestDiscover_NoteFlowsToReportNoRegistry(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFixture(t, dir, cfgPath, "# declares no registry\n")

	routes, report := discoverSynthetic(t, dir, nil, noMatch, probeAs("bearer"))
	if len(routes) != 0 {
		t.Errorf("routes = %+v, want none", routes)
	}
	want := []ecosystem.Note{{ConfigPath: cfgPath, Ecosystem: synthetic.Strategy.Name}}
	if !slices.Equal(report.NoRegistry, want) {
		t.Errorf("report.NoRegistry = %+v, want %+v", report.NoRegistry, want)
	}
}

// A registry URL the Strategy's parser rejects as unusable yields no route, and is
// reported the way Extract reports any other unusable one. Discover has no
// empty-MatchHost guard of its own: registryroutes.Parse rejects such a route
// (see runRegistryDiscover's invariant in registrydiscover.go), so keeping it
// out is the parser's job.
func TestDiscover_UnusableURLYieldsNoRouteReportedAsSkipped(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFixture(t, dir, cfgPath, "main = ftp://registry.example.com/\n")

	routes, report := discoverSynthetic(t, dir, nil, noMatch, probeAs("bearer"))
	if len(routes) != 0 {
		t.Fatalf("routes = %+v, want none", routes)
	}
	want := []ecosystem.Note{{ConfigPath: cfgPath, Ecosystem: synthetic.Strategy.Name, Skipped: true}}
	if !slices.Equal(report.NoRegistry, want) {
		t.Errorf("report.NoRegistry = %+v, want %+v", report.NoRegistry, want)
	}
}

// envPlaceholder maps every non-alphanumeric byte to "_", so a host with a "."
// and a host with a "-" in the same position collide onto one env var name, and
// a token an operator sets for one would silently apply to the other. Both hosts
// here stay unmatched and must get distinct, deterministic CredentialValue names.
func TestDiscover_CollidingEnvPlaceholdersDisambiguated(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFixture(t, dir, cfgPath, "main = https://a.b.example.com/\nscoped = https://a-b.example.com/\n")

	routes, _ := discoverSynthetic(t, dir, nil, noMatch, probeAs("bearer"))
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
	routes2, _ := discoverSynthetic(t, dir, nil, noMatch, probeAs("bearer"))
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
	cfg := fmt.Sprintf("main = https://%s/\n"+
		"scope1 = https://%s/\n"+
		"scope2 = https://%s/\n", firstHost, secondHost, thirdHost)
	testutil.WriteFixture(t, dir, cfgPath, cfg)

	routes, _ := discoverSynthetic(t, dir, nil, noMatch, probeAs("bearer"))
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
	// (each host keeping its own name key), confirms the names are
	// independent of declaration order, not merely repeatable across
	// identical re-runs of the same fixture: a scheme that picks a
	// bucket's "winner" by first-seen order would pass a same-order
	// re-run but fail here.
	dir2 := t.TempDir()
	cfg2 := fmt.Sprintf("scope1 = https://%s/\n"+
		"scope2 = https://%s/\n"+
		"main = https://%s/\n", secondHost, thirdHost, firstHost)
	testutil.WriteFixture(t, dir2, cfgPath, cfg2)
	routes2, _ := discoverSynthetic(t, dir2, nil, noMatch, probeAs("bearer"))
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

func TestDisambiguateEnvPlaceholders_BoundExhaustedNamesEveryContestedPlaceholder(t *testing.T) {
	// Two independent pairs of hosts that genuinely collide under hostHash
	// (found by brute force, not stubbed): each pair shares one
	// CredentialValue, so the round bound trips for real, with no injected
	// hash. The error must name every contested placeholder, not just one,
	// since silently dropping a colliding name would leave an operator
	// unaware that name's hosts still share an env var too.
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
	for _, name := range []string{"SPINDRIFT_REGISTRY_CREDENTIAL_A", "SPINDRIFT_REGISTRY_CREDENTIAL_B"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name contested placeholder %s", err.Error(), name)
		}
	}
	for _, host := range []string{aHost1, aHost2, bHost1, bHost2} {
		if !strings.Contains(err.Error(), host) {
			t.Errorf("error %q does not name contested host %s", err.Error(), host)
		}
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

	// A non-colliding matched host, so report.Matched is covered too.
	const matchedHost = "matched.example.com"

	dir := t.TempDir()
	cfg := fmt.Sprintf("main = https://%s/\nscope1 = https://%s/\nscope2 = https://%s/\n", host1, host2, matchedHost)
	testutil.WriteFixture(t, dir, cfgPath, cfg)

	stores := []Store{{Name: "netrc", Path: "/fake/.netrc"}}
	lookup := func(store Store, d ecosystem.Declaration) (bool, error) {
		return d.Host == matchedHost, nil
	}

	routes, report, err := Discover(dir, syntheticRows, stores, lookup, probeAs("bearer"))
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
	// Discover still hands back the report it built before disambiguation
	// ran, so a caller can show which hosts went unmatched even on this
	// error path, rather than only the bare error.
	if len(report.Unmatched) != 2 {
		t.Fatalf("report.Unmatched = %+v, want 2 entries", report.Unmatched)
	}
	gotHosts := []string{report.Unmatched[0].Host, report.Unmatched[1].Host}
	slices.Sort(gotHosts)
	wantHosts := []string{host1, host2}
	slices.Sort(wantHosts)
	if !slices.Equal(gotHosts, wantHosts) {
		t.Errorf("report.Unmatched hosts = %v, want %v", gotHosts, wantHosts)
	}
	wantMatched := []MatchedHost{{Host: matchedHost, StoreName: "netrc", StorePath: "/fake/.netrc"}}
	if !slices.Equal(report.Matched, wantMatched) {
		t.Errorf("report.Matched = %+v, want %+v", report.Matched, wantMatched)
	}
}

func TestDiscover_ProbeGarbageFallsBackToBearer(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFixture(t, dir, cfgPath, hostDecl)

	routes, _ := discoverSynthetic(t, dir, nil, noMatch, probeAs("not-a-real-scheme"))
	if len(routes) != 1 {
		t.Fatalf("routes = %+v, want exactly 1", routes)
	}
	if routes[0].AuthScheme != "bearer" {
		t.Errorf("routes[0].AuthScheme = %q, want bearer", routes[0].AuthScheme)
	}
}

func TestDiscover_ProbeBasicHonored(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFixture(t, dir, cfgPath, hostDecl)

	routes, _ := discoverSynthetic(t, dir, nil, noMatch, probeAs("basic"))
	if len(routes) != 1 {
		t.Fatalf("routes = %+v, want exactly 1", routes)
	}
	if routes[0].AuthScheme != "basic" {
		t.Errorf("routes[0].AuthScheme = %q, want basic", routes[0].AuthScheme)
	}
}

func TestDiscover_ProbeHeaderSchemeHonored(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFixture(t, dir, cfgPath, hostDecl)

	routes, _ := discoverSynthetic(t, dir, nil, noMatch, probeAs("header:X-JFrog-Art-Api"))
	if len(routes) != 1 {
		t.Fatalf("routes = %+v, want exactly 1", routes)
	}
	if routes[0].AuthScheme != "header:X-JFrog-Art-Api" {
		t.Errorf("routes[0].AuthScheme = %q, want header:X-JFrog-Art-Api", routes[0].AuthScheme)
	}
}

func TestDiscover_SameHostTwoFilesOneRoute(t *testing.T) {
	dir := t.TempDir()
	twin := synthetic.Twin("synthetic-twin")
	testutil.WriteFixture(t, dir, cfgPath, "main = https://shared.example.com/first/\n")
	testutil.WriteFixture(t, dir, twin.InTreeConfigPath, "main = https://shared.example.com/second/\n")

	rows := []ecosystem.Strategy{syntheticRows[0], twin}
	routes, _, err := Discover(dir, rows, nil, noMatch, probeAs("bearer"))
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("routes = %+v, want exactly 1", routes)
	}
	// Strategy order decides, so the first Strategy's declaration wins for the shared host.
	if routes[0].UpstreamBaseURL != "https://shared.example.com/first" {
		t.Errorf("routes[0].UpstreamBaseURL = %q, want the first row's URL", routes[0].UpstreamBaseURL)
	}
}

// A matched store's companion values (registry name, property key) are exactly
// what that kind's own StoreConfig derives from the declaration: Discover sets
// them for every store kind, and a kind with a companion key must end up with
// that companion filled in.
func TestDiscover_MatchedStoreCompanionsComeFromKindStoreConfig(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFixture(t, dir, cfgPath, "main = https://registry.example.com/index/\n")
	decl := ecosystem.Declaration{
		Host:            "registry.example.com",
		UpstreamBaseURL: "https://registry.example.com/index",
		RegistryName:    "main",
	}

	for _, kind := range storeKinds() {
		t.Run(kind.SourceKey, func(t *testing.T) {
			const path = "/home/agent/store"
			stores := []Store{{Name: kind.SourceKey, Path: path}}
			row := synthetic.Strategy
			row.CredentialStores = []string{kind.SourceKey}
			routes, _, err := Discover(dir, []ecosystem.Strategy{row}, stores, alwaysMatch, probeAs("bearer"))
			if err != nil {
				t.Fatalf("Discover: unexpected error: %v", err)
			}
			if len(routes) != 1 {
				t.Fatalf("routes = %+v, want exactly 1", routes)
			}

			want := kind.StoreConfig(path, declarationFacts(decl))
			if routes[0].RegistryName != want.RegistryName || routes[0].PropertyKey != want.PropertyKey {
				t.Errorf("companions = %q/%q, want %q/%q", routes[0].RegistryName, routes[0].PropertyKey, want.RegistryName, want.PropertyKey)
			}
			if kind.CompanionKey == "" {
				if routes[0].RegistryName != "" || routes[0].PropertyKey != "" {
					t.Errorf("companions = %q/%q, want none for a kind with no companion key", routes[0].RegistryName, routes[0].PropertyKey)
				}
				return
			}
			// want is StoreConfig's own output, so also pin the companion to a
			// fact the declaration supplies, independent of StoreConfig.
			got := credresolver.Config{RegistryName: routes[0].RegistryName, PropertyKey: routes[0].PropertyKey}
			if v := *kind.CompanionField(&got); v != decl.RegistryName && v != decl.Host {
				t.Errorf("companion %s = %q, want the declaration's registry name %q or host %q", kind.CompanionKey, v, decl.RegistryName, decl.Host)
			}
		})
	}
}

// registryvocab.HostKey normalizes host case and the default port, which is what
// makes these two declarations dedupe onto one route.
func TestDiscover_NormalizedHostDedupesCaseAndPort(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFixture(t, dir, cfgPath, "main = https://Shared.Example.com:8443/first/\nother = https://shared.example.com/other/\n")

	routes, _ := discoverSynthetic(t, dir, nil, noMatch, probeAs("bearer"))
	if len(routes) != 1 {
		t.Fatalf("routes = %+v, want exactly 1 (case/port variants of the same host)", routes)
	}
	if routes[0].MatchHost != "shared.example.com" {
		t.Errorf("routes[0].MatchHost = %q, want shared.example.com", routes[0].MatchHost)
	}
}

// The stub Strategy's config path is in no registered Strategy, so a route can only appear if
// Discover walked the supplied Strategies rather than the registered Strategies.
func TestDiscover_ScansSuppliedRows(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFixture(t, dir, "fake.cfg", "x")
	row := ecosystem.Strategy{
		Name:             "fake",
		InTreeConfigPath: "fake.cfg",
		ConfigParser: func(string) ([]ecosystem.Declaration, bool, error) {
			return []ecosystem.Declaration{{Host: "reg.fake.example", UpstreamBaseURL: "https://reg.fake.example"}}, true, nil
		},
	}

	routes, _, err := Discover(dir, []ecosystem.Strategy{row}, nil, noMatch, probeAs("bearer"))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(routes) != 1 || routes[0].MatchHost != "reg.fake.example" {
		t.Errorf("routes = %+v, want one route for reg.fake.example", routes)
	}
}

func TestDiscover_ProbeProfileLandsOnRoute(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFixture(t, dir, cfgPath, hostDecl)

	advice := ProfileAdvice{Name: "artifactory", Note: "JFrog Artifactory", BasicUsername: "user"}
	probe := func(string) ProbeResult {
		return ProbeResult{Scheme: "basic", Profile: advice, SchemeFromProfile: true}
	}
	routes, _ := discoverSynthetic(t, dir, nil, noMatch, probe)
	if len(routes) != 1 {
		t.Fatalf("routes = %+v, want exactly 1", routes)
	}
	if routes[0].AuthScheme != "basic" || !reflect.DeepEqual(routes[0].Profile, advice) || !routes[0].SchemeFromProfile {
		t.Errorf("route = %+v, want AuthScheme basic, the profile advice, SchemeFromProfile", routes[0])
	}
}

func TestDiscover_RejectedProfileSchemeIsNotAttributedToProfile(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFixture(t, dir, cfgPath, hostDecl)

	probe := func(string) ProbeResult {
		return ProbeResult{Scheme: "header:", Profile: ProfileAdvice{Name: "x"}, SchemeFromProfile: true}
	}
	routes, _ := discoverSynthetic(t, dir, nil, noMatch, probe)
	if len(routes) != 1 {
		t.Fatalf("routes = %+v, want exactly 1", routes)
	}
	if routes[0].AuthScheme != "bearer" || routes[0].SchemeFromProfile {
		t.Errorf("route = %+v, want fallback AuthScheme bearer and no SchemeFromProfile", routes[0])
	}
}

func siblingsProbe(siblings map[string][]string) Probe {
	return func(string) ProbeResult {
		return ProbeResult{Scheme: "bearer", Profile: ProfileAdvice{Name: "p", Siblings: siblings}}
	}
}

func TestDiscover_AllowMergesSiblingsOfServedEcosystemsOnly(t *testing.T) {
	dir := t.TempDir()
	twin := synthetic.Twin("synthetic-twin")
	other := synthetic.Twin("synthetic-other")
	testutil.WriteFixture(t, dir, cfgPath, "main = https://host.example.com/repo/index/\n")
	testutil.WriteFixture(t, dir, twin.InTreeConfigPath, "main = https://host.example.com/other/index/\n")
	// The probe sees one host once, yet each declaration's siblings count.
	rows := []ecosystem.Strategy{syntheticRows[0], twin, other}
	probe := siblingsProbe(map[string][]string{
		"synthetic":       {"../v1/crates", "../v1/crates"},
		"synthetic-twin":  {"../v1/crates", "extra"},
		"synthetic-other": {"../unserved"},
	})

	routes, _, err := Discover(dir, rows, nil, noMatch, probe)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("routes = %+v, want exactly 1", routes)
	}
	want := []string{"/repo/v1/crates", "/other/v1/crates", "/other/index/extra"}
	if !slices.Equal(routes[0].Allow, want) {
		t.Errorf("Allow = %v, want %v", routes[0].Allow, want)
	}
}

// A root-rooted declaration has no subtree to stay inside: path.Join clamps
// "..", so "../v1/crates" would become "/v1/crates", outside anything declared.
func TestDiscover_AllowSkipsRootRootedDeclaration(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFixture(t, dir, cfgPath, "main = https://host.example.com/\n")

	routes, _ := discoverSynthetic(t, dir, nil, noMatch, siblingsProbe(map[string][]string{"synthetic": {"..", "/", "x", "../v1/crates"}}))
	if len(routes) != 1 {
		t.Fatalf("routes = %+v, want exactly 1", routes)
	}
	if len(routes[0].Allow) != 0 {
		t.Errorf("Allow = %v, want none", routes[0].Allow)
	}
}

func TestDiscover_AllowDropsBareRootJoin(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFixture(t, dir, cfgPath, "main = https://host.example.com/api/\n")

	routes, _ := discoverSynthetic(t, dir, nil, noMatch, siblingsProbe(map[string][]string{"synthetic": {"..", "x"}}))
	if len(routes) != 1 {
		t.Fatalf("routes = %+v, want exactly 1", routes)
	}
	if want := []string{"/api/x"}; !slices.Equal(routes[0].Allow, want) {
		t.Errorf("Allow = %v, want %v", routes[0].Allow, want)
	}
}

func TestSiblingAllow_SkipsUnparseableUpstreamBaseURL(t *testing.T) {
	decls := []ecosystem.Declaration{{Ecosystem: "synthetic", UpstreamBaseURL: "https://host.example.com/%zz/"}}
	got := siblingAllow(decls, ProfileAdvice{Siblings: map[string][]string{"synthetic": {"x", "../v1/crates"}}})
	if len(got) != 0 {
		t.Errorf("siblingAllow = %v, want none", got)
	}
}

// A declared path whose percent-escapes decode to "?", "#", or a dot segment
// would give a join the routes file rejects or one that leaves the declared
// subtree, so such a declaration contributes no sibling allow entry.
func TestDiscover_AllowSkipsNonCanonicalDeclaredSubtree(t *testing.T) {
	for _, tc := range []struct{ name, url string }{
		{"encoded question mark", "https://host.example.com/api/a%3Fb/index/"},
		{"encoded hash", "https://host.example.com/api/a%23b/index/"},
		{"encoded dot-dot segment", "https://host.example.com/a%2F..%2F/"},
		{"encoded dot segment", "https://host.example.com/a%2F.%2Fb/"},
		{"encoded empty segment", "https://host.example.com/a%2F%2Fb/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			testutil.WriteFixture(t, dir, cfgPath, "main = "+tc.url+"\n")

			routes, _ := discoverSynthetic(t, dir, nil, noMatch, siblingsProbe(map[string][]string{"synthetic": {"x", "../v1/crates"}}))
			if len(routes) != 1 {
				t.Fatalf("routes = %+v, want exactly 1", routes)
			}
			if len(routes[0].Allow) != 0 {
				t.Errorf("Allow = %v, want none", routes[0].Allow)
			}
			if _, err := registryroutes.Parse(Render(routes), nil); err != nil {
				t.Errorf("rendered routes file does not parse: %v", err)
			}
		})
	}
}

func TestDiscover_NoSiblingsLeavesAllowEmpty(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFixture(t, dir, cfgPath, hostDecl)

	routes, _ := discoverSynthetic(t, dir, nil, noMatch, siblingsProbe(nil))
	if len(routes) != 1 || routes[0].Allow != nil {
		t.Errorf("routes = %+v, want one route with nil Allow", routes)
	}
}
