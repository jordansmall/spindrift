package ecosystem

import (
	"testing"

	"spindrift.dev/launcher/internal/registrymanifest"
)

// Strategy order is the export-file order, so the stub's export-less Strategy must be
// skipped without disturbing the rest.
func TestEnvExportRows_FollowsInputOrder(t *testing.T) {
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
	for _, row := range EnvExportRows(rows) {
		got = append(got, row.Name)
	}

	want := []string{"first", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("EnvExportRows() = %v, want %v", got, want)
	}
	for i, name := range want {
		if got[i] != name {
			t.Errorf("row %d = %q, want %q (full order %v)", i, got[i], name, got)
		}
	}
}

func TestHomeConfigRows_KeepsHomeConfigRowsInInputOrder(t *testing.T) {
	rows := []Strategy{
		{Name: "b", HomeConfig: &HomeConfig{}},
		{Name: "none"},
		{Name: "a", HomeConfig: &HomeConfig{}},
	}

	var got []string
	for _, row := range HomeConfigRows(rows) {
		got = append(got, row.Name)
	}

	if len(got) != 2 || got[0] != "b" || got[1] != "a" {
		t.Errorf("HomeConfigRows() = %v, want [b a]", got)
	}
}

// registryroutes.mergeRetiredRouteEcosystems resolves keys through this
// lookup instead of hand-listing ecosystem names, so it must resolve every
// declared key, reject one no Strategy declares, and never resolve "" to a
// Strategy that merely carries no retired key.
func TestRowByRetiredRouteKey(t *testing.T) {
	rows := []Strategy{{Name: "keyless"}, {Name: "keyed", RetiredRouteKey: "keyed-registries"}}

	if row, ok := RowByRetiredRouteKey("keyed-registries", rows); !ok || row.Name != "keyed" {
		t.Errorf(`RowByRetiredRouteKey("keyed-registries", rows) = (%q, %v), want (keyed, true)`, row.Name, ok)
	}
	if _, ok := RowByRetiredRouteKey("npm-registry", rows); ok {
		t.Error(`RowByRetiredRouteKey("npm-registry", rows): ok = true, want false`)
	}
	if _, ok := RowByRetiredRouteKey("", rows); ok {
		t.Error(`RowByRetiredRouteKey("", rows): ok = true, want false`)
	}
}
