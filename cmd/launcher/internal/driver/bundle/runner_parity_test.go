package bundle_test

import (
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/runner"
)

// familyRegistryDrift reports every disagreement between the runner family
// rows and the registered Strategies, one message per family.
func familyRegistryDrift(rows, registered []string) []string {
	var drift []string
	for _, n := range rows {
		if !slices.Contains(registered, n) {
			drift = append(drift, "runner family "+n+" has no registered Strategy")
		}
	}
	for _, n := range registered {
		if !slices.Contains(rows, n) {
			drift = append(drift, "runner Strategy "+n+" has no matching lib/runners row")
		}
	}
	return drift
}

// The rows behind runner.Families are generated from lib/runners/*.nix; see
// families_gen.go. A new family needs a row and a Strategy the bundle
// registers.
func TestRunnerStrategiesMatchFamilyRows(t *testing.T) {
	var rows []string
	for _, f := range runner.Families {
		rows = append(rows, f.Name)
	}
	for _, d := range familyRegistryDrift(rows, runner.Names()) {
		t.Error(d)
	}
}

func TestFamilyRegistryDriftClassifiesPlantedInput(t *testing.T) {
	if got := familyRegistryDrift([]string{"oci", "bwrap"}, []string{"bwrap", "oci"}); len(got) != 0 {
		t.Errorf("matching sets reported drift: %v", got)
	}
	got := familyRegistryDrift([]string{"oci", "ghost"}, []string{"oci", "orphan"})
	want := []string{
		"runner family ghost has no registered Strategy",
		"runner Strategy orphan has no matching lib/runners row",
	}
	if !slices.Equal(got, want) {
		t.Errorf("drift = %v, want %v", got, want)
	}
}
