package waves

import "testing"

func TestSeedScope_StringRendersAdapterBranchLabel(t *testing.T) {
	scope := NewSeedScope("beta-engine", "integration/beta-engine")
	if got, want := scope.String(), "integration/beta-engine"; got != want {
		t.Errorf("NewSeedScope(...).String() = %q, want %q", got, want)
	}

	var zero SeedScope
	if got, want := zero.String(), ""; got != want {
		t.Errorf("zero SeedScope.String() = %q, want %q", got, want)
	}
}
