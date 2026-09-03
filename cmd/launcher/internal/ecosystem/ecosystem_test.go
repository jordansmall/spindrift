package ecosystem

import "testing"

// stubGetenv returns a getenv closure over a fixed map, standing in for
// os.Getenv without touching real process env -- keeps these table tests
// hermetic struct-literal tests like ComputeGoBindings's own.
func stubGetenv(env map[string]string) func(string) string {
	return func(key string) string { return env[key] }
}

// rowByName fails the test on an unknown name -- a helper misuse, not a
// case worth a table entry of its own.
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

// TestNpmRowEnvExports pins that the npm row's EnvExports renders exactly
// NpmFamilyBindings' three exports and no warnings -- the row value is a
// thin adapter, not a reimplementation.
func TestNpmRowEnvExports(t *testing.T) {
	row := rowByName(t, "npm")
	if row.EnvExports == nil {
		t.Fatal("npm row has nil EnvExports")
	}

	gotExports, gotWarnings := row.EnvExports(27182, "r0", stubGetenv(nil))

	want := NpmFamilyBindings(27182, "r0")
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

// TestGoRowEnvExports pins that the go row's EnvExports reaches its getenv
// parameter into ComputeGoBindings's decision logic, so the row value isn't
// just discarding the snapshot it's handed.
func TestGoRowEnvExports(t *testing.T) {
	row := rowByName(t, "go")
	if row.EnvExports == nil {
		t.Fatal("go row has nil EnvExports")
	}

	overrideEnv := stubGetenv(map[string]string{"GOTOOLCHAIN": "auto"})
	_, gotWarnings := row.EnvExports(27182, "r0", overrideEnv)
	if !containsWarningSubstring(gotWarnings, "GOTOOLCHAIN") {
		t.Errorf("expected a GOTOOLCHAIN override warning, got %v", gotWarnings)
	}

	emptyEnv := stubGetenv(nil)
	gotExports, gotWarnings2 := row.EnvExports(27182, "r0", emptyEnv)
	if len(gotWarnings2) != 0 {
		t.Errorf("expected no warnings with an empty env snapshot, got %v", gotWarnings2)
	}
	if value, ok := exportValue(gotExports, "GOPROXY"); !ok || value != "http://127.0.0.1:27182/r0" {
		t.Errorf("GOPROXY export = (%q, %v), want (%q, true)", value, ok, "http://127.0.0.1:27182/r0")
	}
}

// TestTable_EnvExportsPresence pins which rows carry an EnvExports render
// function and which leave it nil -- a row with exports and no matching
// entry here, or vice versa, fails loudly instead of silently contributing
// nothing or panicking a caller.
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

// TestEnvExportRows_SortsByEnvExportOrderNotTableOrder proves the sort
// rather than the literal table: a stub table whose export-carrying rows sit
// in the exact reverse of their EnvExportOrder still comes back ascending,
// and the nil-renderer row never appears. A tie keeps table order, so the
// ordering is total and deterministic for rows that never set the field.
func TestEnvExportRows_SortsByEnvExportOrderNotTableOrder(t *testing.T) {
	renderer := func(int, string, func(string) string) ([]EnvExport, []string) { return nil, nil }
	// Swapping the package-level Table bars t.Parallel here and in every
	// other test in this package -- a parallel neighbour would observe the stub.
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

// TestEnvExportRows_GoBeforeNpm pins the one historical order the pins exist
// to preserve: the rendered export file leads with go's exports even though
// npm precedes go in Table (issue #3181).
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

// TestTable_NamesUnique verifies no two rows share a name, so a lookup by
// name can never be ambiguous about which row it found.
func TestTable_NamesUnique(t *testing.T) {
	seen := make(map[string]bool, len(Table))
	for _, row := range Table {
		if seen[row.Name] {
			t.Fatalf("duplicate row name %q", row.Name)
		}
		seen[row.Name] = true
	}
}

// TestTable_ClassificationNonEmpty verifies every row resolves to a
// non-empty classification, so a row added without one fails loudly here
// rather than silently carrying a blank nudge string.
func TestTable_ClassificationNonEmpty(t *testing.T) {
	for _, row := range Table {
		if row.Classification == "" {
			t.Errorf("row %q has empty classification", row.Name)
		}
	}
}

// TestTable_InTreeConfigPath pins every row's InTreeConfigPath, including the
// empty ones, so a row added without a decision on its in-tree registry-config
// path fails here rather than silently being excluded (or included) by
// accident wherever a consumer filters on it.
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

// TestTable_Order pins the row order the nudge's first-hit precedence
// depends on, so a reorder that would silently change which ecosystem a
// mixed repo classifies as fails here rather than in the Box.
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

// TestTable_PatternsNonEmptyExceptGradle verifies every row carries at least
// one allowlist pattern, except gradle, whose nil Patterns is deliberate
// (its Binding is a home-level init script, not allowlisted paths) rather
// than an omission.
func TestTable_PatternsNonEmptyExceptGradle(t *testing.T) {
	for _, row := range Table {
		if row.Name == "gradle" {
			if row.Patterns != nil {
				t.Errorf("row %q: want nil Patterns, got %d", row.Name, len(row.Patterns))
			}
			continue
		}
		if len(row.Patterns) == 0 {
			t.Errorf("row %q: want non-empty Patterns", row.Name)
		}
	}
}
