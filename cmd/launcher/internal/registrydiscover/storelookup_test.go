package registrydiscover

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
)

// TestStoreLookup_NetrcMatch verifies that a netrc store holding an entry for
// the declaration's upstream host answers found=true.
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

// TestStoreLookup_NetrcNoMatch verifies that a netrc store with no entry for
// the declaration's host answers found=false, nil error -- an ordinary
// "this store doesn't have it" miss, not a discovery-halting error.
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

// TestStoreLookup_NpmrcMatch verifies that an npmrc store holding an
// "//host/:_authToken=" entry for the declaration's host answers found=true.
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

// TestStoreLookup_NpmrcNoMatch verifies that an npmrc store with an entry for
// a different host answers found=false.
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

// TestStoreLookup_CargoCredentialsMatch verifies that a cargo-credentials
// store holding a "[registries.NAME]" table for the declaration's cargo
// registry name answers found=true.
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

// TestStoreLookup_CargoCredentialsNoMatch verifies that a cargo-credentials
// store with a table for a different registry name answers found=false.
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

// TestStoreLookup_GradlePropertiesMatch verifies that a gradle-properties
// store holding a property keyed on the declaration's normalized host
// answers found=true -- discover's documented convention: the property key
// is the host, port stripped and lowercased.
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

// TestStoreLookup_GradlePropertiesNoMatch verifies that a gradle-properties
// store with a property for a different key answers found=false.
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

// TestStoreLookup_MissingStoreFileIsNotFoundNilError verifies that a store
// whose file does not exist on disk answers found=false, nil error --
// discovery legitimately runs against stores the operator hasn't populated,
// and a lookup error must never abort the search of the remaining stores
// (see firstMatch).
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

// TestStoreLookup_UnknownStoreNameIsError verifies that a store naming an
// unrecognized format fails with an error naming the store.
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

// TestStoreLookup_NeverReturnsCredentialValue documents the invariant by
// construction: StoreLookup's signature has no return slot for the resolved
// value at all -- only the bool comes back -- so a matching store holding a
// sentinel token can never leak it to the caller, even by accident.
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
	// found is a bool: there is nowhere for the sentinel to have gone.
}
