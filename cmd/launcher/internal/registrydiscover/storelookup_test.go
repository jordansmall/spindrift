package registrydiscover

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/credresolver"
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registryvocab"
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

func TestStoreLookup_NetrcMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "netrc")
	content := "machine registry.example.com\nlogin alice\npassword s3kr3t\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing test netrc file: %v", err)
	}

	store := Store{Name: "netrc", Path: path}
	d := ecosystem.Declaration{Host: "registry.example.com", UpstreamBaseURL: "https://registry.example.com/index"}

	found, err := StoreLookup(store, d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Error("found = false, want true")
	}
}

// A miss answers found=false with a nil error. It is an ordinary "this store
// doesn't have it", not a discovery-halting error.
func TestStoreLookup_NetrcNoMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "netrc")
	content := "machine other.example.com\nlogin alice\npassword s3kr3t\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing test netrc file: %v", err)
	}

	store := Store{Name: "netrc", Path: path}
	d := ecosystem.Declaration{Host: "registry.example.com", UpstreamBaseURL: "https://registry.example.com/index"}

	found, err := StoreLookup(store, d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("found = true, want false")
	}
}

func TestStoreLookup_NpmrcMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "npmrc")
	content := "//npm.example.com/:_authToken=s3kr3t\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing test npmrc file: %v", err)
	}

	store := Store{Name: "npmrc", Path: path}
	d := ecosystem.Declaration{Host: "npm.example.com", UpstreamBaseURL: "https://npm.example.com"}

	found, err := StoreLookup(store, d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Error("found = false, want true")
	}
}

func TestStoreLookup_NpmrcNoMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "npmrc")
	content := "//other.example.com/:_authToken=s3kr3t\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing test npmrc file: %v", err)
	}

	store := Store{Name: "npmrc", Path: path}
	d := ecosystem.Declaration{Host: "npm.example.com", UpstreamBaseURL: "https://npm.example.com"}

	found, err := StoreLookup(store, d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("found = true, want false")
	}
}

// Cargo keys its credentials on the declaration's registry name, not its host.
func TestStoreLookup_CargoCredentialsMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.toml")
	content := "[registries.mycorp]\ntoken = \"s3kr3t\"\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing test credentials.toml file: %v", err)
	}

	store := Store{Name: "cargo-credentials", Path: path}
	d := ecosystem.Declaration{Host: "cargo.example.com", UpstreamBaseURL: "https://cargo.example.com/index", RegistryName: "mycorp"}

	found, err := StoreLookup(store, d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Error("found = false, want true")
	}
}

func TestStoreLookup_CargoCredentialsNoMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.toml")
	content := "[registries.other]\ntoken = \"s3kr3t\"\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing test credentials.toml file: %v", err)
	}

	store := Store{Name: "cargo-credentials", Path: path}
	d := ecosystem.Declaration{Host: "cargo.example.com", UpstreamBaseURL: "https://cargo.example.com/index", RegistryName: "mycorp"}

	found, err := StoreLookup(store, d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("found = true, want false")
	}
}

// The declaration's host is deliberately mixed case and carries a port: by
// discover's convention the gradle property key is the host with the port
// stripped and lowercased, so a simplified fixture would stop testing that.
func TestStoreLookup_GradlePropertiesMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gradle.properties")
	content := "maven.example.com=s3kr3t\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing test gradle.properties file: %v", err)
	}

	store := Store{Name: "gradle-properties", Path: path}
	d := ecosystem.Declaration{Host: "Maven.Example.com:443", UpstreamBaseURL: "https://maven.example.com:443/repo"}

	found, err := StoreLookup(store, d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Error("found = false, want true")
	}
}

func TestStoreLookup_GradlePropertiesNoMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gradle.properties")
	content := "other.example.com=s3kr3t\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing test gradle.properties file: %v", err)
	}

	store := Store{Name: "gradle-properties", Path: path}
	d := ecosystem.Declaration{Host: "maven.example.com", UpstreamBaseURL: "https://maven.example.com/repo"}

	found, err := StoreLookup(store, d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("found = true, want false")
	}
}

// Discovery legitimately runs against stores the operator has not populated, so
// a missing file must not abort the search of the remaining stores. See
// firstMatch.
func TestStoreLookup_MissingStoreFileIsNotFoundNilError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist")

	store := Store{Name: "netrc", Path: path}
	d := ecosystem.Declaration{Host: "registry.example.com", UpstreamBaseURL: "https://registry.example.com/index"}

	found, err := StoreLookup(store, d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("found = true, want false")
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
	path := filepath.Join(t.TempDir(), "netrc")
	content := "machine registry.example.com\nlogin alice\npassword " + sentinel + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing test netrc file: %v", err)
	}

	store := Store{Name: "netrc", Path: path}
	d := ecosystem.Declaration{Host: "registry.example.com", UpstreamBaseURL: "https://registry.example.com/index"}

	found, err := StoreLookup(store, d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Error("found = false, want true")
	}
}
