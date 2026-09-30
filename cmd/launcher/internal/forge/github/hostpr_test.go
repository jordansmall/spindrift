package github

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge/forgetest"
)

// NewExecClient (read-write) now issues draft-PR creates itself, on the same
// terms readOnlyCodeForge previously monopolized (issue #4071): the args
// reaching gh are recorded and checked verbatim.
func TestExecClient_CreateDraftPR_IssuesGhPrCreateAndReturnsURL(t *testing.T) {
	dir := prependFakeGH(t, `case "$1-$2" in
pr-create)
	printf 'https://github.com/owner/repo/pull/42\n'
	;;
esac
`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	url, created, err := c.CreateDraftPR("feat: add widget", "Adds a widget.", "main", "agent/issue-42")
	if err != nil {
		t.Fatalf("CreateDraftPR: %v", err)
	}
	want := "https://github.com/owner/repo/pull/42"
	if url != want {
		t.Errorf("CreateDraftPR url = %q, want %q", url, want)
	}
	if !created {
		t.Error("CreateDraftPR created = false, want true for a fresh create")
	}

	got, err := os.ReadFile(filepath.Join(dir, "call-00.txt"))
	if err != nil {
		t.Fatalf("read recorded call: %v", err)
	}
	wantArgs := "pr\ncreate\n--repo\nowner/repo\n--draft\n--base\nmain\n--head\nagent/issue-42\n--title\nfeat: add widget\n--body\nAdds a widget.\n"
	if string(got) != wantArgs {
		t.Errorf("gh pr create args = %q, want %q", string(got), wantArgs)
	}
}

// A refused create must surface gh's stderr, not just "exit status 1".
func TestExecClient_CreateDraftPR_Errors(t *testing.T) {
	prependFakeGH(t, `case "$1-$2" in
pr-create)
	printf 'could not create pull request: validation failed\n' >&2
	exit 1
	;;
esac
`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	_, _, err := c.CreateDraftPR("feat: add widget", "body", "main", "agent/issue-42")
	if err == nil {
		t.Fatal("CreateDraftPR with a failing gh pr create: got nil error, want one")
	}
	if !strings.Contains(err.Error(), "validation failed") {
		t.Errorf("CreateDraftPR error = %q, want it to contain gh's stderr", err.Error())
	}
}

// PushBranch fetches localRef straight out of the local repo at srcDir, not
// out of a bundle, and pushes it onto a fresh branch on the target
// remote (issue #4071, ADR 0057).
func TestExecClient_PushBranch_CreatesRemoteBranch(t *testing.T) {
	repo := newRelayHarness(t)

	src := t.TempDir()
	forgetest.Run(t, "", "clone", repo.Bare, src)
	forgetest.Run(t, src, "checkout", "-b", "work")
	forgetest.WriteFile(t, filepath.Join(src, "feature.txt"), "feature\n")
	forgetest.Run(t, src, "add", "feature.txt")
	forgetest.Run(t, src, "commit", "-m", "feature")
	wantSHA := forgetest.RevParse(t, src, "work")

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	branch := "agent/issue-9001"
	if err := c.PushBranch(src, "work", branch, "main"); err != nil {
		t.Fatalf("PushBranch: %v", err)
	}

	if got := forgetest.RevParse(t, repo.Bare, "refs/heads/"+branch); got != wantSHA {
		t.Errorf("refs/heads/%s = %s, want %s", branch, got, wantSHA)
	}
}

// PushBranch is create-only (issue #4104): a second push onto a branch the
// first push already created must fail, leaving the first push's tip
// on the remote untouched.
func TestExecClient_PushBranch_RefusesExistingBranch(t *testing.T) {
	repo := newRelayHarness(t)
	branch := "agent/issue-9002"
	c := NewExecClient("owner/repo", testLabels, "agent/issue-")

	src1 := t.TempDir()
	forgetest.Run(t, "", "clone", repo.Bare, src1)
	forgetest.Run(t, src1, "checkout", "-b", "work")
	forgetest.WriteFile(t, filepath.Join(src1, "feature.txt"), "v1\n")
	forgetest.Run(t, src1, "add", "feature.txt")
	forgetest.Run(t, src1, "commit", "-m", "v1")
	if err := c.PushBranch(src1, "work", branch, "main"); err != nil {
		t.Fatalf("PushBranch (first): %v", err)
	}
	wantSHA := forgetest.RevParse(t, repo.Bare, "refs/heads/"+branch)

	src2 := t.TempDir()
	forgetest.Run(t, "", "clone", repo.Bare, src2)
	forgetest.Run(t, src2, "checkout", "main")
	forgetest.Run(t, src2, "checkout", "-b", "work2")
	forgetest.WriteFile(t, filepath.Join(src2, "feature.txt"), "v2\n")
	forgetest.Run(t, src2, "add", "feature.txt")
	forgetest.Run(t, src2, "commit", "-m", "v2")
	if err := c.PushBranch(src2, "work2", branch, "main"); err == nil {
		t.Fatal("PushBranch onto an already-existing branch: got nil error, want one")
	}

	if got := forgetest.RevParse(t, repo.Bare, "refs/heads/"+branch); got != wantSHA {
		t.Errorf("refs/heads/%s = %s, want %s (the first push's tip, unchanged)", branch, got, wantSHA)
	}
}

// A pre-receive hook's rejection message must reach the caller.
func TestExecClient_PushBranch_HookRejectionSurfacesStderr(t *testing.T) {
	repo := newRelayHarness(t)
	hook := "#!/bin/sh\nprintf 'hook declined: protected\\n' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(repo.Bare, "hooks", "pre-receive"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}

	src := t.TempDir()
	forgetest.Run(t, "", "clone", repo.Bare, src)
	forgetest.Run(t, src, "checkout", "-b", "work")
	forgetest.WriteFile(t, filepath.Join(src, "feature.txt"), "feature\n")
	forgetest.Run(t, src, "add", "feature.txt")
	forgetest.Run(t, src, "commit", "-m", "feature")

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	err := c.PushBranch(src, "work", "agent/issue-9003", "main")
	if err == nil {
		t.Fatal("PushBranch rejected by a pre-receive hook: got nil error, want one")
	}
	if !strings.Contains(err.Error(), "hook declined: protected") {
		t.Errorf("PushBranch error = %q, want it to contain the hook's stderr", err.Error())
	}
}

// DeleteBranch issues gh's own ref-delete API verbatim (issue #4112).
func TestExecClient_DeleteBranch_IssuesGhApiDelete(t *testing.T) {
	dir := prependFakeGH(t, "")

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	if err := c.DeleteBranch("agent/issue-9004", "main"); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "call-00.txt"))
	if err != nil {
		t.Fatalf("read recorded call: %v", err)
	}
	wantArgs := "api\n-X\nDELETE\nrepos/owner/repo/git/refs/heads/agent/issue-9004\n"
	if string(got) != wantArgs {
		t.Errorf("gh api args = %q, want %q", string(got), wantArgs)
	}
}

// A refused delete must surface gh's stderr, not just "exit status 1".
func TestExecClient_DeleteBranch_Errors(t *testing.T) {
	prependFakeGH(t, `printf 'HTTP 404: Not Found\n' >&2
exit 1
`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	err := c.DeleteBranch("agent/issue-9005", "main")
	if err == nil {
		t.Fatal("DeleteBranch with a failing gh api: got nil error, want one")
	}
	if !strings.Contains(err.Error(), "HTTP 404: Not Found") {
		t.Errorf("DeleteBranch error = %q, want it to contain gh's stderr", err.Error())
	}
}

// DeleteBranch refuses branch == base and an invalid branch name before any
// gh invocation, matching PushBranch's create-only guard (issue #4104).
func TestExecClient_DeleteBranch_RefusesBeforeAnyGhCall(t *testing.T) {
	cases := []struct {
		name, branch, base, wantErrSubstr string
	}{
		{"empty branch", "", "main", "invalid branch"},
		{"dash branch", "-x", "main", "invalid branch"},
		{"branch is base", "main", "main", "base branch"},
		{"branch is base, refs/heads/ prefixed", "refs/heads/main", "main", "base branch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := prependFakeGH(t, "")

			c := NewExecClient("owner/repo", testLabels, "agent/issue-")
			err := c.DeleteBranch(tc.branch, tc.base)
			if err == nil {
				t.Fatal("DeleteBranch with an invalid arg: got nil error, want one")
			}
			if !strings.Contains(err.Error(), tc.wantErrSubstr) {
				t.Errorf("DeleteBranch error = %q, want it to contain %q", err.Error(), tc.wantErrSubstr)
			}
			if calls, _ := filepath.Glob(filepath.Join(dir, "call-*.txt")); len(calls) != 0 {
				t.Errorf("DeleteBranch with an invalid arg: gh was invoked, want it skipped")
			}
		})
	}
}
