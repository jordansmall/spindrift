package main

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestReadOnlyCapabilityGate_ReadWriteIsNoOp verifies that
// checkReadOnlyCapabilityGate never inspects cf/it when
// BOX_FORGE_AND_ISSUE_ACCESS is read-write (the default) — read-write must
// stay a complete no-op regardless of which forge/tracker shape is selected.
func TestReadOnlyCapabilityGate_ReadWriteIsNoOp(t *testing.T) {
	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-write"
	fc := forge.NewFake() // github-shaped: PRForge, no BundleRelay, no DraftPRCreator
	if err := checkReadOnlyCapabilityGate(c, fc, fc); err != nil {
		t.Errorf("checkReadOnlyCapabilityGate() with read-write = %v, want nil", err)
	}
}

// TestReadOnlyCapabilityGate_GitHubShapedForgeFails verifies that read-only
// is rejected at startup for a PR-shaped Code Forge (github's shape) that
// implements neither BundleRelay nor DraftPRCreator yet — the acceptance
// criterion "github fails the gate until the host-mediation seams land".
func TestReadOnlyCapabilityGate_GitHubShapedForgeFails(t *testing.T) {
	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-only"
	fc := forge.NewFake()
	err := checkReadOnlyCapabilityGate(c, fc, fc)
	if err == nil {
		t.Fatal("checkReadOnlyCapabilityGate() = nil, want an error naming the missing seam")
	}
	if !strings.Contains(err.Error(), "BOX_FORGE_AND_ISSUE_ACCESS") {
		t.Errorf("error should mention BOX_FORGE_AND_ISSUE_ACCESS, got: %v", err)
	}
	if !strings.Contains(err.Error(), "bundle-relay") {
		t.Errorf("error should name the missing bundle-relay seam, got: %v", err)
	}
}

// TestReadOnlyCapabilityGate_LocalShapedForgeSatisfies verifies that
// read-only is permitted for a local-shaped Code Forge (BundleRelay, no
// PRForge) paired with a tracker that implements HostPostedCommenter — the
// acceptance criterion "local backends satisfy the gate (inherently
// read-only)". A local-shaped forge has no PR concept at all, so it needs no
// DraftPRCreator to pass.
func TestReadOnlyCapabilityGate_LocalShapedForgeSatisfies(t *testing.T) {
	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-only"
	c.codeForge = "local"
	fc := forge.NewFake()
	cf := fc.AsLocal()
	if err := checkReadOnlyCapabilityGate(c, cf, fc); err != nil {
		t.Errorf("checkReadOnlyCapabilityGate() with local-shaped forge = %v, want nil", err)
	}
}

// prForgeWithBundleRelay wraps *forge.Fake (promoting its full CodeForge and
// PRForge method sets) and adds RelayBundle, giving a synthetic Code Forge
// shape no real adapter has yet — PRForge and BundleRelay both, but not
// DraftPRCreator — so the gate's middle branch (a PR-shaped forge that also
// needs host-side draft-PR-create) has a fixture to exercise.
type prForgeWithBundleRelay struct {
	*forge.Fake
}

func (p prForgeWithBundleRelay) RelayBundle(outboxDir, ref string) error { return nil }

// TestReadOnlyCapabilityGate_PRForgeWithBundleRelayButNoDraftPRCreatorFails
// verifies that a PR-shaped forge implementing BundleRelay still fails the
// gate when it lacks DraftPRCreator: BundleRelay alone is not enough for a
// forge with an open-PR concept, since the Box can no longer `gh pr create`
// itself under read-only.
func TestReadOnlyCapabilityGate_PRForgeWithBundleRelayButNoDraftPRCreatorFails(t *testing.T) {
	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-only"
	fc := forge.NewFake()
	cf := prForgeWithBundleRelay{fc}
	if _, ok := any(cf).(forge.DraftPRCreator); ok {
		t.Fatal("test fixture unexpectedly implements DraftPRCreator")
	}

	err := checkReadOnlyCapabilityGate(c, cf, fc)
	if err == nil {
		t.Fatal("checkReadOnlyCapabilityGate() = nil, want an error naming the missing seam")
	}
	if !strings.Contains(err.Error(), "draft-PR-create") {
		t.Errorf("error should name the missing draft-PR-create seam, got: %v", err)
	}
}

// TestReadOnlyCapabilityGate_PushOnlyForgeFails verifies that read-only is
// rejected for a push-only Code Forge (git's shape: no PRForge, no
// BundleRelay either) — git is out of scope until it implements the seams.
func TestReadOnlyCapabilityGate_PushOnlyForgeFails(t *testing.T) {
	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-only"
	c.codeForge = "git"
	fc := forge.NewFake()
	cf := fc.AsPushOnly()
	err := checkReadOnlyCapabilityGate(c, cf, fc)
	if err == nil {
		t.Fatal("checkReadOnlyCapabilityGate() = nil, want an error naming the missing seam")
	}
	if !strings.Contains(err.Error(), "bundle-relay") {
		t.Errorf("error should name the missing bundle-relay seam, got: %v", err)
	}
}
