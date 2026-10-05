package github

import (
	_ "embed"
	"encoding/json"
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

// ghWireComment mirrors gh's native comment JSON independently of the
// adapter's own ghComment struct, so a tag typo in the adapter fails the
// contract instead of round-tripping through a shared type.
type ghWireComment struct {
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	CreatedAt       string `json:"createdAt"`
	Body            string `json:"body"`
	IsMinimized     bool   `json:"isMinimized"`
	MinimizedReason string `json:"minimizedReason"`
}

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

// SeedComments writes num's thread in gh's native wire shape, which is what
// pins execClient.Comments's JSON tags against the real CLI's output.
func (h *githubHarness) SeedComments(num string, comments []forge.Comment) {
	doc := struct {
		Comments []ghWireComment `json:"comments"`
	}{Comments: []ghWireComment{}}
	for _, c := range comments {
		var w ghWireComment
		w.Author.Login = c.Author
		w.CreatedAt = c.CreatedAt
		w.Body = c.Body
		w.IsMinimized = c.Minimized
		w.MinimizedReason = c.MinimizedReason
		doc.Comments = append(doc.Comments, w)
	}
	b, _ := json.Marshal(doc)
	os.WriteFile(filepath.Join(h.issueDir(num), "comments"), b, 0o644)
}

func (h *githubHarness) IsolatesNativeFailure() {}

func (h *githubHarness) IsPriorityCapable() {}

func TestExecClient_TrackerContract(t *testing.T) {
	forgetest.RunTrackerContract(t, newGithubHarness(t))
}

func TestExecClient_CommentsCarryMinimized(t *testing.T) {
	h := newGithubHarness(t)
	h.SeedComments("7", []forge.Comment{
		{Author: "a", CreatedAt: "2026-01-01T00:00:00Z", Body: "live"},
		{Author: "b", CreatedAt: "2026-01-02T00:00:00Z", Body: "noise", Minimized: true, MinimizedReason: "SPAM"},
	})
	got, err := h.tr.(forge.CommentLister).Comments("7")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d comments, want 2", len(got))
	}
	if got[0].Minimized || got[0].MinimizedReason != "" {
		t.Errorf("live comment = %+v, want not minimized", got[0])
	}
	if !got[1].Minimized || got[1].MinimizedReason != "SPAM" {
		t.Errorf("minimized comment = %+v, want Minimized with reason SPAM", got[1])
	}
}
