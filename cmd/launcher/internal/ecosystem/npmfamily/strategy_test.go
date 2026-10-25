package npmfamily

import (
	"reflect"
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/credresolver"
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/registryvocab"
)

// strategies lists the family in the Bundle's order, since Strategy order is the
// precedence these tests lean on.
var strategies = []ecosystem.Strategy{Npm, Yarn, Pnpm}

// stubGetenv stands in for os.Getenv so these tests never read the real
// process environment.
func stubGetenv(env map[string]string) func(string) string {
	return func(key string) string { return env[key] }
}

func rowByName(t *testing.T, name string) ecosystem.Strategy {
	t.Helper()
	for _, row := range strategies {
		if row.Name == name {
			return row
		}
	}
	t.Fatalf("no row named %q", name)
	return ecosystem.Strategy{}
}

func TestStrategy_CredentialStores(t *testing.T) {
	want := []string{"gradle-properties", "netrc", "npmrc"}
	storeKeys := map[string]bool{}
	for _, k := range credresolver.StoreKinds() {
		storeKeys[k.SourceKey] = true
	}
	stores := ecosystem.CredentialStores(strategies)
	for _, s := range []ecosystem.Strategy{Npm, Yarn, Pnpm} {
		got := slices.Sorted(slices.Values(stores[s.Name]))
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: stores = %v, want %v", s.Name, got, want)
		}
		for _, key := range s.CredentialStores {
			if !storeKeys[key] {
				t.Errorf("%s: %q is not a credresolver store kind", s.Name, key)
			}
		}
	}
}

// A Strategy that gains or loses EnvExports otherwise fails silently: it
// contributes nothing to the export file, or panics a caller.
func TestStrategies_EnvExportsPresence(t *testing.T) {
	want := map[string]bool{
		"npm":  true,
		"yarn": true,
		"pnpm": true,
	}
	for _, row := range strategies {
		wantPresent, ok := want[row.Name]
		if !ok {
			t.Fatalf("row %q not covered by this test's want map", row.Name)
		}
		gotPresent := row.EnvExports != nil
		if gotPresent != wantPresent {
			t.Errorf("row %q EnvExports present = %v, want %v", row.Name, gotPresent, wantPresent)
		}
	}
}

// npm, pnpm and yarn bind the Forwarder through an env var; a Strategy that
// binds through a file leaves the field empty.
func TestStrategies_BindingEnvVarPresence(t *testing.T) {
	want := map[string]string{
		"npm":  "npm_config_registry",
		"yarn": "YARN_NPM_REGISTRY_SERVER",
		"pnpm": "pnpm_config_registry",
	}
	for _, row := range strategies {
		wantVar, ok := want[row.Name]
		if !ok {
			t.Fatalf("row %q not covered by this test's want map", row.Name)
		}
		if row.BindingEnvVar != wantVar {
			t.Errorf("row %q BindingEnvVar = %q, want %q", row.Name, row.BindingEnvVar, wantVar)
		}
	}
}

// A single Strategy's own EnvExports output cannot always be diffed against its
// BindingEnvVar: a renderer may emit its vars conditionally. So this checks
// each BindingEnvVar against the whole export file, walking EnvExportRows
// the way bindings mode does.
func TestStrategies_BindingEnvVarMatchesRenderedExports(t *testing.T) {
	renderedNames := map[string]bool{}
	for _, row := range ecosystem.EnvExportRows(strategies) {
		exports, _ := row.EnvExports(27182, "r0", stubGetenv(nil), allDeclaredRoutes())
		for _, export := range exports {
			renderedNames[export.Name] = true
		}
	}
	for _, row := range strategies {
		if row.BindingEnvVar == "" {
			continue
		}
		if !renderedNames[row.BindingEnvVar] {
			t.Errorf("row %q BindingEnvVar %q not among rendered export names %v", row.Name, row.BindingEnvVar, renderedNames)
		}
	}
}

// No npm-family Strategy re-renders its home config once the Target repo is on
// disk (issue #3201); a Strategy that does must fail here rather than pass
// silently.
func TestStrategies_RepoAwareHomeConfigPresence(t *testing.T) {
	want := map[string]bool{
		"npm":  false,
		"yarn": false,
		"pnpm": false,
	}
	for _, row := range strategies {
		wantPresent, ok := want[row.Name]
		if !ok {
			t.Fatalf("row %q not covered by this test's want map", row.Name)
		}
		gotPresent := row.RepoAwareHomeConfig != nil
		if gotPresent != wantPresent {
			t.Errorf("row %q RepoAwareHomeConfig present = %v, want %v", row.Name, gotPresent, wantPresent)
		}
	}
}

// The only caller reaches repo-aware Strategies by filtering HomeConfigRows, so
// a Strategy setting RepoAwareHomeConfig with a nil HomeConfig never runs at all
// (issue #3201).
func TestStrategies_RepoAwareHomeConfigRequiresHomeConfig(t *testing.T) {
	for _, row := range strategies {
		if row.RepoAwareHomeConfig != nil && row.HomeConfig == nil {
			t.Errorf("row %q sets RepoAwareHomeConfig with a nil HomeConfig, want both set: such a row never runs, since repo-aware rows are reached only by filtering ecosystem.HomeConfigRows", row.Name)
		}
	}
}

// BindHomes announces AnnounceBindingValue by reading the BindingEnvVar export,
// so a Strategy setting it without naming a var would announce nothing.
func TestStrategies_AnnounceBindingValueRequiresBindingEnvVar(t *testing.T) {
	for _, row := range strategies {
		if row.AnnounceBindingValue && row.BindingEnvVar == "" {
			t.Errorf("row %q sets AnnounceBindingValue with an empty BindingEnvVar, want both set", row.Name)
		}
	}
}

// Duplicate names would make every lookup by name ambiguous.
func TestStrategies_NamesUnique(t *testing.T) {
	seen := make(map[string]bool, len(strategies))
	for _, row := range strategies {
		if seen[row.Name] {
			t.Fatalf("duplicate row name %q", row.Name)
		}
		seen[row.Name] = true
	}
}

// A Strategy added without a classification would silently carry a blank nudge
// string.
func TestStrategies_ClassificationNonEmpty(t *testing.T) {
	for _, row := range strategies {
		if row.Classification == "" {
			t.Errorf("row %q has empty classification", row.Name)
		}
	}
}

// The want map covers the empty paths too, so a Strategy added without a decision
// on its in-tree registry-config path fails here rather than being silently
// excluded or included wherever a consumer filters on the field.
func TestStrategies_InTreeConfigPath(t *testing.T) {
	want := map[string]string{
		"npm":  ".npmrc",
		"yarn": ".yarnrc.yml",
		"pnpm": "pnpm-workspace.yaml",
	}
	for _, row := range strategies {
		path, ok := want[row.Name]
		if !ok {
			t.Errorf("row %q has no expected InTreeConfigPath in this test", row.Name)
			continue
		}
		if row.InTreeConfigPath != path {
			t.Errorf("row %q InTreeConfigPath = %q, want %q", row.Name, row.InTreeConfigPath, path)
		}
	}
}

// Nothing else in this package asserts on the collected slice's contents, so
// a Strategy the collector forgets to gather would fail silently: New would just
// never see it.
// Without this pin, dropping Npm's RewriteRows shows up only indirectly,
// as a proxy round-trip failure a package away from the cause.
func TestResponseRewriteRows_ContainsNpmPackumentRow(t *testing.T) {
	rows := ecosystem.ResponseRewriteRows([]ecosystem.Strategy{Npm, Yarn, Pnpm})
	if len(rows) == 0 {
		t.Fatal("ResponseRewriteRows() = empty, want at least npm's packument row")
	}
	for _, row := range rows {
		if row.Name == "npm packument" && row.Ecosystem == "npm" {
			return
		}
	}
	t.Errorf("ResponseRewriteRows() = %+v, want a row named %q tagged %q", rows, "npm packument", "npm")
}

// No npm-family Strategy writes a home-level config. A Strategy gaining or
// losing one otherwise just changes HomeConfigRows' count silently.
func TestStrategies_HomeConfigPresence(t *testing.T) {
	want := map[string]bool{
		"npm":  false,
		"yarn": false,
		"pnpm": false,
	}
	for _, row := range strategies {
		wantPresent, ok := want[row.Name]
		if !ok {
			t.Fatalf("row %q not covered by this test's want map", row.Name)
		}
		gotPresent := row.HomeConfig != nil
		if gotPresent != wantPresent {
			t.Errorf("row %q HomeConfig present = %v, want %v", row.Name, gotPresent, wantPresent)
		}
	}

	homeConfigRowCount := 0
	for _, present := range want {
		if present {
			homeConfigRowCount++
		}
	}
	if got := len(ecosystem.HomeConfigRows(strategies)); got != homeConfigRowCount {
		t.Errorf("len(ecosystem.HomeConfigRows(strategies)) = %d, want %d", got, homeConfigRowCount)
	}
}

// allDeclaredRoutes declares a path for every ecosystem a Strategy binds an env
// var for, so one route renders all three bindings at once. The npm family
// declares their paths as tagged enforced paths.
func allDeclaredRoutes() []registrymanifest.Route {
	return []registrymanifest.Route{{
		Prefix: "r0",
		EnforcedPaths: []registryvocab.Subtree{
			{Ecosystem: "npm", Path: "/npm"},
			{Ecosystem: "pnpm", Path: "/pnpm"},
			{Ecosystem: "yarn", Path: "/yarn"},
		},
	}}
}

// Some Strategies carry a retired top-level routes-file key (ADR 0047, issue
// #3261) predating [routes.ecosystems.<name>] (issue #3403). A Strategy gaining
// or losing the key must fail here.
func TestStrategies_RetiredRouteKeyPresence(t *testing.T) {
	want := map[string]string{
		"npm":  "",
		"yarn": "",
		"pnpm": "",
	}
	for _, row := range strategies {
		wantKey, ok := want[row.Name]
		if !ok {
			t.Fatalf("row %q not covered by this test's want map", row.Name)
		}
		if row.RetiredRouteKey != wantKey {
			t.Errorf("row %q RetiredRouteKey = %q, want %q", row.Name, row.RetiredRouteKey, wantKey)
		}
	}
}

// A nil RouteDeclaration means "no such notion", not "accept anything", so the
// caller rejects every non-"path" key for a Strategy with no hook.
func TestStrategies_NoStrategyHasRouteDeclaration(t *testing.T) {
	for _, row := range strategies {
		if row.RouteDeclaration != nil {
			t.Errorf("row %q: RouteDeclaration is non-nil, want nil (no key beyond \"path\" accepted)", row.Name)
		}
	}
}
