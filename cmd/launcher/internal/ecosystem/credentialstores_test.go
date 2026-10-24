package ecosystem

import "testing"

func TestCredentialStoresReturnsCopies(t *testing.T) {
	rows := []Strategy{{Name: "stub", CredentialStores: []string{"netrc"}}}
	CredentialStores(rows)["stub"][0] = "mutated"
	if got := CredentialStores(rows)["stub"][0]; got != "netrc" {
		t.Fatalf("mutating the returned slice reached the Strategy: stores[0] = %q", got)
	}
}
