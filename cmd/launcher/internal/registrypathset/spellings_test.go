package registrypathset

import (
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystemtest/synthetic"
	"spindrift.dev/launcher/internal/registryvocab"
	"spindrift.dev/launcher/internal/testutil"
)

func TestDerive_DiscoveryConfigPathsOnlyRowContributesItsPathSet(t *testing.T) {
	row := synthetic.Spellings("spellings", "first.cfg", "Second.cfg")
	dir := t.TempDir()
	testutil.WriteFixture(t, dir, "Second.cfg", "a = https://second.example.com/index\n")

	got, err := Derive(dir, []ecosystem.Strategy{row})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	want := []HostPathSet{{
		Host:     "second.example.com",
		Origin:   "https://second.example.com",
		Subtrees: []registryvocab.Subtree{{Ecosystem: row.Name, Path: "/index", RegistryName: "a"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Derive = %+v, want %+v", got, want)
	}
}
