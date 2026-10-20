package registrydiscover

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystemtest/synthetic"
	"spindrift.dev/launcher/internal/testutil"
)

// UncoveredHostsRows must surface Extract's error instead of swallowing it. The
// synthetic parser never fails, so the row under test swaps in one that does;
// the real parsers' malformed-input errors are pinned in the ecosystem package.
func TestUncoveredHostsRows_ExtractErrorReturnsError(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFixture(t, dir, cfgPath, "anything")
	failing := synthetic.Row
	failing.ConfigParser = func(string) ([]ecosystem.Declaration, bool, error) {
		return nil, false, errors.New("parse failed")
	}

	got, err := UncoveredHostsRows(dir, nil, []ecosystem.Row{failing})
	if err == nil {
		t.Fatal("UncoveredHostsRows() succeeded, want the parser's error")
	}
	if got != nil {
		t.Errorf("UncoveredHostsRows() = %v, want nil on error", got)
	}
}

// Two config files (one per row) declaring the same host must produce one
// entry, the first occurrence. Discover's own loop applies the same dedup.
func TestUncoveredHostsRows_DuplicateDeclaredHostDedupedToOne(t *testing.T) {
	dir := t.TempDir()
	twin := synthetic.Twin("synthetic-twin")
	testutil.WriteFixture(t, dir, cfgPath, "a = https://registry.same.example.com/\n")
	testutil.WriteFixture(t, dir, twin.InTreeConfigPath, "b = https://registry.same.example.com\n")

	got, err := UncoveredHostsRows(dir, nil, []ecosystem.Row{synthetic.Row, twin})
	if err != nil {
		t.Fatalf("UncoveredHostsRows: unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != "registry.same.example.com" {
		t.Errorf("UncoveredHostsRows() = %v, want [registry.same.example.com] deduped to one entry", got)
	}
}

// A declared host with no matching covered entry is the drift doctor row's
// core signal (issue #3144 slice 2).
func TestUncoveredHostsRows_DeclaredHostNotInCovered_ReturnsHost(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFixture(t, dir, cfgPath, hostDecl)

	got, err := UncoveredHostsRows(dir, nil, syntheticRows)
	if err != nil {
		t.Fatalf("UncoveredHostsRows: unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != "host.example.com" {
		t.Errorf("UncoveredHostsRows() = %v, want [host.example.com]", got)
	}
}

// Coverage matching runs through the same registryvocab.HostKey
// normalization Discover and registryroutes.Parse apply, so a covered
// MatchHost differing only in case or an explicit default port still counts
// as coverage.
func TestUncoveredHostsRows_DeclaredHostCoveredCaseAndPortNormalized_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFixture(t, dir, cfgPath, hostDecl)

	got, err := UncoveredHostsRows(dir, []string{"HOST.Example.com:443"}, syntheticRows)
	if err != nil {
		t.Fatalf("UncoveredHostsRows: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("UncoveredHostsRows() = %v, want none (covered host normalizes to a match)", got)
	}
}

// A tree with none of the rows' config files, such as no checkout or a
// checkout naming no registry, yields no uncovered hosts rather than an
// error: nothing declared means nothing to be uncovered.
func TestUncoveredHostsRows_NoDeclarations_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()

	got, err := UncoveredHostsRows(dir, nil, syntheticRows)
	if err != nil {
		t.Fatalf("UncoveredHostsRows: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("UncoveredHostsRows() = %v, want none", got)
	}
}

// This test pins the shared-engine acceptance criterion of issue #3144.
// Discover and UncoveredHostsRows read their declared hosts off the same Extract
// call, so with zero routes configured every host Discover proposes a route
// for must come back uncovered, and covering exactly those routes' own
// MatchHost values must leave nothing uncovered.
func TestUncoveredHostsRows_ConsistentWithDiscover_SameTree(t *testing.T) {
	dir := t.TempDir()
	twin := synthetic.Twin("synthetic-twin")
	rows := []ecosystem.Row{synthetic.Row, twin}
	testutil.WriteFixture(t, dir, cfgPath, "mycorp = https://one.example.com/index/\n")
	testutil.WriteFixture(t, dir, twin.InTreeConfigPath, "other = https://two.example.com/\n")

	lookup := func(store Store, d ecosystem.Declaration) (bool, error) { return false, nil }
	probe := func(upstreamBaseURL string) string { return "bearer" }
	stores := map[string][]string{synthetic.Row.Name: nil, twin.Name: nil}

	routes, _, err := DiscoverRows(dir, rows, nil, stores, lookup, probe)
	if err != nil {
		t.Fatalf("DiscoverRows: unexpected error: %v", err)
	}
	var discoveredHosts []string
	for _, r := range routes {
		discoveredHosts = append(discoveredHosts, r.MatchHost)
	}

	uncoveredNone, err := UncoveredHostsRows(dir, nil, rows)
	if err != nil {
		t.Fatalf("UncoveredHostsRows: unexpected error: %v", err)
	}
	if len(uncoveredNone) != len(discoveredHosts) {
		t.Fatalf("UncoveredHostsRows(nil) = %v, want the same hosts DiscoverRows proposed routes for: %v", uncoveredNone, discoveredHosts)
	}
	for _, h := range discoveredHosts {
		found := false
		for _, u := range uncoveredNone {
			if u == h {
				found = true
			}
		}
		if !found {
			t.Errorf("DiscoverRows proposed a route for %q, but UncoveredHostsRows(nil) = %v doesn't name it", h, uncoveredNone)
		}
	}

	uncoveredAll, err := UncoveredHostsRows(dir, discoveredHosts, rows)
	if err != nil {
		t.Fatalf("UncoveredHostsRows: unexpected error: %v", err)
	}
	if len(uncoveredAll) != 0 {
		t.Errorf("UncoveredHostsRows(discoveredHosts) = %v, want none once every discovered host is covered", uncoveredAll)
	}
}
