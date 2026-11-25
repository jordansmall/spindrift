package connectorforge_test

import (
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorforge/connectorforgetest"
	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/gitexec/gitexectest"
)

// The harness default rewrites the clone URL to a local path with insteadOf, so
// core's credential helper is never consulted. These tests serve git from the
// backend itself, at the exact clone URL, and gate it on the credential: a site
// that scoped the helper to any other URL fails with "could not read Username".

const cloneURLPath = "/" + connectorforgetest.Repo + ".git/"

func TestSeedMirrorAuthenticatesAtTheCloneURL(t *testing.T) {
	h := connectorforgetest.NewHostMediation(t, connectorforgetest.WithGitOverHTTPS())
	dir := filepath.Join(t.TempDir(), "mirror")
	if err := h.Capabilities().MirrorSeeder.SeedMirror(dir); err != nil {
		t.Fatalf("SeedMirror: %v", err)
	}
	if got, want := forgetest.RevParse(t, dir, "refs/heads/main"), forgetest.RevParse(t, h.Repo.Bare, "refs/heads/main"); got != want {
		t.Errorf("mirror main = %s, want %s", got, want)
	}
	gitexectest.RequireAuthOnlyUnder(t, h.GitRequests(), cloneURLPath)
}

func TestRelayBundleAuthenticatesAtTheCloneURL(t *testing.T) {
	h := connectorforgetest.NewHostMediation(t, connectorforgetest.WithGitOverHTTPS())
	const ref = "agent/issue-9101"
	outbox := h.SeedBundle(ref)
	if err := h.Capabilities().BundleRelay.RelayBundle(outbox, ref); err != nil {
		t.Fatalf("RelayBundle: %v", err)
	}
	if !h.BundleLanded(ref) {
		t.Errorf("%s never reached the repository", ref)
	}
	gitexectest.RequireAuthOnlyUnder(t, h.GitRequests(), cloneURLPath)
}

func TestPushBranchAuthenticatesAtTheCloneURL(t *testing.T) {
	h := connectorforgetest.NewHostMediation(t, connectorforgetest.WithGitOverHTTPS())
	src := t.TempDir()
	forgetest.Run(t, "", "clone", h.Repo.Bare, src)
	forgetest.Run(t, src, "checkout", "-b", "work")
	forgetest.WriteFile(t, filepath.Join(src, "feature.txt"), "feature\n")
	forgetest.Run(t, src, "add", "feature.txt")
	forgetest.Run(t, src, "commit", "-m", "feature")
	const branch = "agent/issue-9102"
	if err := h.Capabilities().BranchPusher.PushBranch(src, "work", branch, "main"); err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	if got, want := forgetest.RevParse(t, h.Repo.Bare, "refs/heads/"+branch), forgetest.RevParse(t, src, "work"); got != want {
		t.Errorf("refs/heads/%s = %s, want %s", branch, got, want)
	}
	gitexectest.RequireAuthOnlyUnder(t, h.GitRequests(), cloneURLPath)
}

func TestRoutedMergeAndRebaseAuthenticateAtTheCloneURL(t *testing.T) {
	h := connectorforgetest.New(t, connectorforgetest.WithGitOverHTTPS())
	ref := h.SeedLandable("9103")
	h.AdvanceBase()
	if err := h.Forge().Rebase(ref); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	if !h.Rebased("9103") {
		t.Error("Rebase never pushed the rebased branch")
	}
	gitexectest.RequireAuthOnlyUnder(t, h.GitRequests(), cloneURLPath)

	if err := h.Forge().Merge(ref); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if !h.Landed("9103") {
		t.Error("Merge never landed the branch")
	}
	gitexectest.RequireAuthOnlyUnder(t, h.GitRequests(), cloneURLPath)
}

// The Connector's forge.remote names another repository on the same host.
// Core never asks: its push, bundle relay and mirror seed go to the repository
// it derived, and the credential is offered only under that repository's path.
func TestGitGoesToTheDerivedCloneURLWhateverTheConnectorSaysItIs(t *testing.T) {
	h := connectorforgetest.NewHostMediation(t, connectorforgetest.WithGitOverHTTPS())
	h.Backend.SetRemote("https://"+h.Backend.Host+"/o/other.git", "https://"+h.Backend.Host+"/o/other")
	_, caps, err := h.Compose()
	if err != nil {
		t.Fatal(err)
	}

	if err := caps.MirrorSeeder.SeedMirror(filepath.Join(t.TempDir(), "mirror")); err != nil {
		t.Fatalf("SeedMirror: %v", err)
	}
	const ref = "agent/issue-9104"
	if err := caps.BundleRelay.RelayBundle(h.SeedBundle(ref), ref); err != nil {
		t.Fatalf("RelayBundle: %v", err)
	}
	if !h.BundleLanded(ref) {
		t.Errorf("%s never reached the derived repository", ref)
	}
	src := t.TempDir()
	forgetest.Run(t, "", "clone", h.Repo.Bare, src)
	forgetest.Run(t, src, "checkout", "-b", "work")
	forgetest.WriteFile(t, filepath.Join(src, "feature.txt"), "feature\n")
	forgetest.Run(t, src, "add", "feature.txt")
	forgetest.Run(t, src, "commit", "-m", "feature")
	const branch = "agent/issue-9105"
	if err := caps.BranchPusher.PushBranch(src, "work", branch, "main"); err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	if got, want := forgetest.RevParse(t, h.Repo.Bare, "refs/heads/"+branch), forgetest.RevParse(t, src, "work"); got != want {
		t.Errorf("refs/heads/%s = %s, want %s", branch, got, want)
	}

	gitexectest.RequireAuthOnlyUnder(t, h.GitRequests(), cloneURLPath)
	for _, r := range h.GitRequests() {
		if !strings.HasPrefix(r.Path, cloneURLPath) {
			t.Errorf("git request to %s, want only the derived repository under %s", r.Path, cloneURLPath)
		}
	}
	if n := h.Backend.RemoteRequests(); n != 0 {
		t.Errorf("core asked the Connector for its remote %d times, want 0", n)
	}
}
