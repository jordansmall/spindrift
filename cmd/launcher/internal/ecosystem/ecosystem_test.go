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

func rowByName(t *testing.T, name string) Row {
	t.Helper()
	for _, row := range Table {
		if row.Name == name {
			return row
		}
	}
	t.Fatalf("no row named %q", name)
	return Row{}
}

// The npm row must delegate to NpmFamilyBindings rather than reimplement it.
func TestNpmRowEnvExports(t *testing.T) {
	row := rowByName(t, "npm")
	if row.EnvExports == nil {
		t.Fatal("npm row has nil EnvExports")
	}

	gotExports, gotWarnings := row.EnvExports(27182, "r0", stubGetenv(nil), allDeclaredRoutes())

	want, _ := NpmFamilyBindings(27182, "r0", allDeclaredRoutes())
	if len(gotExports) != len(want) {
		t.Fatalf("got %d exports, want %d: %v", len(gotExports), len(want), gotExports)
	}
	for i, w := range want {
		if gotExports[i] != w {
			t.Errorf("export %d = %+v, want %+v", i, gotExports[i], w)
		}
	}
	if gotWarnings != nil {
		t.Errorf("got warnings %v, want nil", gotWarnings)
	}
}

// The go row must pass its getenv parameter into ComputeGoBindings rather
// than discard the snapshot it is handed.
func TestGoRowEnvExports(t *testing.T) {
	row := rowByName(t, "go")
	if row.EnvExports == nil {
		t.Fatal("go row has nil EnvExports")
	}

	overrideEnv := stubGetenv(map[string]string{"GOTOOLCHAIN": "auto"})
	_, gotWarnings := row.EnvExports(27182, "r0", overrideEnv, allDeclaredRoutes())
	if !containsWarningSubstring(gotWarnings, "GOTOOLCHAIN") {
		t.Errorf("expected a GOTOOLCHAIN override warning, got %v", gotWarnings)
	}

	emptyEnv := stubGetenv(nil)
	gotExports, gotWarnings2 := row.EnvExports(27182, "r0", emptyEnv, allDeclaredRoutes())
	if len(gotWarnings2) != 0 {
		t.Errorf("expected no warnings with an empty env snapshot, got %v", gotWarnings2)
	}
	if value, ok := ExportValue(gotExports, "GOPROXY"); !ok || value != "http://127.0.0.1:27182/r0/go" {
		t.Errorf("GOPROXY export = (%q, %v), want (%q, true)", value, ok, "http://127.0.0.1:27182/r0/go")
	}
}

// The pre-#3260 row discarded its routes parameter. A route declaring a go
// path renders a full-path GOPROXY the row can only produce by passing
// routes through to ComputeGoBindings.
func TestGoRowEnvExports_ThreadsRoutes(t *testing.T) {
	row := rowByName(t, "go")
	routes := []registrymanifest.Route{{
		Prefix:     "r0",
		Ecosystems: registryvocab.RouteEcosystems{"go": registryvocab.RouteDeclaration{"path": "/artifactory/api/go/go-local"}},
	}}

	gotExports, _ := row.EnvExports(27182, "r0", stubGetenv(nil), routes)

	want := "http://127.0.0.1:27182/r0/artifactory/api/go/go-local"
	if value, ok := ExportValue(gotExports, "GOPROXY"); !ok || value != want {
		t.Errorf("GOPROXY export = (%q, %v), want (%q, true)", value, ok, want)
	}
}

// A row that gains or loses EnvExports otherwise fails silently: it
// contributes nothing to the export file, or panics a caller.
func TestTable_EnvExportsPresence(t *testing.T) {
	want := map[string]bool{
		"cargo":  false,
		"npm":    true,
		"yarn":   false,
		"pnpm":   false,
		"go":     true,
		"gradle": false,
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

// npm, pnpm, yarn and go bind the Forwarder through an env var; cargo and
// gradle bind through a file and leave the field empty. pnpm and yarn carry
// a value despite rendering no EnvExports of their own, because npm's
// NpmFamilyBindings renders all three family vars in one call.
func TestTable_BindingEnvVarPresence(t *testing.T) {
	want := map[string]string{
		"cargo":  "",
		"npm":    "npm_config_registry",
		"yarn":   "YARN_NPM_REGISTRY_SERVER",
		"pnpm":   "pnpm_config_registry",
		"go":     "GOPROXY",
		"gradle": "",
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

// No single row's own EnvExports output can be diffed against its
// BindingEnvVar: pnpm and yarn render none, and go's renderer emits its vars
// conditionally. So this checks each BindingEnvVar against the whole export
// file, walking EnvExportRows() the way bindings mode does.
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
		"cargo":  true,
		"npm":    false,
		"yarn":   false,
		"pnpm":   false,
		"go":     false,
		"gradle": false,
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

// The only caller reaches repo-aware rows by filtering HomeConfigRows(), so
// a row setting RepoAwareHomeConfig with a nil HomeConfig never runs at all
// (issue #3201).
func TestTable_RepoAwareHomeConfigRequiresHomeConfig(t *testing.T) {
	for _, row := range Table {
		if row.RepoAwareHomeConfig != nil && row.HomeConfig == nil {
			t.Errorf("row %q sets RepoAwareHomeConfig with a nil HomeConfig, want both set: such a row never runs, since repo-aware rows are reached only by filtering HomeConfigRows()", row.Name)
		}
	}
}

// The stub table puts its export-carrying rows in the exact reverse of their
// EnvExportOrder, so passing proves the sort rather than the literal order.
// The two tied rows pin that a tie keeps table order, which is what makes
// the ordering deterministic for rows that never set the field.
func TestEnvExportRows_SortsByEnvExportOrderNotTableOrder(t *testing.T) {
	renderer := func(int, string, func(string) string, []registrymanifest.Route) ([]EnvExport, []string) {
		return nil, nil
	}
	// Swapping the package-level Table bars t.Parallel here and in every other
	// test in this package, since a parallel neighbour would observe the stub.
	original := Table
	Table = []Row{
		{Name: "third", EnvExports: renderer, EnvExportOrder: 9},
		{Name: "no-exports", EnvExportOrder: 1},
		{Name: "second", EnvExports: renderer, EnvExportOrder: 5},
		{Name: "tie-a", EnvExports: renderer},
		{Name: "tie-b", EnvExports: renderer},
	}
	defer func() { Table = original }()

	var got []string
	for _, row := range EnvExportRows() {
		got = append(got, row.Name)
	}

	want := []string{"tie-a", "tie-b", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("EnvExportRows() = %v, want %v", got, want)
	}
	for i, name := range want {
		if got[i] != name {
			t.Errorf("row %d = %q, want %q (full order %v)", i, got[i], name, got)
		}
	}
}

// The rendered export file must lead with go's exports even though npm
// precedes go in Table (issue #3181).
func TestEnvExportRows_GoBeforeNpm(t *testing.T) {
	indexIn := func(rows []Row, name string) int {
		for i, row := range rows {
			if row.Name == name {
				return i
			}
		}
		return -1
	}

	if goIdx, npmIdx := indexIn(Table, "go"), indexIn(Table, "npm"); goIdx < npmIdx {
		t.Fatalf("Table has go (%d) before npm (%d); this test only proves something while Table's order is the opposite", goIdx, npmIdx)
	}

	rows := EnvExportRows()
	goIdx, npmIdx := indexIn(rows, "go"), indexIn(rows, "npm")
	if goIdx < 0 || npmIdx < 0 {
		t.Fatalf("EnvExportRows() missing go (%d) or npm (%d)", goIdx, npmIdx)
	}
	if goIdx > npmIdx {
		t.Errorf("EnvExportRows() has go at %d, npm at %d, want go first", goIdx, npmIdx)
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

// A row added without a classification would silently carry a blank nudge
// string.
func TestTable_ClassificationNonEmpty(t *testing.T) {
	for _, row := range Table {
		if row.Classification == "" {
			t.Errorf("row %q has empty classification", row.Name)
		}
	}
}

// The want map covers the empty paths too, so a row added without a decision
// on its in-tree registry-config path fails here rather than being silently
// excluded or included wherever a consumer filters on the field.
func TestTable_InTreeConfigPath(t *testing.T) {
	want := map[string]string{
		"cargo":  ".cargo/config.toml",
		"npm":    ".npmrc",
		"yarn":   ".yarnrc.yml",
		"pnpm":   "pnpm-workspace.yaml",
		"go":     "",
		"gradle": "",
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

// The nudge takes the first matching row, so a reorder changes which
// ecosystem a mixed repo classifies as. Catch that here, not in the Box.
func TestTable_Order(t *testing.T) {
	want := []string{"cargo", "npm", "yarn", "pnpm", "go", "gradle"}
	if len(Table) != len(want) {
		t.Fatalf("got %d rows, want %d", len(Table), len(want))
	}
	for i, name := range want {
		if Table[i].Name != name {
			t.Errorf("row %d = %q, want %q", i, Table[i].Name, name)
		}
	}
}

// Nothing else in this package asserts on the collected slice's contents, so
// a row the collector forgets to gather would fail silently: New would just
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

// Without this pin, dropping npmRow's RewriteRows shows up only indirectly,
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

// A row added to Table between cargo and gradle, or a reorder, must fail
// here rather than pass silently.
func TestHomeConfigRows_CargoThenGradle(t *testing.T) {
	var got []string
	for _, row := range HomeConfigRows() {
		got = append(got, row.Name)
	}
	want := []string{"cargo", "gradle"}
	if len(got) != len(want) {
		t.Fatalf("HomeConfigRows() = %v, want %v", got, want)
	}
	for i, name := range want {
		if got[i] != name {
			t.Errorf("row %d = %q, want %q", i, got[i], name)
		}
	}
}

// The cargo row's Render must be CargoConfigTOML itself, not a
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

// The gradle row's Render must be GradleInitScript itself, not a
// reimplementation of it.
func TestGradleRowHomeConfig(t *testing.T) {
	row := rowByName(t, "gradle")
	if row.HomeConfig == nil {
		t.Fatal("gradle row has nil HomeConfig")
	}
	hc := row.HomeConfig
	if hc.HomeEnvVar != "GRADLE_USER_HOME" {
		t.Errorf("HomeEnvVar = %q, want %q", hc.HomeEnvVar, "GRADLE_USER_HOME")
	}
	if hc.HomeRelativeDefault != ".gradle" {
		t.Errorf("HomeRelativeDefault = %q, want %q", hc.HomeRelativeDefault, ".gradle")
	}
	if hc.ConfigPath != "init.d/spindrift-registry-proxy.init.gradle" {
		t.Errorf("ConfigPath = %q, want %q", hc.ConfigPath, "init.d/spindrift-registry-proxy.init.gradle")
	}
	if hc.Render == nil {
		t.Fatal("gradle row HomeConfig has nil Render")
	}
	got := hc.Render(27182, "r0", nil)
	want := GradleInitScript(27182, "r0", nil)
	if got != want {
		t.Errorf("Render(27182, %q) = %q, want %q", "r0", got, want)
	}
}

// Only cargo and gradle write a home-level config. A row gaining or losing
// one otherwise just changes HomeConfigRows' count silently.
func TestTable_HomeConfigPresence(t *testing.T) {
	want := map[string]bool{
		"cargo":  true,
		"npm":    false,
		"yarn":   false,
		"pnpm":   false,
		"go":     false,
		"gradle": true,
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

// allDeclaredRoutes declares a path for every ecosystem a row binds an env
// var for, so one route renders all four bindings at once. go declares its
// path in the ecosystems block; the npm family declares theirs as tagged
// enforced paths.
func allDeclaredRoutes() []registrymanifest.Route {
	return []registrymanifest.Route{{
		Prefix:     "r0",
		Ecosystems: registryvocab.RouteEcosystems{"go": registryvocab.RouteDeclaration{"path": "/go"}},
		EnforcedPaths: []registryvocab.Subtree{
			{Ecosystem: "npm", Path: "/npm"},
			{Ecosystem: "pnpm", Path: "/pnpm"},
			{Ecosystem: "yarn", Path: "/yarn"},
			{Ecosystem: "go", Path: "/go"},
		},
	}}
}

// Some rows carry a retired top-level routes-file key (ADR 0047, issue
// #3261) predating [routes.ecosystems.<name>] (issue #3403). A row gaining
// or losing the key must fail here.
func TestTable_RetiredRouteKeyPresence(t *testing.T) {
	want := map[string]string{
		"cargo":  "cargo-registries",
		"npm":    "",
		"yarn":   "",
		"pnpm":   "",
		"go":     "go-path",
		"gradle": "gradle-path",
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
// every declared key and reject one no row declares.
func TestRowByRetiredRouteKey(t *testing.T) {
	for key, wantName := range map[string]string{
		"cargo-registries": "cargo",
		"gradle-path":      "gradle",
		"go-path":          "go",
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
