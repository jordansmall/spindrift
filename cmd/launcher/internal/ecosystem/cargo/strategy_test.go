package cargo

import (
	"reflect"
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/bindregistry"
	"spindrift.dev/launcher/internal/credresolver"
	"spindrift.dev/launcher/internal/ecosystem"
)

// strategies is the list the tests below walk: cargo alone, since an ecosystem
// package never imports another.
var strategies = []ecosystem.Strategy{Strategy}

func TestStrategy_CredentialStores(t *testing.T) {
	want := []string{"cargo-credentials", "gradle-properties", "netrc", "npmrc"}
	got := slices.Sorted(slices.Values(ecosystem.CredentialStores(strategies)[Strategy.Name]))
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stores = %v, want %v", got, want)
	}

	storeKeys := map[string]bool{}
	for _, k := range credresolver.StoreKinds() {
		storeKeys[k.SourceKey] = true
	}
	for _, key := range Strategy.CredentialStores {
		if !storeKeys[key] {
			t.Errorf("%q is not a credresolver store kind", key)
		}
	}
}

// A Strategy that gains or loses one of these fields otherwise fails silently
// wherever a consumer filters on it.
func TestStrategy_Shape(t *testing.T) {
	s := Strategy
	if s.Name != "cargo" || s.Classification != "cargo" {
		t.Errorf("Name, Classification = %q, %q, want cargo, cargo", s.Name, s.Classification)
	}
	if want := []string{"Cargo.lock"}; !slices.Equal(s.LockfileNames, want) {
		t.Errorf("LockfileNames = %v, want %v", s.LockfileNames, want)
	}
	if s.EnvExports != nil {
		t.Error("EnvExports is non-nil, want nil: cargo binds through a file")
	}
	if s.BindingEnvVar != "" {
		t.Errorf("BindingEnvVar = %q, want empty: cargo binds through a file", s.BindingEnvVar)
	}
	if s.RepoAwareHomeConfig == nil {
		t.Error("RepoAwareHomeConfig is nil, want cargo's source-replacement renderer")
	}
	if s.InTreeConfigPath != ".cargo/config.toml" {
		t.Errorf("InTreeConfigPath = %q, want %q", s.InTreeConfigPath, ".cargo/config.toml")
	}
	if s.RetiredRouteKey != "cargo-registries" {
		t.Errorf("RetiredRouteKey = %q, want %q", s.RetiredRouteKey, "cargo-registries")
	}
	if s.RouteDeclaration == nil {
		t.Error("RouteDeclaration is nil, want cargo's validator")
	}
}

// registryroutes resolves the retired cargo-registries key through the registry.
func TestStrategy_ResolvesByRetiredRouteKey(t *testing.T) {
	row, ok := ecosystem.RowByRetiredRouteKey("cargo-registries", strategies)
	if !ok || row.Name != "cargo" {
		t.Errorf(`RowByRetiredRouteKey("cargo-registries") = (%q, %v), want (cargo, true)`, row.Name, ok)
	}
}

// Nothing else asserts on the collected slice's contents, so a Strategy the
// collector forgets to gather would fail silently: New would just never see it.
func TestResponseRewriteRows_ContainsCargoConfigRow(t *testing.T) {
	rows := ecosystem.ResponseRewriteRows(strategies)
	for _, row := range rows {
		if row.Name == "cargo config.json" && row.Ecosystem == Strategy.Name {
			return
		}
	}
	t.Errorf("ResponseRewriteRows() = %+v, want a row named %q tagged %q", rows, "cargo config.json", Strategy.Name)
}

// The only Strategy listed here is cargo, so a second home-config row
// cannot hide behind it.
func TestHomeConfigRows_Cargo(t *testing.T) {
	var got []string
	for _, row := range ecosystem.HomeConfigRows(strategies) {
		got = append(got, row.Name)
	}
	if want := []string{Strategy.Name}; !slices.Equal(got, want) {
		t.Fatalf("HomeConfigRows() = %v, want %v", got, want)
	}
}

// The Strategy's Render must be cargoConfigTOML itself, not a reimplementation
// of it.
func TestStrategy_HomeConfig(t *testing.T) {
	hc := Strategy.HomeConfig
	if hc == nil {
		t.Fatal("cargo Strategy has nil HomeConfig")
	}
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
		t.Fatal("cargo HomeConfig has nil Render")
	}
	got := hc.Render(27182, "r0", nil)
	want := cargoConfigTOML(27182, "r0", nil)
	if got != want {
		t.Errorf("Render(27182, %q) = %q, want %q", "r0", got, want)
	}
}

// Cargo keeps a non-empty InTreeConfigPath for host-side discovery but carries
// a non-nil RepoAwareHomeConfig, so InTreeBindings' filter must exclude it
// (issue #3201). A regression would dual-write the in-tree rewrite and cargo's
// source-replacement home config, which its 1:1 URL to source-name rule cannot
// tolerate.
func TestInTreeBindings_ExcludesCargo(t *testing.T) {
	for _, row := range bindregistry.InTreeBindings(strategies) {
		if row.Name == Strategy.Name {
			t.Fatalf("InTreeBindings(strategies) = %+v, want it to never include the cargo row", bindregistry.InTreeBindings(strategies))
		}
	}
}
