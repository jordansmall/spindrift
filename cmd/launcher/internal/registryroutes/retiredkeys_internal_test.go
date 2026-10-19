package registryroutes

import (
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
)

// mergeRetiredRouteEcosystems panics on a key that resolves to no
// ecosystem.Table row, so this test is what keeps that panic unreachable: only
// a row dropping its RetiredRouteKey out from under retiredRouteKeys could
// trip it.
func TestRetiredRouteKeysResolveToRows(t *testing.T) {
	for _, entry := range retiredRouteKeys {
		if _, ok := ecosystem.RowByRetiredRouteKey(entry.key); !ok {
			t.Errorf("retiredRouteKeys key %q does not resolve to any ecosystem.Table row", entry.key)
		}
	}
}

// A Go struct tag cannot reference a const, so this test is the only thing
// keeping rawRoute's toml tags for the three retired keys and the ecosystem
// consts that own their spelling from drifting apart.
func TestRawRouteRetiredKeyTagsMatchEcosystemConsts(t *testing.T) {
	cases := []struct {
		field string
		want  string
	}{
		{"CargoRegistries", ecosystem.CargoRetiredRouteKey},
		{"GradlePath", ecosystem.GradleRetiredRouteKey},
		{"GoPath", ecosystem.GoRetiredRouteKey},
	}

	rt := reflect.TypeOf(rawRoute{})
	for _, tc := range cases {
		field, ok := rt.FieldByName(tc.field)
		if !ok {
			t.Errorf("rawRoute has no field %s", tc.field)
			continue
		}
		if got := field.Tag.Get("toml"); got != tc.want {
			t.Errorf("rawRoute.%s toml tag = %q, want %q", tc.field, got, tc.want)
		}
	}
}
