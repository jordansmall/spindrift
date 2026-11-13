package git

import (
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/testutil"
)

func TestSeedMirror_PicksUpBaseAdvanceAndNewBranch(t *testing.T) {
	gitIdentity(t)
	fx := forgetest.NewGitRepoFixture(t, "main")
	dir := filepath.Join(t.TempDir(), "mirror.git")

	if err := SeedMirror(fx.Bare, "main", dir); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	if got, want := testutil.GitOutput(t, dir, "rev-parse", "refs/heads/main"), fx.BranchSHA("main"); got != want {
		t.Errorf("mirror main = %s, want %s", got, want)
	}
	if got := testutil.GitOutput(t, dir, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Errorf("mirror HEAD = %s, want refs/heads/main", got)
	}

	fx.AdvanceBase()
	fx.SeedBranch("agent/issue-7", "7")
	if err := SeedMirror(fx.Bare, "main", dir); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	for _, b := range []string{"main", "agent/issue-7"} {
		if got, want := testutil.GitOutput(t, dir, "rev-parse", "refs/heads/"+b), fx.BranchSHA(b); got != want {
			t.Errorf("mirror %s = %s, want %s", b, got, want)
		}
	}
}

func TestSeedMirror_PrunesBranchDeletedOnRemote(t *testing.T) {
	gitIdentity(t)
	fx := forgetest.NewGitRepoFixture(t, "main")
	fx.SeedBranch("agent/issue-8", "8")
	dir := filepath.Join(t.TempDir(), "mirror.git")
	if err := SeedMirror(fx.Bare, "main", dir); err != nil {
		t.Fatalf("seed: %v", err)
	}
	testutil.GitRun(t, fx.Bare, "branch", "-D", "agent/issue-8")
	if err := SeedMirror(fx.Bare, "main", dir); err != nil {
		t.Fatalf("reseed: %v", err)
	}
	if out := testutil.GitOutput(t, dir, "branch", "--list", "agent/issue-8"); out != "" {
		t.Errorf("mirror still holds a branch deleted on the remote: %q", out)
	}
}

func TestSeedMirror_UnreachableRemoteFails(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mirror.git")
	if err := SeedMirror(filepath.Join(t.TempDir(), "missing.git"), "main", dir); err == nil {
		t.Fatal("SeedMirror from a missing remote = nil, want an error")
	}
}

func TestSeedMirror_BaseBranchMissingOnRemoteFails(t *testing.T) {
	gitIdentity(t)
	fx := forgetest.NewGitRepoFixture(t, "main")
	dir := filepath.Join(t.TempDir(), "mirror.git")

	err := SeedMirror(fx.Bare, "trunk", dir)
	if err == nil {
		t.Fatal("SeedMirror with a base branch absent from the remote = nil, want an error")
	}
	if !strings.Contains(err.Error(), `"trunk"`) {
		t.Errorf("error %q does not name the missing base branch", err)
	}
}

func TestSeedMirror_UserinfoRemote_AttachesCredentialOutOfBand(t *testing.T) {
	clean, remote := authFixture(t)
	dir := filepath.Join(t.TempDir(), "mirror.git")

	if err := SeedMirror(clean, "main", dir); err == nil {
		t.Fatal("seed without the credential = nil, want an auth failure")
	}
	trace := traceGit(t)
	if err := SeedMirror(remote, "main", dir); err != nil {
		t.Fatalf("seed: %v", err)
	}
	assertNoUserinfoInTrace(t, trace())
	for _, b := range []string{"main", "agent/issue-1"} {
		testutil.GitOutput(t, dir, "rev-parse", "refs/heads/"+b)
	}
}

func gitIdentity(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")
}
