package main

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/forge/github"
	"spindrift.dev/launcher/internal/forge/local"
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
	if !strings.Contains(err.Error(), "does not implement") {
		t.Errorf("error should say the selected backend does not implement the seam, got: %v", err)
	}
	if !strings.Contains(err.Error(), "the selected CODE_FORGE=") {
		t.Errorf("error should phrase the failure as the selected CODE_FORGE=, got: %v", err)
	}
}

// TestReadOnlyCapabilityGate_LocalShapedForgeSatisfies verifies that
// read-only is permitted for a local-shaped Code Forge (BundleRelay, no
// PRForge) paired with a tracker that implements HostPostedCommenter and
// HostPostedIssueFiler — the acceptance criterion "local backends satisfy
// the gate (inherently read-only)". A local-shaped forge has no PR concept
// at all, so it needs no DraftPRCreator to pass.
func TestReadOnlyCapabilityGate_LocalShapedForgeSatisfies(t *testing.T) {
	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-only"
	c.codeForge = "local"
	fc := forge.NewFake()
	cf := fc.AsLocal()
	it := fc.AsIssueFiler()
	if err := checkReadOnlyCapabilityGate(c, cf, it); err != nil {
		t.Errorf("checkReadOnlyCapabilityGate() with local-shaped forge = %v, want nil", err)
	}
}

// TestReadOnlyCapabilityGate_TrackerWithoutHostPostedIssueFilerFails
// verifies that a tracker implementing HostPostedCommenter but not
// HostPostedIssueFiler still fails the gate — the acceptance criterion
// "read-only startup gate fails fast if the selected tracker lacks
// HostPostedIssueFiler" (issue #2018). A local-shaped Code Forge is paired
// here specifically so the Code Forge side of the gate is already
// satisfied, isolating the failure to the tracker's missing seam.
func TestReadOnlyCapabilityGate_TrackerWithoutHostPostedIssueFilerFails(t *testing.T) {
	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-only"
	c.codeForge = "local"
	fc := forge.NewFake()
	cf := fc.AsLocal()
	if _, ok := any(fc).(forge.HostPostedIssueFiler); ok {
		t.Fatal("test fixture unexpectedly implements HostPostedIssueFiler")
	}

	err := checkReadOnlyCapabilityGate(c, cf, fc)
	if err == nil {
		t.Fatal("checkReadOnlyCapabilityGate() = nil, want an error naming the missing seam")
	}
	if !strings.Contains(err.Error(), "issue-filing") {
		t.Errorf("error should name the missing issue-filing seam, got: %v", err)
	}
	if !strings.Contains(err.Error(), "does not implement") {
		t.Errorf("error should say the selected backend does not implement the seam, got: %v", err)
	}
	if !strings.Contains(err.Error(), "the selected ISSUE_TRACKER=") {
		t.Errorf("error should phrase the failure as the selected ISSUE_TRACKER=, got: %v", err)
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
	if !strings.Contains(err.Error(), "does not implement") {
		t.Errorf("error should say the selected backend does not implement the seam, got: %v", err)
	}
	if !strings.Contains(err.Error(), "the selected CODE_FORGE=") {
		t.Errorf("error should phrase the failure as the selected CODE_FORGE=, got: %v", err)
	}
}

// TestReadOnlyCapabilityGate_GithubReadOnlyAdapterSatisfies verifies the
// closing acceptance criterion of the read-only epic (issue #1919): the real
// github.NewReadOnlyCodeForge adapter — not a synthetic test fixture — now
// implements both BundleRelay (issue #1918) and DraftPRCreator (this issue),
// so the capability gate that #1916 opened for "github + read-only" in
// principle actually passes for the concrete adapter newCodeForge wires up.
func TestReadOnlyCapabilityGate_GithubReadOnlyAdapterSatisfies(t *testing.T) {
	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-only"
	cf := github.NewReadOnlyCodeForge("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	fc := forge.NewFake() // HostPostedCommenter-shaped, per TestFake_ImplementsHostPostedCommenter
	it := fc.AsIssueFiler()
	if err := checkReadOnlyCapabilityGate(c, cf, it); err != nil {
		t.Errorf("checkReadOnlyCapabilityGate() with the real github read-only adapter = %v, want nil", err)
	}
}

// TestReadOnlyCapabilityGate_GithubTrackerSatisfiesHostPostedIssueFiler
// verifies the closing acceptance criterion of issue #2028: the real github
// tracker (github.NewExecClient, ISSUE_TRACKER=github) — not a synthetic
// fake — now implements forge.HostPostedIssueFiler, so the gate's
// issue-filing axis passes for the concrete tracker newIssueTracker wires
// up, not just for forge.Fake.
func TestReadOnlyCapabilityGate_GithubTrackerSatisfiesHostPostedIssueFiler(t *testing.T) {
	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-only"
	c.issueTracker = "github"
	cf := github.NewReadOnlyCodeForge("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	it := github.NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	if err := checkReadOnlyCapabilityGate(c, cf, it); err != nil {
		t.Errorf("checkReadOnlyCapabilityGate() with the real github tracker = %v, want nil", err)
	}
}

// TestReadOnlyCapabilityGate_ForgejoReadOnlyAdapterSatisfies verifies the
// closing acceptance criterion of issue #1964: the real
// forgejo.NewReadOnlyForgejoCodeForge adapter — not a synthetic test fixture
// — implements both BundleRelay and DraftPRCreator, so the capability gate
// passes for the concrete adapter newCodeForge wires up for CODE_FORGE=forgejo,
// mirroring TestReadOnlyCapabilityGate_GithubReadOnlyAdapterSatisfies.
func TestReadOnlyCapabilityGate_ForgejoReadOnlyAdapterSatisfies(t *testing.T) {
	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-only"
	cf := forgejo.NewReadOnlyForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{Repo: "owner/repo"})
	it := forgejo.NewForgejoClient(forgejo.ForgejoConfig{Repo: "owner/repo"})
	if err := checkReadOnlyCapabilityGate(c, cf, it); err != nil {
		t.Errorf("checkReadOnlyCapabilityGate() with the real forgejo read-only adapter = %v, want nil", err)
	}
}

// TestReadOnlyCapabilityGate_ForgejoTrackerSatisfiesHostPostedIssueFiler
// verifies the closing acceptance criterion of issue #1964: the real forgejo
// tracker (forgejo.NewForgejoClient, ISSUE_TRACKER=forgejo) — not a
// synthetic fake — implements forge.HostPostedIssueFiler, so the gate's
// issue-filing axis passes for the concrete tracker newIssueTracker wires
// up, mirroring TestReadOnlyCapabilityGate_GithubTrackerSatisfiesHostPostedIssueFiler.
func TestReadOnlyCapabilityGate_ForgejoTrackerSatisfiesHostPostedIssueFiler(t *testing.T) {
	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-only"
	c.issueTracker = "forgejo"
	cf := forgejo.NewReadOnlyForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{Repo: "owner/repo"})
	it := forgejo.NewForgejoClient(forgejo.ForgejoConfig{Repo: "owner/repo"})
	if err := checkReadOnlyCapabilityGate(c, cf, it); err != nil {
		t.Errorf("checkReadOnlyCapabilityGate() with the real forgejo tracker = %v, want nil", err)
	}
}

// TestReadOnlyCapabilityGate_LocalTrackerSatisfiesHostPostedIssueFiler
// verifies the closing acceptance criterion of issue #2117: the real local
// tracker (local.NewLocalTracker, ISSUE_TRACKER=local) — not a synthetic
// fake — now implements forge.HostPostedIssueFiler (landed alongside
// forge.HostPostedCommenter, which it already satisfied via its base
// Comment method), so the gate's issue-filing axis passes for the concrete
// tracker newIssueTracker wires up for local, not just for forge.Fake or
// the github tracker (#2028).
func TestReadOnlyCapabilityGate_LocalTrackerSatisfiesHostPostedIssueFiler(t *testing.T) {
	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-only"
	c.codeForge = "local"
	c.issueTracker = "local"
	fc := forge.NewFake()
	cf := fc.AsLocal()
	it := local.NewLocalTracker(t.TempDir(), dispatchLabels(c))
	if err := checkReadOnlyCapabilityGate(c, cf, it); err != nil {
		t.Errorf("checkReadOnlyCapabilityGate() with the real local tracker = %v, want nil", err)
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
	if !strings.Contains(err.Error(), "does not implement") {
		t.Errorf("error should say the selected backend does not implement the seam, got: %v", err)
	}
	if !strings.Contains(err.Error(), "the selected CODE_FORGE=") {
		t.Errorf("error should phrase the failure as the selected CODE_FORGE=, got: %v", err)
	}
}
