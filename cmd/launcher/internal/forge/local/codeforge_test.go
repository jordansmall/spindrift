package local

import "testing"

func TestIntegrationBranch(t *testing.T) {
	if got, want := IntegrationBranch("1694"), "integration/1694"; got != want {
		t.Fatalf("IntegrationBranch(1694) = %q, want %q", got, want)
	}
}
