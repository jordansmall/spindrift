package github

import (
	_ "embed"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

//go:embed testdata/fake-gh-hostmediation.sh
var fakeGHHostMediation string

// hostMediationHarness is a forgetest.HostMediationHarness backed by a real
// bare git repo (RelayBundle's genuine push target) plus a scripted `gh`
// stand-in for repo-clone/pr-create/issue-comment, mirroring relay_test.go's
// newRelayHarness.
type hostMediationHarness struct {
	t          *testing.T
	repo       *forgetest.GitRepoFixture
	stateDir   string
	base       string
	cf         forge.CodeForge
	tr         forge.IssueTracker
	relayedSHA map[string]string
}

func newHostMediationHarness(t *testing.T) *hostMediationHarness {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")

	repo := forgetest.NewGitRepoFixture(t, "main")
	stateDir := t.TempDir()

	scriptDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(scriptDir, "gh"), []byte(fakeGHHostMediation), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", scriptDir+":"+os.Getenv("PATH"))
	t.Setenv("REMOTE", repo.Bare)
	t.Setenv("STATE_DIR", stateDir)

	return &hostMediationHarness{
		t:          t,
		repo:       repo,
		stateDir:   stateDir,
		base:       "main",
		cf:         NewReadOnlyCodeForge("owner/repo", forge.DispatchLabels{}, "agent/issue-"),
		tr:         NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-"),
		relayedSHA: map[string]string{},
	}
}

func (h *hostMediationHarness) CodeForge() forge.CodeForge  { return h.cf }
func (h *hostMediationHarness) Tracker() forge.IssueTracker { return h.tr }

func (h *hostMediationHarness) SeedBundle(ref string) (outboxDir string) {
	outbox := h.t.TempDir()
	h.relayedSHA[ref] = forgetest.SeedRelayBundle(h.t, h.repo.Bare, h.base, outbox, ref)
	return outbox
}

func (h *hostMediationHarness) BundleLanded(ref string) bool {
	want, ok := h.relayedSHA[ref]
	if !ok {
		return false
	}
	return forgetest.RevParse(h.t, h.repo.Bare, "refs/heads/"+ref) == want
}

func (h *hostMediationHarness) EmptyOutbox() (outboxDir string) {
	return h.t.TempDir()
}

func (h *hostMediationHarness) SeedDraftPRHead(failing bool) (head string) {
	if failing {
		return "fail-head"
	}
	return "agent/issue-hmpr1"
}

func (h *hostMediationHarness) SeedCommentTarget(failing bool) (num string) {
	if failing {
		return "fail-comment"
	}
	return "801"
}

func (h *hostMediationHarness) CommentPosted(num, body string) bool {
	data, err := os.ReadFile(filepath.Join(h.stateDir, "comments", num))
	if err != nil {
		return false
	}
	return strings.Contains(string(data), body)
}

func TestReadOnlyCodeForge_HostMediationContract(t *testing.T) {
	forgetest.RunHostMediationContract(t, newHostMediationHarness(t))
}
