package registrydiscover

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/credresolver"
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registryvocab"
	"spindrift.dev/launcher/internal/testutil"
)

// This test pins storeLookupConfig to the credresolver kind table rather than a
// parallel copy: every kind must get back exactly what its own StoreConfig
// produces from the same path and facts.
func TestStoreLookupConfig_MatchesKindStoreConfig(t *testing.T) {
	d := ecosystem.Declaration{
		Host:            "Registry.Example.com:443",
		UpstreamBaseURL: "https://registry.example.com:443/index",
		RegistryName:    "mycorp",
	}
	facts := credresolver.StoreFacts{
		Host:            d.Host,
		HostKey:         registryvocab.HostKey(d.Host),
		UpstreamBaseURL: d.UpstreamBaseURL,
		RegistryName:    d.RegistryName,
	}

	for _, kind := range credresolver.StoreKinds() {
		t.Run(kind.SourceKey, func(t *testing.T) {
			path := "/fake/path/for/" + kind.SourceKey
			got, err := storeLookupConfig(Store{Name: kind.SourceKey, Path: path}, d)
			if err != nil {
				t.Fatalf("storeLookupConfig: unexpected error: %v", err)
			}
			want := kind.StoreConfig(path, facts)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("storeLookupConfig = %+v, want %+v", got, want)
			}
		})
	}
}

// lookupNetrc runs StoreLookup for registry.example.com against a netrc store
// holding content. netrc stands in for every store kind here: the lookup glue
// is kind-agnostic, and each format's own parsing is pinned in credresolver's
// tests and, end to end per Strategy, in the ecosystem corpus's stores/ cases.
func lookupNetrc(t *testing.T, content string) bool {
	t.Helper()
	dir := t.TempDir()
	testutil.WriteFixture(t, dir, "netrc", content)

	store := Store{Name: "netrc", Path: filepath.Join(dir, "netrc")}
	d := ecosystem.Declaration{Host: "registry.example.com", UpstreamBaseURL: "https://registry.example.com/index"}

	found, err := StoreLookup(store, d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return found
}

func TestStoreLookup_NetrcMatch(t *testing.T) {
	if !lookupNetrc(t, "machine registry.example.com\nlogin alice\npassword s3kr3t\n") {
		t.Error("found = false, want true")
	}
}

// A miss answers found=false with a nil error. It is an ordinary "this store
// doesn't have it", not a discovery-halting error.
func TestStoreLookup_NetrcNoMatch(t *testing.T) {
	if lookupNetrc(t, "machine other.example.com\nlogin alice\npassword s3kr3t\n") {
		t.Error("found = true, want false")
	}
}

// Discovery legitimately runs against stores the operator has not populated, so
// a missing file must not abort the search of the remaining stores. See
// firstMatch.
func TestStoreLookup_MissingStoreFileIsNotFoundNilError(t *testing.T) {
	d := ecosystem.Declaration{
		Host:            "registry.example.com",
		UpstreamBaseURL: "https://registry.example.com/index",
		RegistryName:    "mycorp",
	}
	for _, kind := range storeKinds() {
		t.Run(kind.SourceKey, func(t *testing.T) {
			store := Store{Name: kind.SourceKey, Path: filepath.Join(t.TempDir(), "does-not-exist")}

			found, err := StoreLookup(store, d)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if found {
				t.Error("found = true, want false")
			}
		})
	}
}

func TestStoreLookup_UnknownStoreNameIsError(t *testing.T) {
	store := Store{Name: "bogus", Path: "/dev/null"}
	d := ecosystem.Declaration{Host: "registry.example.com", UpstreamBaseURL: "https://registry.example.com/index"}

	_, err := StoreLookup(store, d)
	if err == nil {
		t.Fatal("expected error for unknown store name, got nil")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("expected error to mention the store name %q, got: %v", "bogus", err)
	}
}

// StoreLookup's signature has no return slot for the resolved value, only the
// bool, so a matching store holding a sentinel token cannot leak it to the
// caller. This test pins that shape against a future signature change.
func TestStoreLookup_NeverReturnsCredentialValue(t *testing.T) {
	const sentinel = "DO-NOT-LEAK-s3kr3t"
	if !lookupNetrc(t, "machine registry.example.com\nlogin alice\npassword "+sentinel+"\n") {
		t.Error("found = false, want true")
	}
}
