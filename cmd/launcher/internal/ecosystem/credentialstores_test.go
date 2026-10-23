package ecosystem

import (
	"reflect"
	"sort"
	"testing"

	"spindrift.dev/launcher/internal/credresolver"
)

func TestCredentialStoresPinsTodaysQueries(t *testing.T) {
	got := CredentialStores(Table)
	if len(got) != len(Table) {
		t.Fatalf("CredentialStores has %d rows, Table has %d", len(got), len(Table))
	}
	for _, row := range Table {
		want := []string{"gradle-properties", "netrc", "npmrc"}
		stores := append([]string(nil), got[row.Name]...)
		sort.Strings(stores)
		if !reflect.DeepEqual(stores, want) {
			t.Errorf("%s: stores = %v, want %v", row.Name, stores, want)
		}
	}
}

func TestCredentialStoresAreStoreKinds(t *testing.T) {
	storeKeys := map[string]bool{}
	for _, k := range credresolver.StoreKinds() {
		storeKeys[k.SourceKey] = true
	}
	for _, row := range Table {
		for _, key := range row.CredentialStores {
			if !storeKeys[key] {
				t.Errorf("%s: %q is not a credresolver store kind", row.Name, key)
			}
		}
	}
}

func TestCredentialStoresReturnsCopies(t *testing.T) {
	CredentialStores(Table)[Npm.Name][0] = "mutated"
	if CredentialStores(Table)[Npm.Name][0] == "mutated" {
		t.Fatal("mutating the returned slice reached Table")
	}
}
