package ecosystem

import (
	"testing"

	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/registryvocab"
)

// stubGetenv stands in for os.Getenv so these tests never read the real
// process environment.
func stubGetenv(env map[string]string) func(string) string {
	return func(key string) string { return env[key] }
}

func rowByName(t *testing.T, name string) Strategy {
	t.Helper()
	for _, row := range Table {
		if row.Name == name {
			return row
		}
	}
	t.Fatalf("no row named %q", name)
	return Strategy{}
}

// A Strategy that gains or loses EnvExports otherwise fails silently: it
// contributes nothing to the export file, or panics a caller.
func TestTable_EnvExportsPresence(t *testing.T) {
	want := map[string]bool{
		"cargo": false,
		"npm":   true,
		"yarn":  true,
		"pnpm":  true,
	}
	for _, row := range Table {
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

// npm, pnpm and yarn bind the Forwarder through an env var; cargo binds
// through a file and leaves the field empty.
func TestTable_BindingEnvVarPresence(t *testing.T) {
	want := map[string]string{
		"cargo": "",
		"npm":   "npm_config_registry",
		"yarn":  "YARN_NPM_REGISTRY_SERVER",
		"pnpm":  "pnpm_config_registry",
	}
	for _, row := range Table {
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
// each BindingEnvVar against the whole export file, walking EnvExportRows()
// the way bindings mode does.
func TestTable_BindingEnvVarMatchesRenderedExports(t *testing.T) {
	renderedNames := map[string]bool{}
	for _, row := range EnvExportRows() {
		exports, _ := row.EnvExports(27182, "r0", stubGetenv(nil), allDeclaredRoutes())
		for _, export := range exports {
			renderedNames[export.Name] = true
		}
	}
	for _, row := range Table {
		if row.BindingEnvVar == "" {
			continue
		}
		if !renderedNames[row.BindingEnvVar] {
			t.Errorf("row %q BindingEnvVar %q not among rendered export names %v", row.Name, row.BindingEnvVar, renderedNames)
		}
	}
}

// Only cargo re-renders its home config once the Target repo is on disk
// (issue #3201), because only cargo binds through source replacement keyed
// off the repo's own un-rewritten registry declarations.
func TestTable_RepoAwareHomeConfigPresence(t *testing.T) {
	want := map[string]bool{
		"cargo": true,
		"npm":   false,
		"yarn":  false,
		"pnpm":  false,
	}
	for _, row := range Table {
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

// The only caller reaches repo-aware Strategies by filtering HomeConfigRows(), so
// a Strategy setting RepoAwareHomeConfig with a nil HomeConfig never runs at all
// (issue #3201).
func TestTable_RepoAwareHomeConfigRequiresHomeConfig(t *testing.T) {
	for _, row := range Table {
		if row.RepoAwareHomeConfig != nil && row.HomeConfig == nil {
			t.Errorf("row %q sets RepoAwareHomeConfig with a nil HomeConfig, want both set: such a row never runs, since repo-aware rows are reached only by filtering HomeConfigRows()", row.Name)
		}
	}
}

// BindHomes announces AnnounceBindingValue by reading the BindingEnvVar export,
// so a Strategy setting it without naming a var would announce nothing.
func TestTable_AnnounceBindingValueRequiresBindingEnvVar(t *testing.T) {
	for _, row := range Table {
		if row.AnnounceBindingValue && row.BindingEnvVar == "" {
			t.Errorf("row %q sets AnnounceBindingValue with an empty BindingEnvVar, want both set", row.Name)
		}
	}
}

// Table order is the export-file order, so the stub's export-less Strategy must be
// skipped without disturbing the rest.
func TestEnvExportRows_FollowsTableOrder(t *testing.T) {
	renderer := func(int, string, func(string) string, []registrymanifest.Route) ([]EnvExport, []string) {
		return nil, nil
	}
	rows := []Strategy{
		{Name: "first", EnvExports: renderer},
		{Name: "no-exports"},
		{Name: "second", EnvExports: renderer},
		{Name: "third", EnvExports: renderer},
	}

	var got []string
	for _, row := range EnvExportRowsOf(rows) {
		got = append(got, row.Name)
	}

	want := []string{"first", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("EnvExportRowsOf() = %v, want %v", got, want)
	}
	for i, name := range want {
		if got[i] != name {
			t.Errorf("row %d = %q, want %q (full order %v)", i, got[i], name, got)
		}
	}
}

func TestHomeConfigRowsOf_KeepsHomeConfigRowsInInputOrder(t *testing.T) {
	rows := []Strategy{
		{Name: "b", HomeConfig: &HomeConfig{}},
		{Name: "none"},
		{Name: "a", HomeConfig: &HomeConfig{}},
	}

	var got []string
	for _, row := range HomeConfigRowsOf(rows) {
		got = append(got, row.Name)
	}

	if len(got) != 2 || got[0] != "b" || got[1] != "a" {
		t.Errorf("HomeConfigRowsOf() = %v, want [b a]", got)
	}
}

// Duplicate names would make every lookup by name ambiguous.
func TestTable_NamesUnique(t *testing.T) {
	seen := make(map[string]bool, len(Table))
	for _, row := range Table {
		if seen[row.Name] {
			t.Fatalf("duplicate row name %q", row.Name)
		}
		seen[row.Name] = true
	}
}

// A Strategy added without a classification would silently carry a blank nudge
// string.
func TestTable_ClassificationNonEmpty(t *testing.T) {
	for _, row := range Table {
		if row.Classification == "" {
			t.Errorf("row %q has empty classification", row.Name)
		}
	}
}

// The want map covers the empty paths too, so a Strategy added without a decision
// on its in-tree registry-config path fails here rather than being silently
// excluded or included wherever a consumer filters on the field.
func TestTable_InTreeConfigPath(t *testing.T) {
	want := map[string]string{
		"cargo": ".cargo/config.toml",
		"npm":   ".npmrc",
		"yarn":  ".yarnrc.yml",
		"pnpm":  "pnpm-workspace.yaml",
	}
	for _, row := range Table {
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
func TestResponseRewriteRows_ContainsCargoConfigRow(t *testing.T) {
	rows := ResponseRewriteRows()
	if len(rows) == 0 {
		t.Fatal("ResponseRewriteRows() = empty, want at least cargo's config.json row")
	}
	for _, row := range rows {
		if row.Name == "cargo config.json" && row.Ecosystem == "cargo" {
			return
		}
	}
	t.Errorf("ResponseRewriteRows() = %+v, want a row named %q tagged %q", rows, "cargo config.json", "cargo")
}

// Without this pin, dropping Npm's RewriteRows shows up only indirectly,
// as a proxy round-trip failure a package away from the cause.
func TestResponseRewriteRows_ContainsNpmPackumentRow(t *testing.T) {
	rows := ResponseRewriteRows()
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

// Cargo is the only core Strategy with a home-level config; a second one must
// fail here rather than pass silently.
func TestHomeConfigRows_Cargo(t *testing.T) {
	var got []string
	for _, row := range HomeConfigRows() {
		got = append(got, row.Name)
	}
	want := []string{"cargo"}
	if len(got) != len(want) {
		t.Fatalf("HomeConfigRows() = %v, want %v", got, want)
	}
	for i, name := range want {
		if got[i] != name {
			t.Errorf("row %d = %q, want %q", i, got[i], name)
		}
	}
}

// The cargo Strategy's Render must be CargoConfigTOML itself, not a
// reimplementation of it.
func TestCargoRowHomeConfig(t *testing.T) {
	row := rowByName(t, "cargo")
	if row.HomeConfig == nil {
		t.Fatal("cargo row has nil HomeConfig")
	}
	hc := row.HomeConfig
	if hc.HomeEnvVar != "CARGO_HOME" {
		t.Errorf("HomeEnvVar = %q, want %q", hc.HomeEnvVar, "CARGO_HOME")
	}
	if hc.HomeRelativeDefault != ".cargo" {
		t.Errorf("HomeRelativeDefault = %q, want %q", hc.HomeRelativeDefault, ".cargo")
	}
	if hc.ConfigPath != "config.toml" {
		t.Errorf("ConfigPath = %q, want %q", hc.ConfigPath, "config.toml")
	}
	if hc.Render == nil {
		t.Fatal("cargo row HomeConfig has nil Render")
	}
	got := hc.Render(27182, "r0", nil)
	want := CargoConfigTOML(27182, "r0", nil)
	if got != want {
		t.Errorf("Render(27182, %q) = %q, want %q", "r0", got, want)
	}
}

// Only cargo writes a home-level config among the core Strategies. A Strategy
// gaining or losing one otherwise just changes HomeConfigRows' count silently.
func TestTable_HomeConfigPresence(t *testing.T) {
	want := map[string]bool{
		"cargo": true,
		"npm":   false,
		"yarn":  false,
		"pnpm":  false,
	}
	for _, row := range Table {
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
	if got := len(HomeConfigRows()); got != homeConfigRowCount {
		t.Errorf("len(HomeConfigRows()) = %d, want %d", got, homeConfigRowCount)
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
func TestTable_RetiredRouteKeyPresence(t *testing.T) {
	want := map[string]string{
		"cargo": "cargo-registries",
		"npm":   "",
		"yarn":  "",
		"pnpm":  "",
	}
	for _, row := range Table {
		wantKey, ok := want[row.Name]
		if !ok {
			t.Fatalf("row %q not covered by this test's want map", row.Name)
		}
		if row.RetiredRouteKey != wantKey {
			t.Errorf("row %q RetiredRouteKey = %q, want %q", row.Name, row.RetiredRouteKey, wantKey)
		}
	}
}

// registryroutes.mergeRetiredRouteEcosystems resolves keys through this
// lookup instead of hand-listing ecosystem names, so it must both resolve
// every declared key and reject one no Strategy declares.
func TestRowByRetiredRouteKey(t *testing.T) {
	for key, wantName := range map[string]string{
		"cargo-registries": "cargo",
	} {
		row, ok := RowByRetiredRouteKey(key)
		if !ok {
			t.Fatalf("RowByRetiredRouteKey(%q): ok = false, want true", key)
		}
		if row.Name != wantName {
			t.Errorf("RowByRetiredRouteKey(%q).Name = %q, want %q", key, row.Name, wantName)
		}
	}

	if _, ok := RowByRetiredRouteKey("npm-registry"); ok {
		t.Error(`RowByRetiredRouteKey("npm-registry"): ok = true, want false`)
	}
	if _, ok := RowByRetiredRouteKey(""); ok {
		t.Error(`RowByRetiredRouteKey(""): ok = true, want false`)
	}
}
