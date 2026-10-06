package github

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// seedModifyDeleteConflict registers PR num whose branch modifies base.txt
// while main deletes it: a conflict that leaves unmerged index entries but no
// "CONFLICT (content)" line.
func seedModifyDeleteConflict(t *testing.T, h *codeforgeHarness, num string) string {
	t.Helper()
	branch := h.branchName(num)
	work := t.TempDir()
	forgetest.Run(t, "", "clone", h.repo.Bare, work)
	forgetest.Run(t, work, "checkout", h.base)
	forgetest.Run(t, work, "checkout", "-b", branch)
	writeFile(t, filepath.Join(work, "base.txt"), "modified by pr\n")
	forgetest.Run(t, work, "commit", "-am", "modify base.txt")
	forgetest.Run(t, work, "push", "origin", branch)
	forgetest.Run(t, work, "checkout", h.base)
	forgetest.Run(t, work, "rm", "base.txt")
	forgetest.Run(t, work, "commit", "-m", "delete base.txt")
	forgetest.Run(t, work, "push", "origin", h.base)

	return h.registerPR(num, branch)
}

// withoutGitIdentity strips every source of a committer identity from the
// environment, so a sync that must write a commit fails. Call it after the
// fixture has made its own commits.
func withoutGitIdentity(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL",
	} {
		t.Setenv(k, "") // registers the restore; the unset below is the real change
		os.Unsetenv(k)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "user.useConfigOnly")
	t.Setenv("GIT_CONFIG_VALUE_0", "true")
}

// seedDivergedIdentityless registers a PR whose branch and base each hold a
// commit the other lacks, then strips git's identity: both a rebase and a
// merge need a committer to finish.
func seedDivergedIdentityless(t *testing.T, h *codeforgeHarness, num string) string {
	t.Helper()
	url := h.SeedLandable(num)
	h.AdvanceBase()
	withoutGitIdentity(t)
	return url
}

// TestRebase_SyncFailureClassification pins that Rebase reports
// ErrMergeConflict only for a genuine conflict, and surfaces any other sync
// failure with git's own message (issue #4606).
func TestRebase_SyncFailureClassification(t *testing.T) {
	for _, method := range []string{"rebase", "merge"} {
		t.Run(method+"/modify-delete conflict", func(t *testing.T) {
			h := newCodeForgeHarness(t)
			h.cf = NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-", WithSyncMethod(method))
			url := seedModifyDeleteConflict(t, h, "1")

			err := h.cf.Rebase(url)
			if !errors.Is(err, forge.ErrMergeConflict) {
				t.Fatalf("Rebase err = %v, want ErrMergeConflict", err)
			}
		})

		t.Run(method+"/missing identity is not a conflict", func(t *testing.T) {
			h := newCodeForgeHarness(t)
			h.cf = NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-", WithSyncMethod(method))
			url := seedDivergedIdentityless(t, h, "2")

			err := h.cf.Rebase(url)
			if err == nil {
				t.Fatal("Rebase succeeded without a git identity, want an error")
			}
			if errors.Is(err, forge.ErrMergeConflict) {
				t.Fatalf("Rebase err = %v, must not be ErrMergeConflict", err)
			}
			for _, want := range []string{"git " + method + " origin/main", "email"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Rebase err = %q, want it to contain %q", err, want)
				}
			}
		})
	}
}
