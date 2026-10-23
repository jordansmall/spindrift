package cargo

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registryvocab"
)

func TestCargoRowRouteDeclaration_AcceptsValidRegistriesList(t *testing.T) {
	row := Strategy
	if row.RouteDeclaration == nil {
		t.Fatal("cargo row has nil RouteDeclaration")
	}

	if err := row.RouteDeclaration("registries", []any{"internal", "crates-remote"}); err != nil {
		t.Errorf("RouteDeclaration(registries, valid list) = %v, want nil", err)
	}
}

// RouteDeclarationValidator's contract requires a bare noun phrase, so the error
// must not echo the key: the caller prefixes the operator's own spelling of it.
func TestCargoRowRouteDeclaration_RejectsUnknownKey(t *testing.T) {
	row := Strategy

	err := row.RouteDeclaration("bogus", []any{"internal"})
	if err == nil {
		t.Fatal("RouteDeclaration(bogus, ...) = nil, want error")
	}
	if strings.Contains(err.Error(), "bogus") {
		t.Errorf("RouteDeclaration(bogus, ...) error %q should not echo the key itself (caller prefixes it)", err.Error())
	}
}

// go-toml decodes a scalar TOML value as a bare string, so the hook must reject
// it rather than panic on the type assertion.
func TestCargoRowRouteDeclaration_RejectsNonArrayValue(t *testing.T) {
	row := Strategy

	err := row.RouteDeclaration("registries", "not-an-array")
	if err == nil {
		t.Fatal("RouteDeclaration(registries, non-array) = nil, want error")
	}
}

func TestCargoRowRouteDeclaration_RejectsNonStringElement(t *testing.T) {
	row := Strategy

	err := row.RouteDeclaration("registries", []any{"internal", 42})
	if err == nil {
		t.Fatal("RouteDeclaration(registries, non-string element) = nil, want error")
	}
}

func TestCargoRowRouteDeclaration_RejectsEmptyName(t *testing.T) {
	row := Strategy

	err := row.RouteDeclaration("registries", []any{""})
	if err == nil {
		t.Fatal("RouteDeclaration(registries, [\"\"]) = nil, want error")
	}
	if !strings.Contains(err.Error(), "empty string") {
		t.Errorf("RouteDeclaration(registries, [\"\"]) error %q, want it to mention an empty string", err.Error())
	}
}

// cargoBareKeyPattern rejects a name outside [A-Za-z0-9_-] because the name
// ends up in a CARGO_REGISTRIES_<NAME>_TOKEN shell env var.
func TestCargoRowRouteDeclaration_RejectsBadCharsetName(t *testing.T) {
	row := Strategy

	err := row.RouteDeclaration("registries", []any{"bad name!"})
	if err == nil {
		t.Fatal("RouteDeclaration(registries, [\"bad name!\"]) = nil, want error")
	}
	if !strings.Contains(err.Error(), "must match") {
		t.Errorf("RouteDeclaration(registries, [\"bad name!\"]) error %q, want it to mention the pattern", err.Error())
	}
}

func TestCargoRowRouteDeclaration_RejectsDuplicateName(t *testing.T) {
	row := Strategy

	err := row.RouteDeclaration("registries", []any{"internal", "internal"})
	if err == nil {
		t.Fatal("RouteDeclaration(registries, [dup]) = nil, want error")
	}
	if !strings.Contains(err.Error(), "more than once") {
		t.Errorf("RouteDeclaration(registries, [dup]) error %q, want it to mention repetition", err.Error())
	}
}

// cargoRouteRegistries is the one place this package reads a route's declared
// cargo registries, so no caller spells the keys itself (ADR 0048).
func TestCargoRouteRegistries_ReadsBlockList(t *testing.T) {
	blocks := registryvocab.RouteEcosystems{
		nameCargo: registryvocab.RouteDeclaration{ecosystem.CargoRouteRegistriesKey: []any{"internal", "crates-remote"}},
	}

	got := cargoRouteRegistries(blocks)
	want := []string{"internal", "crates-remote"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("cargoRouteRegistries(%v) = %v, want %v", blocks, got, want)
	}
}

// Two distinct absent shapes reach this function: a cargo block with no
// registries key, and a route with no per-ecosystem declarations at all.
func TestCargoRouteRegistries_NilForNoCargoBlock(t *testing.T) {
	blocks := registryvocab.RouteEcosystems{nameCargo: registryvocab.RouteDeclaration{}}
	if got := cargoRouteRegistries(blocks); got != nil {
		t.Errorf("cargoRouteRegistries(empty cargo block) = %v, want nil", got)
	}

	if got := cargoRouteRegistries(nil); got != nil {
		t.Errorf("cargoRouteRegistries(nil) = %v, want nil", got)
	}
}
