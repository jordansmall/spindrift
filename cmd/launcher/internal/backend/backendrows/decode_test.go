package backendrows_test

import (
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
)

func TestDecodeReturnsTheRowsAsASet(t *testing.T) {
	t.Parallel()
	set, err := backendrows.Decode(rows(t, []backendrows.Row{row("zeta", getRepos), row("alpha", getRepos)}), noEnv, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := set.Lookup("alpha")
	if !ok || got.Name != "alpha" || got.Module != "/x/alpha.wasm" {
		t.Errorf("Lookup(alpha) = %+v, %v", got, ok)
	}
	if _, ok := set.Lookup("other"); ok {
		t.Error("Lookup(other) = true, want false")
	}
	var names []string
	for _, r := range set.All() {
		names = append(names, r.Name)
	}
	if want := []string{"alpha", "zeta"}; !slices.Equal(names, want) {
		t.Errorf("All names = %v, want %v", names, want)
	}
}

func TestDecodeOfNoRowsIsTheEmptySet(t *testing.T) {
	t.Parallel()
	for _, raw := range [][]byte{nil, {}, []byte("  \n"), []byte("[]")} {
		set, err := backendrows.Decode(raw, noEnv, nil)
		if err != nil {
			t.Fatal(err)
		}
		if n := len(set.All()); n != 0 {
			t.Errorf("Decode(%q).All() has %d rows", raw, n)
		}
	}
}

func TestZeroSetIsEmpty(t *testing.T) {
	t.Parallel()
	var set backendrows.Set
	if _, ok := set.Lookup("x"); ok {
		t.Error("zero Set has a row")
	}
	if len(set.All()) != 0 || set.BaseError("x") != nil {
		t.Error("zero Set is not empty")
	}
}

func TestDecodeKeepsAnUnusableBaseRowOutOfTheSetWithItsBaseError(t *testing.T) {
	t.Parallel()
	good, site := row("good", getRepos), baseRow(baseSearch)
	raw := rows(t, []backendrows.Row{good, site})
	set, err := backendrows.Decode(raw, envOf("http://site.lan:3000"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := set.Lookup("site"); ok {
		t.Error("row with an unusable base is in the set")
	}
	if err := set.BaseError("site"); err == nil || !strings.Contains(err.Error(), "must be an https URL") {
		t.Errorf("BaseError(site) = %v, want the https error", err)
	}
	if err := set.BaseError("good"); err != nil {
		t.Errorf("BaseError(good) = %v, want nil", err)
	}
	usable, err := backendrows.Decode(raw, envOf("https://site.example.net"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := usable.Lookup("site"); !ok || usable.BaseError("site") != nil {
		t.Error("a usable base row is not applied cleanly")
	}
}

func TestDecodeDropsAGatedOutRowSilently(t *testing.T) {
	t.Parallel()
	gates := map[string]backendrows.Gate{"gated": func(lookup func(string) string) bool { return lookup("GATE_OPEN") != "" }}
	raw := rows(t, []backendrows.Row{row("gated", getRepos), row("plain", getRepos)})
	set, err := backendrows.Decode(raw, noEnv, gates)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := set.Lookup("gated"); ok {
		t.Error("a row whose gate answers false is in the set")
	}
	if set.BaseError("gated") != nil {
		t.Error("a gated-out row carries a base error")
	}
	if _, ok := set.Lookup("plain"); !ok {
		t.Error("an ungated row is dropped")
	}
	open := func(k string) string {
		if k == "GATE_OPEN" {
			return "1"
		}
		return ""
	}
	if set, err = backendrows.Decode(raw, open, gates); err != nil {
		t.Fatal(err)
	}
	if _, ok := set.Lookup("gated"); !ok {
		t.Error("a row whose gate answers true is dropped")
	}
}

func TestDecodeValidatesAGatedOutRow(t *testing.T) {
	t.Parallel()
	bad := row("gated")
	bad.Routes = nil
	gates := map[string]backendrows.Gate{"gated": func(func(string) string) bool { return false }}
	if _, err := backendrows.Decode(rows(t, []backendrows.Row{bad}), noEnv, gates); err == nil {
		t.Error("an invalid row passed validation because its gate refuses it")
	}
}

func TestNewSetHoldsTheRowsGiven(t *testing.T) {
	t.Parallel()
	set := backendrows.NewSet(row("b", getRepos), row("a", getRepos))
	if got := set.All(); len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
		t.Errorf("All = %+v", got)
	}
	if _, ok := set.Lookup("b"); !ok {
		t.Error("Lookup(b) = false")
	}
}
