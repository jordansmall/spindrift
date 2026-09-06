package ecosystem

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/registryvocab"
)

// TestCargoRowRouteDeclaration_AcceptsValidRegistriesList covers the
// happy path: a "registries" key whose value is a []any of strings passes
// with no error.
func TestCargoRowRouteDeclaration_AcceptsValidRegistriesList(t *testing.T) {
	row := rowByName(t, nameCargo)
	if row.RouteDeclaration == nil {
		t.Fatal("cargo row has nil RouteDeclaration")
	}

	if err := row.RouteDeclaration("registries", []any{"internal", "crates-remote"}); err != nil {
		t.Errorf("RouteDeclaration(registries, valid list) = %v, want nil", err)
	}
}

// TestCargoRowRouteDeclaration_RejectsUnknownKey covers a key other than
// "registries". Per RouteDeclarationValidator's contract the returned error
// is a bare noun-phrase -- it must not itself echo the key, since the
// caller prefixes the operator's own spelling of it.
func TestCargoRowRouteDeclaration_RejectsUnknownKey(t *testing.T) {
	row := rowByName(t, nameCargo)

	err := row.RouteDeclaration("bogus", []any{"internal"})
	if err == nil {
		t.Fatal("RouteDeclaration(bogus, ...) = nil, want error")
	}
	if strings.Contains(err.Error(), "bogus") {
		t.Errorf("RouteDeclaration(bogus, ...) error %q should not echo the key itself (caller prefixes it)", err.Error())
	}
}

// TestCargoRowRouteDeclaration_RejectsNonArrayValue covers a "registries"
// value that isn't a []any at all (e.g. a bare string) -- go-toml would
// decode a scalar TOML value that way, and the hook must reject rather than
// panic on the type assertion.
func TestCargoRowRouteDeclaration_RejectsNonArrayValue(t *testing.T) {
	row := rowByName(t, nameCargo)

	err := row.RouteDeclaration("registries", "not-an-array")
	if err == nil {
		t.Fatal("RouteDeclaration(registries, non-array) = nil, want error")
	}
}

// TestCargoRowRouteDeclaration_RejectsNonStringElement covers a
// "registries" array carrying a non-string element (e.g. a TOML integer).
func TestCargoRowRouteDeclaration_RejectsNonStringElement(t *testing.T) {
	row := rowByName(t, nameCargo)

	err := row.RouteDeclaration("registries", []any{"internal", 42})
	if err == nil {
		t.Fatal("RouteDeclaration(registries, non-string element) = nil, want error")
	}
}

// TestCargoRowRouteDeclaration_RejectsEmptyName covers the moved-over rule:
// an empty registry name is never valid.
func TestCargoRowRouteDeclaration_RejectsEmptyName(t *testing.T) {
	row := rowByName(t, nameCargo)

	err := row.RouteDeclaration("registries", []any{""})
	if err == nil {
		t.Fatal("RouteDeclaration(registries, [\"\"]) = nil, want error")
	}
	if !strings.Contains(err.Error(), "empty string") {
		t.Errorf("RouteDeclaration(registries, [\"\"]) error %q, want it to mention an empty string", err.Error())
	}
}

// TestCargoRowRouteDeclaration_RejectsBadCharsetName covers the moved-over
// charset rule (cargoBareKeyPattern): a name outside [A-Za-z0-9_-] is
// rejected because it ultimately names a CARGO_REGISTRIES_<NAME>_TOKEN
// shell env var.
func TestCargoRowRouteDeclaration_RejectsBadCharsetName(t *testing.T) {
	row := rowByName(t, nameCargo)

	err := row.RouteDeclaration("registries", []any{"bad name!"})
	if err == nil {
		t.Fatal("RouteDeclaration(registries, [\"bad name!\"]) = nil, want error")
	}
	if !strings.Contains(err.Error(), "must match") {
		t.Errorf("RouteDeclaration(registries, [\"bad name!\"]) error %q, want it to mention the pattern", err.Error())
	}
}

// TestCargoRowRouteDeclaration_RejectsDuplicateName covers the moved-over
// dedup rule: the same name repeated within one route's registries list is
// rejected.
func TestCargoRowRouteDeclaration_RejectsDuplicateName(t *testing.T) {
	row := rowByName(t, nameCargo)

	err := row.RouteDeclaration("registries", []any{"internal", "internal"})
	if err == nil {
		t.Fatal("RouteDeclaration(registries, [dup]) = nil, want error")
	}
	if !strings.Contains(err.Error(), "more than once") {
		t.Errorf("RouteDeclaration(registries, [dup]) error %q, want it to mention repetition", err.Error())
	}
}

// TestCargoRouteRegistries_ReadsBlockList pins CargoRouteRegistries as the
// one place a caller past this package reads a route's declared cargo
// registries back out, so it never has to spell "cargo"/"registries" itself
// (ADR 0048).
func TestCargoRouteRegistries_ReadsBlockList(t *testing.T) {
	blocks := registryvocab.RouteEcosystems{
		nameCargo: registryvocab.RouteDeclaration{CargoRouteRegistriesKey: []any{"internal", "crates-remote"}},
	}

	got := CargoRouteRegistries(blocks)
	want := []string{"internal", "crates-remote"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("CargoRouteRegistries(%v) = %v, want %v", blocks, got, want)
	}
}

// TestCargoRouteRegistries_NilForNoCargoBlock covers both "absent" shapes a
// caller might hand in: a block that never declared cargo at all, and a nil
// RouteEcosystems (a route with no per-ecosystem declarations whatsoever).
func TestCargoRouteRegistries_NilForNoCargoBlock(t *testing.T) {
	blocks := registryvocab.RouteEcosystems{nameCargo: registryvocab.RouteDeclaration{}}
	if got := CargoRouteRegistries(blocks); got != nil {
		t.Errorf("CargoRouteRegistries(empty cargo block) = %v, want nil", got)
	}

	if got := CargoRouteRegistries(nil); got != nil {
		t.Errorf("CargoRouteRegistries(nil) = %v, want nil", got)
	}
}

// TestOnlyCargoRowHasRouteDeclaration pins the "nil means no such notion"
// default (ecosystem.go's ConfigParser doc explains the same convention):
// every row but cargo's must carry a nil RouteDeclaration, so the parser
// slice's caller can reject every non-"path" key for a row with no hook
// rather than reading nil as "accept anything". Comparing against cargoRow's
// own Name, not a bare literal, keeps this test correct if cargo's row is
// ever renamed.
func TestOnlyCargoRowHasRouteDeclaration(t *testing.T) {
	for _, row := range Table {
		if row.Name == cargoRow.Name {
			if row.RouteDeclaration == nil {
				t.Errorf("row %q: RouteDeclaration is nil, want cargo's validator", row.Name)
			}
			continue
		}
		if row.RouteDeclaration != nil {
			t.Errorf("row %q: RouteDeclaration is non-nil, want nil (no key beyond \"path\" accepted)", row.Name)
		}
	}
}
