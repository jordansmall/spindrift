package registrydiscover

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
)

// UncoveredHosts must surface Extract's error instead of swallowing it. The
// malformed .cargo/config.toml fixture matches the one
// TestRegistryRouteDriftCheckFor_ExtractErrorDegradesProbe uses in
// cmd/launcher/registryroutesdrift_doctor_checks_test.go.
func TestUncoveredHosts_ExtractErrorReturnsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".cargo"), 0o755); err != nil {
		t.Fatal(err)
	}
	malformed := "not valid toml [[["
	if err := os.WriteFile(filepath.Join(dir, ".cargo", "config.toml"), []byte(malformed), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := UncoveredHosts(dir, nil)
	if err == nil {
		t.Fatal("UncoveredHosts() succeeded, want an error for malformed .cargo/config.toml")
	}
	if got != nil {
		t.Errorf("UncoveredHosts() = %v, want nil on error", got)
	}
}

// Two config files (.npmrc and .yarnrc.yml) declaring the same host must
// produce one entry, the first occurrence. Discover's own loop applies the
// same dedup.
func TestUncoveredHosts_DuplicateDeclaredHostDedupedToOne(t *testing.T) {
	dir := t.TempDir()
	npmrc := "registry=https://registry.same.example.com/\n"
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}
	yarnrc := "npmRegistryServer: https://registry.same.example.com\n"
	if err := os.WriteFile(filepath.Join(dir, ".yarnrc.yml"), []byte(yarnrc), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := UncoveredHosts(dir, nil)
	if err != nil {
		t.Fatalf("UncoveredHosts: unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != "registry.same.example.com" {
		t.Errorf("UncoveredHosts() = %v, want [registry.same.example.com] deduped to one entry", got)
	}
}

// A declared host with no matching covered entry is the drift doctor row's
// core signal (issue #3144 slice 2).
func TestUncoveredHosts_DeclaredHostNotInCovered_ReturnsHost(t *testing.T) {
	dir := t.TempDir()
	npmrc := "registry=https://npm.example.com/\n"
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := UncoveredHosts(dir, nil)
	if err != nil {
		t.Fatalf("UncoveredHosts: unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != "npm.example.com" {
		t.Errorf("UncoveredHosts() = %v, want [npm.example.com]", got)
	}
}

// Coverage matching runs through the same registryvocab.HostKey
// normalization Discover and registryroutes.Parse apply, so a covered
// MatchHost differing only in case or an explicit default port still counts
// as coverage.
func TestUncoveredHosts_DeclaredHostCoveredCaseAndPortNormalized_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	npmrc := "registry=https://npm.example.com/\n"
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := UncoveredHosts(dir, []string{"NPM.Example.com:443"})
	if err != nil {
		t.Fatalf("UncoveredHosts: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("UncoveredHosts() = %v, want none (covered host normalizes to a match)", got)
	}
}

// A tree with none of Extract's four config files, such as no checkout or a
// checkout naming no registry, yields no uncovered hosts rather than an
// error: nothing declared means nothing to be uncovered.
func TestUncoveredHosts_NoDeclarations_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()

	got, err := UncoveredHosts(dir, nil)
	if err != nil {
		t.Fatalf("UncoveredHosts: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("UncoveredHosts() = %v, want none", got)
	}
}

// This test pins the shared-engine acceptance criterion of issue #3144.
// Discover and UncoveredHosts read their declared hosts off the same Extract
// call, so with zero routes configured every host Discover proposes a route
// for must come back uncovered, and covering exactly those routes' own
// MatchHost values must leave nothing uncovered.
func TestUncoveredHosts_ConsistentWithDiscover_SameTree(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".cargo"), 0o755); err != nil {
		t.Fatal(err)
	}
	cargoConfig := `
[registries.mycorp]
index = "sparse+https://cargo.example.com/index/"
`
	if err := os.WriteFile(filepath.Join(dir, ".cargo", "config.toml"), []byte(cargoConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	npmrc := "registry=https://npm.example.com/\n"
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	lookup := func(store Store, d ecosystem.Declaration) (bool, error) { return false, nil }
	probe := func(upstreamBaseURL string) string { return "bearer" }

	routes, _, err := Discover(dir, nil, lookup, probe)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	var discoveredHosts []string
	for _, r := range routes {
		discoveredHosts = append(discoveredHosts, r.MatchHost)
	}

	uncoveredNone, err := UncoveredHosts(dir, nil)
	if err != nil {
		t.Fatalf("UncoveredHosts: unexpected error: %v", err)
	}
	if len(uncoveredNone) != len(discoveredHosts) {
		t.Fatalf("UncoveredHosts(nil) = %v, want the same hosts Discover proposed routes for: %v", uncoveredNone, discoveredHosts)
	}
	for _, h := range discoveredHosts {
		found := false
		for _, u := range uncoveredNone {
			if u == h {
				found = true
			}
		}
		if !found {
			t.Errorf("Discover proposed a route for %q, but UncoveredHosts(nil) = %v doesn't name it", h, uncoveredNone)
		}
	}

	uncoveredAll, err := UncoveredHosts(dir, discoveredHosts)
	if err != nil {
		t.Fatalf("UncoveredHosts: unexpected error: %v", err)
	}
	if len(uncoveredAll) != 0 {
		t.Errorf("UncoveredHosts(discoveredHosts) = %v, want none once every discovered host is covered", uncoveredAll)
	}
}
