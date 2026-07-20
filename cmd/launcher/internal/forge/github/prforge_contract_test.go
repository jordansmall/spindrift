package github

import (
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// fakeGHPRForge is a stateful stand-in for the gh CLI, backing the
// prforgeHarness below.
//
//go:embed testdata/fake-gh-prforge.sh
var fakeGHPRForge string

// prforgeHarness is a forgetest.PRForgeHarness backed by a real bare git
// repo (forgetest.GitRepoFixture, the fake gh script's REMOTE) plus a
// scripted `gh` stand-in for every PR-indirection call (pr list/view/merge/
// ready, api graphql) — mirroring codeforgeHarness's split between real git
// plumbing and scripted PR-shaped lookups.
type prforgeHarness struct {
	t        *testing.T
	repo     *forgetest.GitRepoFixture
	stateDir string
	base     string
	cf       forge.CodeForge
}

func newPRForgeHarness(t *testing.T) *prforgeHarness {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")

	repo := forgetest.NewGitRepoFixture(t, "main")
	stateDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stateDir, "prs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stateDir, "branches"), 0o755); err != nil {
		t.Fatal(err)
	}

	scriptDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(scriptDir, "gh"), []byte(fakeGHPRForge), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", scriptDir+":"+os.Getenv("PATH"))
	t.Setenv("REMOTE", repo.Bare)
	t.Setenv("STATE_DIR", stateDir)

	return &prforgeHarness{
		t:        t,
		repo:     repo,
		stateDir: stateDir,
		base:     "main",
		cf:       NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-"),
	}
}

func (h *prforgeHarness) Forge() forge.PRForge       { return h.cf.(forge.PRForge) }
func (h *prforgeHarness) CodeForge() forge.CodeForge { return h.cf }

func (h *prforgeHarness) branchName(num string) string { return "agent/issue-" + num }

func (h *prforgeHarness) prURL(num string) string {
	return "https://github.com/owner/repo/pull/" + num
}

// SeedOpenPR creates branch agent/issue-<num> one commit ahead of main's
// current tip, pushes it, and registers the head/base/prstate/branch
// mappings the fake gh script's `pr list`/`pr view`/`pr merge` handlers
// look up. Returns the PR URL every PRForge method expects.
func (h *prforgeHarness) SeedOpenPR(num string) string {
	branch := h.branchName(num)
	h.repo.SeedBranch(branch, num)

	prDir := filepath.Join(h.stateDir, "prs", num)
	if err := os.MkdirAll(prDir, 0o755); err != nil {
		h.t.Fatal(err)
	}
	writeFile(h.t, filepath.Join(prDir, "head"), branch)
	writeFile(h.t, filepath.Join(prDir, "base"), h.base)
	writeFile(h.t, filepath.Join(prDir, "prstate"), "OPEN")
	writeFile(h.t, filepath.Join(prDir, "url"), h.prURL(num))

	branchFile := filepath.Join(h.stateDir, "branches", branch)
	if err := os.MkdirAll(filepath.Dir(branchFile), 0o755); err != nil {
		h.t.Fatal(err)
	}
	writeFile(h.t, branchFile, num)

	return h.prURL(num)
}

// SeedCheckStates writes the scripted RollupState queue CheckState pops
// from, one entry per line.
func (h *prforgeHarness) SeedCheckStates(url string, states []forge.RollupState) {
	num := prNum(url)
	lines := make([]string, len(states))
	for i, s := range states {
		lines[i] = string(s)
	}
	prDir := filepath.Join(h.stateDir, "prs", num)
	if err := os.MkdirAll(prDir, 0o755); err != nil {
		h.t.Fatal(err)
	}
	writeFile(h.t, filepath.Join(prDir, "checks"), strings.Join(lines, "\n")+"\n")
}

// SeedFailingCheck writes a single failing CheckRun context so the real
// adapter's FailureDetail runs its genuine GraphQL-response parsing and
// rendering (ci_rollup.go), not a scripted pass-through.
func (h *prforgeHarness) SeedFailingCheck(url, name, conclusion, summary string) {
	num := prNum(url)
	prDir := filepath.Join(h.stateDir, "prs", num)
	if err := os.MkdirAll(prDir, 0o755); err != nil {
		h.t.Fatal(err)
	}
	contexts := []failureDetailContext{{TypeName: "CheckRun", Name: name, Conclusion: conclusion, Summary: summary}}
	out, err := json.Marshal(contexts)
	if err != nil {
		h.t.Fatal(err)
	}
	writeFile(h.t, filepath.Join(prDir, "contexts.json"), string(out))
}

// SeedAutoMergeAllowed scripts the repo-wide CanAutoMerge result.
func (h *prforgeHarness) SeedAutoMergeAllowed(allowed bool) {
	writeFile(h.t, filepath.Join(h.stateDir, "automerge_allowed"), strconv.FormatBool(allowed))
}

// AutoMergeEnqueued reports whether the fake gh script's `pr merge --auto`
// handler recorded url's PR number as enqueued.
func (h *prforgeHarness) AutoMergeEnqueued(url string) bool {
	_, err := os.Stat(filepath.Join(h.stateDir, "prs", prNum(url), "automerge"))
	return err == nil
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestExecClient_PRForgeContract(t *testing.T) {
	forgetest.RunPRForgeContract(t, newPRForgeHarness(t))
}
