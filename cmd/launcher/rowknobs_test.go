package main

import (
	"testing"

	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/forge/git"
	"spindrift.dev/launcher/internal/forge/jira"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/runner/bwrap"
	"spindrift.dev/launcher/internal/runner/oci"
	"spindrift.dev/launcher/internal/tuning"
)

// Every rowKnobs key must be an env some backend, Chore, or runner row's
// generated ReadKnobs looks up, and ReadKnobs over the config's lookup must
// succeed: for the backend rows, the latter pins mustBuild's claim that its
// error is unreachable.
func TestRowKnobsKeysAreDeclaredByARow(t *testing.T) {
	c := minimalValidConfig()
	seen := map[string]bool{}
	rec := func(env string) (string, bool) {
		seen[env] = true
		return c.knobLookup(env)
	}
	if _, err := forgejo.ReadKnobs(rec); err != nil {
		t.Errorf("forgejo.ReadKnobs: %v", err)
	}
	if _, err := jira.ReadKnobs(rec); err != nil {
		t.Errorf("jira.ReadKnobs: %v", err)
	}
	if _, err := local.ReadKnobs(rec); err != nil {
		t.Errorf("local.ReadKnobs: %v", err)
	}
	if _, err := tuning.Strategy.Knobs(rec); err != nil {
		t.Errorf("tuning.Strategy.Knobs: %v", err)
	}
	if _, err := git.ReadKnobs(rec); err != nil {
		t.Errorf("git.ReadKnobs: %v", err)
	}
	if _, err := oci.ReadKnobs(oci.Lookup(rec)); err != nil {
		t.Errorf("oci.ReadKnobs: %v", err)
	}
	if _, err := bwrap.ReadKnobs(bwrap.Lookup(rec)); err != nil {
		t.Errorf("bwrap.ReadKnobs: %v", err)
	}
	for key := range rowKnobs {
		if !seen[key] {
			t.Errorf("rowKnobs key %q is not declared by any row", key)
		}
	}
}

// PODMAN_NETWORK is a row knob: it reaches the oci runner through knobLookup,
// not runner.Config.
func TestKnobLookup_ServesPodmanNetwork(t *testing.T) {
	t.Setenv("PODMAN_NETWORK", "pasta")

	if v, _ := loadConfig().knobLookup("PODMAN_NETWORK"); v != "pasta" {
		t.Errorf("PODMAN_NETWORK knob = %q, want pasta", v)
	}
}
