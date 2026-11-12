package git

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// bundleRelayHarness is a forgetest.BundleRelayHarness backed by a real bare
// git repo, so RelayBundle exercises genuine clone, checkout, and force-push.
type bundleRelayHarness struct {
	t          *testing.T
	repo       *forgetest.GitRepoFixture
	cf         forge.CodeForge
	relayedSHA map[string]string
}

func newBundleRelayHarness(t *testing.T) *bundleRelayHarness {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")

	repo := forgetest.NewGitRepoFixture(t, "main")
	return &bundleRelayHarness{
		t:          t,
		repo:       repo,
		cf:         NewReadOnlyGitClient(repo.Bare, "main", "Test Bot", "bot@example.com"),
		relayedSHA: map[string]string{},
	}
}

func (h *bundleRelayHarness) CodeForge() forge.CodeForge { return h.cf }

func (h *bundleRelayHarness) SeedBundle(ref string) (outboxDir string) {
	outbox := h.t.TempDir()
	h.relayedSHA[ref] = forgetest.SeedRelayBundle(h.t, h.repo.Bare, "main", outbox, ref)
	return outbox
}

func (h *bundleRelayHarness) BundleLanded(ref string) bool {
	want, ok := h.relayedSHA[ref]
	return ok && forgetest.RevParse(h.t, h.repo.Bare, "refs/heads/"+ref) == want
}

func (h *bundleRelayHarness) EmptyOutbox() (outboxDir string) { return h.t.TempDir() }

func TestGit_BundleRelayContract(t *testing.T) {
	forgetest.RunBundleRelayContract(t, newBundleRelayHarness(t))
}

// TestReadOnlyGitClientIsBundleOnly pins that git relays bundles but, having
// no PRs, never advertises the PR pair settle keys read-only handling off.
func TestReadOnlyGitClientIsBundleOnly(t *testing.T) {
	cf := NewReadOnlyGitClient("unused", "main", "n", "e")
	if _, ok := cf.(forge.BundleRelay); !ok {
		t.Error("read-only git CodeForge must implement forge.BundleRelay")
	}
	if _, ok := cf.(forge.DraftPRCreator); ok {
		t.Error("read-only git CodeForge must not implement forge.DraftPRCreator")
	}
	if _, ok := cf.(forge.BundleCommitSubjects); ok {
		t.Error("read-only git CodeForge must not implement forge.BundleCommitSubjects")
	}
	if _, ok := NewGitClient("unused", "main", "n", "e").(forge.BundleRelay); ok {
		t.Error("read-write git CodeForge must not implement forge.BundleRelay")
	}
}
