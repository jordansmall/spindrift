package github

import (
	_ "embed"
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// fakeGHState is a stateful stand-in for the gh CLI. It stays a real .sh file
// rather than an inline Go string so the repo's shellcheck sweep keeps
// covering it.
//
//go:embed testdata/fake-gh.sh
var fakeGHState string

// githubHarness implements forgetest.Harness over a STATE_DIR/issues/<num>/
// tree the fakeGHState script reads and mutates, so successive gh calls see
// each other's effects. prependFakeGH's single scripted response cannot do
// that: CompleteVerdict's double-dispatch guard has to observe a label an
// earlier TransitionState call added.
type githubHarness struct {
	issuesDir string
	tr        forge.IssueTracker
}

func newGithubHarness(t *testing.T) *githubHarness {
	t.Helper()
	stateDir := t.TempDir()
	issuesDir := filepath.Join(stateDir, "issues")
	if err := os.MkdirAll(issuesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	scriptDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(scriptDir, "gh"), []byte(fakeGHState), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", scriptDir+":"+os.Getenv("PATH"))
	t.Setenv("STATE_DIR", stateDir)

	return &githubHarness{
		issuesDir: issuesDir,
		tr:        NewExecClient("owner/repo", testLabels, "agent/issue-", WithVerdictLabels(forge.ResearchVerdictLabels())),
	}
}

func (h *githubHarness) issueDir(num string) string {
	dir := filepath.Join(h.issuesDir, num)
	os.MkdirAll(dir, 0o755)
	return dir
}

func (h *githubHarness) Tracker() forge.IssueTracker { return h.tr }

func (h *githubHarness) SeedIssue(iss forge.Issue) {
	dir := h.issueDir(iss.Number)
	os.WriteFile(filepath.Join(dir, "title"), []byte(iss.Title), 0o644)
	os.WriteFile(filepath.Join(dir, "body"), []byte(iss.Body), 0o644)
	var labels string
	for _, l := range iss.Labels {
		labels += l + "\n"
	}
	os.WriteFile(filepath.Join(dir, "labels"), []byte(labels), 0o644)
}

func (h *githubHarness) SeedNativeDeps(num string, ids []string) {
	dir := h.issueDir(num)
	var s string
	for _, id := range ids {
		s += id + "\n"
	}
	os.WriteFile(filepath.Join(dir, "deps"), []byte(s), 0o644)
}

func (h *githubHarness) FailNativeDeps(num string) {
	os.WriteFile(filepath.Join(h.issueDir(num), "fail_native"), nil, 0o644)
}

func (h *githubHarness) IsolatesNativeFailure() {}

func (h *githubHarness) IsPriorityCapable() {}

func TestExecClient_TrackerContract(t *testing.T) {
	forgetest.RunTrackerContract(t, newGithubHarness(t))
}
