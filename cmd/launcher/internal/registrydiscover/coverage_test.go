package registrydiscover

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
)

// TestUncoveredHosts_ExtractErrorReturnsError verifies UncoveredHosts
// surfaces Extract's error (here: malformed .cargo/config.toml) rather than
// swallowing it -- the same fixture trick
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

// TestUncoveredHosts_DuplicateDeclaredHostDedupedToOne verifies that the
// same host declared by two different config files (.npmrc and
// .yarnrc.yml) appears exactly once in the uncovered result, first
// occurrence, rather than twice -- the seen-dedup Discover's own loop
// already applies.
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

// TestUncoveredHosts_DeclaredHostNotInCovered_ReturnsHost verifies that a
// host Extract finds declared in the repo, with no matching entry in
// covered, comes back as uncovered -- the drift doctor row's core signal
// (issue #3144 slice 2).
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

// TestUncoveredHosts_DeclaredHostCoveredCaseAndPortNormalized_ReturnsEmpty
// verifies a covered entry matches a declared host through the same
// registryvocab.HostKey normalization Discover and registryroutes.Parse
// both already apply -- a covered MatchHost differing only in case or an
// explicit default port must still count as coverage.
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

// TestUncoveredHosts_NoDeclarations_ReturnsEmpty verifies a repo tree with
// none of Extract's four config files present -- e.g. no checkout, or a
// checkout that names no registry -- yields no uncovered hosts, not an
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

// TestUncoveredHosts_ConsistentWithDiscover_SameTree is the shared-engine
// acceptance test (issue #3144 AC): running Discover and UncoveredHosts
// against the identical tree must agree by construction, since both read
// their declared hosts off the same Extract call. With zero routes
// configured, every host Discover would propose a route for must also come
// back from UncoveredHosts as uncovered; once covered is populated with
// exactly those routes' own MatchHost values, nothing is left uncovered.
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
