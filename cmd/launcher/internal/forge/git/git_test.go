package git

import (
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/testutil"
)

func TestGitClient_ImplementsCodeForge(t *testing.T) {
	var _ forge.CodeForge = NewGitClient("https://example.invalid/repo.git", "main", "Test Bot", "bot@example.com", "agent/issue-")
}

// The git Code Forge deliberately implements no PR, CI, or auto-merge calls.
// A failed type assertion against forge.PRForge is what callers check for, in
// place of the removed PushOnly() flag.
func TestGitClient_NoPRForgeConcept(t *testing.T) {
	g := NewGitClient("https://example.invalid/repo.git", "main", "Test Bot", "bot@example.com", "agent/issue-")
	if _, ok := g.(forge.PRForge); ok {
		t.Error("gitClient satisfies forge.PRForge, want it to implement forge.CodeForge only")
	}
}

// newBareRemoteWithBranches builds the shape a Box leaves behind: a base
// branch plus a pushed per-issue branch one commit ahead of it. It returns the
// bare repo path.
func newBareRemoteWithBranches(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bare := filepath.Join(dir, "origin.git")
	work := filepath.Join(dir, "work")

	testutil.GitRun(t, "", "init", "--bare", bare)
	testutil.GitRun(t, "", "clone", bare, work)
	testutil.GitRun(t, work, "checkout", "-B", "main")
	testutil.GitRun(t, work, "config", "user.email", "test@example.com")
	testutil.GitRun(t, work, "config", "user.name", "Test")
	testutil.WriteFixture(t, work, "base.txt", "base\n")
	testutil.GitRun(t, work, "add", "base.txt")
	testutil.GitRun(t, work, "commit", "-m", "base")
	testutil.GitRun(t, work, "push", "-u", "origin", "main")

	testutil.GitRun(t, work, "checkout", "-b", "agent/issue-1")
	testutil.WriteFixture(t, work, "feature.txt", "feature\n")
	testutil.GitRun(t, work, "add", "feature.txt")
	testutil.GitRun(t, work, "commit", "-m", "feature")
	testutil.GitRun(t, work, "push", "-u", "origin", "agent/issue-1")

	return bare
}

// Merge is how MERGE_MODE=immediate lands work on a push-only forge.
func TestGitClient_Merge_PushOnlyLanding(t *testing.T) {
	bare := newBareRemoteWithBranches(t)
	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-")

	if err := g.Merge("agent/issue-1"); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	verify := t.TempDir()
	testutil.GitRun(t, "", "clone", bare, verify)
	testutil.GitRun(t, verify, "checkout", "main")
	if _, err := os.Stat(filepath.Join(verify, "feature.txt")); err != nil {
		t.Errorf("main does not contain feature.txt after Merge: %v", err)
	}
}

// conflictRemote builds a bare remote whose "main" and "agent/issue-1" both
// descend from a base commit holding files; each side then runs its own steps
// in the work clone before they are committed and pushed.
func conflictRemote(t *testing.T, files map[string]string, branchSteps, mainSteps func(work string)) string {
	t.Helper()
	dir := t.TempDir()
	bare := filepath.Join(dir, "origin.git")
	work := filepath.Join(dir, "work")

	testutil.GitRun(t, "", "init", "--bare", bare)
	testutil.GitRun(t, "", "clone", bare, work)
	testutil.GitRun(t, work, "checkout", "-B", "main")
	testutil.GitRun(t, work, "config", "user.email", "test@example.com")
	testutil.GitRun(t, work, "config", "user.name", "Test")
	for name, body := range files {
		testutil.WriteFixture(t, work, name, body)
		testutil.GitRun(t, work, "add", name)
	}
	testutil.GitRun(t, work, "commit", "-m", "base")
	testutil.GitRun(t, work, "push", "-u", "origin", "main")

	testutil.GitRun(t, work, "checkout", "-b", "agent/issue-1")
	branchSteps(work)
	testutil.GitRun(t, work, "commit", "-m", "feature")
	testutil.GitRun(t, work, "push", "-u", "origin", "agent/issue-1")

	testutil.GitRun(t, work, "checkout", "main")
	mainSteps(work)
	testutil.GitRun(t, work, "commit", "-m", "main")
	testutil.GitRun(t, work, "push", "origin", "main")
	return bare
}

func TestGitClient_Merge_ConflictReturnsErrMergeConflict(t *testing.T) {
	bare := conflictRemote(t, map[string]string{"shared.txt": "base\n"},
		func(work string) {
			testutil.WriteFixture(t, work, "shared.txt", "feature change\n")
			testutil.GitRun(t, work, "add", "shared.txt")
		},
		func(work string) {
			testutil.WriteFixture(t, work, "shared.txt", "conflicting main change\n")
			testutil.GitRun(t, work, "add", "shared.txt")
		})

	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-")
	if err := g.Merge("agent/issue-1"); !errors.Is(err, forge.ErrMergeConflict) {
		t.Fatalf("Merge: want forge.ErrMergeConflict, got: %v", err)
	}
}

// TestGitClient_Merge_ModifyDeleteConflictReturnsErrMergeConflict covers a
// conflict for which git prints no "merge conflict" text, so the
// classification must come from the unmerged index entries.
func TestGitClient_Merge_ModifyDeleteConflictReturnsErrMergeConflict(t *testing.T) {
	bare := conflictRemote(t, map[string]string{"shared.txt": "base\n"},
		func(work string) { testutil.GitRun(t, work, "rm", "shared.txt") },
		func(work string) {
			testutil.WriteFixture(t, work, "shared.txt", "main change\n")
			testutil.GitRun(t, work, "add", "shared.txt")
		})

	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-")
	if err := g.Merge("agent/issue-1"); !errors.Is(err, forge.ErrMergeConflict) {
		t.Fatalf("Merge: want forge.ErrMergeConflict, got: %v", err)
	}
}

// TestGitClient_Merge_RenameRenameConflictReturnsErrMergeConflict covers
// another conflict kind that prints no "merge conflict" text.
func TestGitClient_Merge_RenameRenameConflictReturnsErrMergeConflict(t *testing.T) {
	body := "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\n"
	bare := conflictRemote(t, map[string]string{"x.txt": body},
		func(work string) { testutil.GitRun(t, work, "mv", "x.txt", "z.txt") },
		func(work string) { testutil.GitRun(t, work, "mv", "x.txt", "y.txt") })

	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-")
	if err := g.Merge("agent/issue-1"); !errors.Is(err, forge.ErrMergeConflict) {
		t.Fatalf("Merge: want forge.ErrMergeConflict, got: %v", err)
	}
}

func TestGitClient_Rebase_ForcePushesRebasedBranch(t *testing.T) {
	dir := t.TempDir()
	bare := filepath.Join(dir, "origin.git")
	work := filepath.Join(dir, "work")

	testutil.GitRun(t, "", "init", "--bare", bare)
	testutil.GitRun(t, "", "clone", bare, work)
	testutil.GitRun(t, work, "checkout", "-B", "main")
	testutil.GitRun(t, work, "config", "user.email", "test@example.com")
	testutil.GitRun(t, work, "config", "user.name", "Test")
	testutil.WriteFixture(t, work, "base.txt", "base\n")
	testutil.GitRun(t, work, "add", "base.txt")
	testutil.GitRun(t, work, "commit", "-m", "base")
	testutil.GitRun(t, work, "push", "-u", "origin", "main")

	testutil.GitRun(t, work, "checkout", "-b", "agent/issue-1")
	testutil.WriteFixture(t, work, "feature.txt", "feature\n")
	testutil.GitRun(t, work, "add", "feature.txt")
	testutil.GitRun(t, work, "commit", "-m", "feature")
	testutil.GitRun(t, work, "push", "-u", "origin", "agent/issue-1")

	// Advance main so the feature branch is now behind.
	testutil.GitRun(t, work, "checkout", "main")
	testutil.WriteFixture(t, work, "later.txt", "later\n")
	testutil.GitRun(t, work, "add", "later.txt")
	testutil.GitRun(t, work, "commit", "-m", "later main commit")
	testutil.GitRun(t, work, "push", "origin", "main")

	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-")
	if err := g.Rebase("agent/issue-1"); err != nil {
		t.Fatalf("Rebase: %v", err)
	}

	verify := t.TempDir()
	testutil.GitRun(t, "", "clone", bare, verify)
	testutil.GitRun(t, verify, "checkout", "agent/issue-1")
	if _, err := os.Stat(filepath.Join(verify, "later.txt")); err != nil {
		t.Errorf("rebased branch does not contain later.txt from base: %v", err)
	}
	if _, err := os.Stat(filepath.Join(verify, "feature.txt")); err != nil {
		t.Errorf("rebased branch lost feature.txt: %v", err)
	}
}

// Regression test for argument-injection RCE via `git fetch origin <branch>`:
// a ref starting with "-" reaches git as an option. The landing= field of an
// outcome line is untrusted input under CLAUDE.md's comment-injection trust
// boundary, so Merge must reject the ref rather than pass it on.
func TestGitClient_Merge_RejectsFlagLikeRef(t *testing.T) {
	bare := newBareRemoteWithBranches(t)
	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-")

	canary := filepath.Join(t.TempDir(), "pwned")
	err := g.Merge("--upload-pack=touch " + canary)
	if err == nil {
		t.Fatal("Merge: want error for a flag-like ref, got nil")
	}
	if _, statErr := os.Stat(canary); statErr == nil {
		t.Fatal("Merge executed the injected command — argument injection succeeded")
	}
}

// This is Rebase's counterpart to TestGitClient_Merge_RejectsFlagLikeRef,
// which carries the full rationale.
func TestGitClient_Rebase_RejectsFlagLikeRef(t *testing.T) {
	bare := newBareRemoteWithBranches(t)
	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-")

	if err := g.Rebase("--upload-pack=touch /tmp/should-not-run"); err == nil {
		t.Fatal("Rebase: want error for a flag-like ref, got nil")
	}
}

// A merge commit needs a committer identity, and ambient host git config may
// have none on a bare CI runner, so Merge sets the launcher-supplied identity
// on its throwaway clone.
func TestGitClient_Merge_SetsCommitIdentityOnTempClone(t *testing.T) {
	bare := newBareRemoteWithBranches(t)
	g := NewGitClient(bare, "main", "Spindrift Bot", "bot@example.com", "agent/issue-")

	if err := g.Merge("agent/issue-1"); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	verify := t.TempDir()
	testutil.GitRun(t, "", "clone", bare, verify)
	testutil.GitRun(t, verify, "checkout", "main")
	got := testutil.GitOutput(t, verify, "log", "-1", "--format=%an <%ae>", "main")
	if got != "Spindrift Bot <bot@example.com>" {
		t.Errorf("merge commit identity = %q, want \"Spindrift Bot <bot@example.com>\"", got)
	}
}

// unreachableRemoteURL returns a credential-bearing https URL on an ephemeral
// port the OS just freed. A hardcoded privileged port such as 127.0.0.1:1 is
// not a safe substitute: whether anything can bind it depends on process
// privileges and the host.
func unreachableRemoteURL(t *testing.T, secret string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return "https://oauth2:" + secret + "@" + addr + "/does-not-exist.git"
}

// This test pins the precondition every test that forces a clone or probe
// failure depends on: nothing is listening on the helper's port.
func TestUnreachableRemoteURL_PointsToClosedPort(t *testing.T) {
	remote := unreachableRemoteURL(t, "sometoken123")

	u, err := url.Parse(remote)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", remote, err)
	}

	conn, err := net.DialTimeout("tcp", u.Host, 2*time.Second)
	if err == nil {
		conn.Close()
		t.Fatalf("dial %q: want error (unreachable), got success", u.Host)
	}
}

// Merge's error flows unmodified into a public GitHub issue comment
// (settle.mergeImmediate), so it must never echo back the credential from the
// oauth2:<token>@host remote URL that CODE_FORGE_REMOTE_URL uses for hosts
// without a credential helper (docs/reference.md).
func TestGitClient_Merge_CloneFailureDoesNotLeakCredentials(t *testing.T) {
	const secret = "sometoken123"
	g := NewGitClient(unreachableRemoteURL(t, secret), "main", "Test Bot", "bot@example.com", "agent/issue-")

	err := g.Merge("agent/issue-1")
	if err == nil {
		t.Fatal("Merge against unreachable credential-bearing remote: want error, got nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Merge error leaks embedded credential: %v", err)
	}
}

// installLeakingHook makes every fresh clone carry a hookName hook that rejects
// the operation and writes a credential-bearing URL to stderr. init.templateDir
// (via a throwaway HOME's .gitconfig) is how the hook reaches the temp clone
// that cloneToTemp makes.
func installLeakingHook(t *testing.T, hookName, secret string) {
	t.Helper()
	home := t.TempDir()
	templateDir := filepath.Join(home, "template")
	hooksDir := filepath.Join(templateDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hookPath := filepath.Join(hooksDir, hookName)
	testutil.WriteFixture(t, hooksDir, hookName, "#!/bin/sh\necho 'fatal: unable to access "+
		"https://oauth2:"+secret+"@git.example.com/org/repo.git/' >&2\nexit 1\n")
	if err := os.Chmod(hookPath, 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFixture(t, home, ".gitconfig", "[init]\n\ttemplateDir = "+templateDir+"\n")
	t.Setenv("HOME", home)
}

// This test crosses the same public-comment trust boundary as the clone and
// probe tests above, but reaches it through git.go's non-conflict merge
// branch: the credential text comes from a rejecting hook's own output rather
// than from the remote URL.
func TestGitClient_Merge_HookOutputDoesNotLeakCredentials(t *testing.T) {
	const secret = "sometoken123"
	t.Setenv("LC_ALL", "C")
	bare := newBareRemoteWithBranches(t)
	installLeakingHook(t, "pre-merge-commit", secret)

	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-")
	err := g.Merge("agent/issue-1")
	if err == nil {
		t.Fatal("Merge: want error from failing pre-commit hook, got nil")
	}
	if errors.Is(err, forge.ErrMergeConflict) {
		t.Fatal("Merge: want non-conflict hook failure, got forge.ErrMergeConflict")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Merge error leaks hook output credential: %v", err)
	}
	if !strings.Contains(err.Error(), "unable to access") {
		t.Fatalf("Merge error should carry the redacted hook output: %v", err)
	}
}

func TestGitClient_Probe(t *testing.T) {
	bare := newBareRemoteWithBranches(t)

	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-")
	if _, err := g.Probe(); err != nil {
		t.Errorf("Probe on reachable remote: %v", err)
	}

	bad := NewGitClient(filepath.Join(t.TempDir(), "does-not-exist.git"), "main", "Test Bot", "bot@example.com", "agent/issue-")
	if _, err := bad.Probe(); err == nil {
		t.Error("Probe on unreachable remote: want error, got nil")
	}
}

func TestGitClient_BranchExists(t *testing.T) {
	bare := newBareRemoteWithBranches(t)
	// ls-remote patterns tail-match and glob, so this decoy (and the glob
	// names below) must not read as the branch existing.
	testutil.GitRun(t, bare, "update-ref", "refs/heads/backup/agent/issue-2", "refs/heads/agent/issue-1")
	// Matches the qualified pattern under `*/`, so ls-remote exits 0 and only
	// the exact-line filter rejects it.
	testutil.GitRun(t, bare, "update-ref", "refs/heads/x/refs/heads/agent/issue-2", "refs/heads/agent/issue-1")
	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-")

	exists, err := g.BranchExists("agent/issue-1")
	if err != nil {
		t.Fatalf("BranchExists(agent/issue-1): %v", err)
	}
	if !exists {
		t.Error("BranchExists(agent/issue-1) = false, want true — the branch was pushed")
	}

	for _, tc := range []struct{ branch, why string }{
		{"agent/issue-999", "never pushed"},
		{"agent/issue-2", "only tail-matched by backup/ and x/refs/heads/ decoys"},
		{"issue-1", "tail-matches agent/issue-1"},
		{"issue-2", "tail-matches decoy refs"},
		{"agent/*", "glob"},
		{"agent/issue-?", "glob"},
		{"refs/heads/agent/issue-1", "double prefix"},
	} {
		exists, err := g.BranchExists(tc.branch)
		if err != nil {
			t.Fatalf("BranchExists(%s): %v", tc.branch, err)
		}
		if exists {
			t.Errorf("BranchExists(%s) = true, want false — %s (no refs/heads/%s)", tc.branch, tc.why, tc.branch)
		}
	}
}

// Probe's error can reach `doctor` output, so error text derived from
// remoteURL stays redacted the same way Merge's and Rebase's do.
func TestGitClient_Probe_DoesNotLeakCredentials(t *testing.T) {
	const secret = "sometoken123"
	g := NewGitClient(unreachableRemoteURL(t, secret), "main", "Test Bot", "bot@example.com", "agent/issue-")

	_, err := g.Probe()
	if err == nil {
		t.Fatal("Probe against unreachable credential-bearing remote: want error, got nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Probe error leaks embedded credential: %v", err)
	}
}

// Probe's success value flows to the Box the same way its error does, so the
// returned remote URL must be redacted too — git's file transport happily
// succeeds against a credential-bearing URL, which would otherwise echo the
// token back.
func TestGitClient_Probe_SuccessDoesNotLeakCredentials(t *testing.T) {
	const secret = "sometoken123"
	bare := newBareRemoteWithBranches(t)
	remote := "file://oauth2:" + secret + "@/" + bare
	g := NewGitClient(remote, "main", "Test Bot", "bot@example.com", "agent/issue-")

	got, err := g.Probe()
	if err != nil {
		t.Fatalf("Probe against reachable credential-bearing remote: %v", err)
	}
	if u, perr := url.Parse(got); perr != nil {
		t.Fatalf("Probe success returned unparseable URL %q: %v", got, perr)
	} else if u.User != nil {
		t.Fatalf("Probe success leaks embedded credential: %q", got)
	}
}

// hangingRemoteURL returns a credential-bearing http URL whose listener
// accepts every connection and never answers, so the remote hangs
// mid-handshake. unreachableRemoteURL covers the fail-fast case.
func hangingRemoteURL(t *testing.T, secret string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})

	return "http://oauth2:" + secret + "@" + ln.Addr().String() + "/does-not-exist.git"
}

// cloneToTemp's timeout has to bound the `git clone` invocation itself: a
// remote that accepts the connection and then hangs never fails on its own,
// so without the timeout Merge blocks forever.
func TestGitClient_Merge_CloneTimesOutOnHangingRemote(t *testing.T) {
	const secret = "sometoken123"
	g := NewGitClient(hangingRemoteURL(t, secret), "main", "Test Bot", "bot@example.com", "agent/issue-",
		WithCloneTimeout(200*time.Millisecond))

	start := time.Now()
	err := g.Merge("agent/issue-1")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Merge against hanging remote: want error, got nil")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Merge took %s to return, want it bounded by the configured clone timeout", elapsed)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Merge error = %q, want it to mention timing out", err.Error())
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Merge error leaks embedded credential: %v", err)
	}
}

// This is Rebase's counterpart to
// TestGitClient_Merge_CloneTimesOutOnHangingRemote, since Rebase calls the
// same cloneToTemp helper.
func TestGitClient_Rebase_CloneTimesOutOnHangingRemote(t *testing.T) {
	const secret = "sometoken123"
	g := NewGitClient(hangingRemoteURL(t, secret), "main", "Test Bot", "bot@example.com", "agent/issue-",
		WithCloneTimeout(200*time.Millisecond))

	start := time.Now()
	err := g.Rebase("agent/issue-1")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Rebase against hanging remote: want error, got nil")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Rebase took %s to return, want it bounded by the configured clone timeout", elapsed)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Rebase error = %q, want it to mention timing out", err.Error())
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Rebase error leaks embedded credential: %v", err)
	}
}

// Probe bounds its `git ls-remote` invocation with the op timeout, so the
// same hanging remote must not block it forever.
func TestGitClient_Probe_TimesOutOnHangingRemote(t *testing.T) {
	const secret = "sometoken123"
	g := NewGitClient(hangingRemoteURL(t, secret), "main", "Test Bot", "bot@example.com", "agent/issue-",
		WithOpTimeout(200*time.Millisecond))

	start := time.Now()
	_, err := g.Probe()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Probe against hanging remote: want error, got nil")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Probe took %s to return, want it bounded by the configured op timeout", elapsed)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Probe error = %q, want it to mention timing out", err.Error())
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Probe error leaks embedded credential: %v", err)
	}
}

// newBareRemoteWithHangingPush is newBareRemoteWithBranches plus a
// pre-receive hook that sleeps forever, so the remote hangs partway through a
// push rather than during the clone hangingRemoteURL covers. Local pushes
// still run server-side hooks, so this needs no network listener.
func newBareRemoteWithHangingPush(t *testing.T) string {
	t.Helper()
	bare := newBareRemoteWithBranches(t)
	rel := filepath.Join("hooks", "pre-receive")
	testutil.WriteFixture(t, bare, rel, "#!/bin/sh\nsleep 999\n")
	if err := os.Chmod(filepath.Join(bare, rel), 0o755); err != nil {
		t.Fatalf("chmod pre-receive hook: %v", err)
	}
	return bare
}

// The op timeout has to cover Merge's post-clone subprocesses (checkout,
// fetch, merge, push) too, not just the clone.
func TestGitClient_Merge_TimesOutOnHangingPush(t *testing.T) {
	bare := newBareRemoteWithHangingPush(t)
	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-",
		WithOpTimeout(200*time.Millisecond))

	start := time.Now()
	err := g.Merge("agent/issue-1")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Merge against hanging push: want error, got nil")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Merge took %s to return, want it bounded by the configured op timeout", elapsed)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Merge error = %q, want it to mention timing out", err.Error())
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Merge error = %v, want errors.Is(err, context.DeadlineExceeded)", err)
	}
}

// installGitShim puts a "git" shim ahead of the real one on PATH. On a
// non-abort `git ... <subcommand> ...` it runs action, a shell snippet, then
// passes through to the real git; every other subcommand passes straight
// through. Checkout, merge, and rebase run locally, with no network round trip
// that hangingRemoteURL or a pre-receive hook could hang, so the shim is the
// only deterministic way to stall them.
func installGitShim(t *testing.T, subcommand, action string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("look up real git: %v", err)
	}
	shimDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"sub=; skip=0\n" +
		"for a; do\n" +
		"  if [ $skip = 1 ]; then skip=0; continue; fi\n" +
		"  case $a in -C|-c) skip=1;; *) sub=$a; break;; esac\n" +
		"done\n" +
		"case \" $*\" in *\" --abort\"*) sub=;; esac\n" +
		"if [ \"$sub\" = \"" + subcommand + "\" ]; then\n" +
		"  " + action + "\n" +
		"fi\n" +
		"exec " + realGit + " \"$@\"\n"
	shim := filepath.Join(shimDir, "git")
	testutil.WriteFixture(t, shimDir, "git", script)
	if err := os.Chmod(shim, 0o755); err != nil {
		t.Fatalf("chmod git shim: %v", err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// installHangingGitShim makes a non-abort `git ... <subcommand> ...` sleep forever.
func installHangingGitShim(t *testing.T, subcommand string) {
	t.Helper()
	installGitShim(t, subcommand, "sleep 999")
}

// The op timeout has to cover the merge invocation itself, not just the
// clone and the push around it.
func TestGitClient_Merge_TimesOutOnHangingMerge(t *testing.T) {
	bare := newBareRemoteWithBranches(t)
	installHangingGitShim(t, "merge")
	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-",
		WithOpTimeout(200*time.Millisecond))

	start := time.Now()
	err := g.Merge("agent/issue-1")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Merge against hanging merge invocation: want error, got nil")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Merge took %s to return, want it bounded by the configured op timeout", elapsed)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Merge error = %q, want it to mention timing out", err.Error())
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Merge error = %v, want errors.Is(err, context.DeadlineExceeded)", err)
	}
}

// A hook's backgrounded grandchild inherits git's output pipe and outlives git;
// Merge must not wait on it, and git's own exit 0 still means the merge landed.
func TestGitClient_Merge_SucceedsWhenGrandchildHoldsOutputPipe(t *testing.T) {
	bare := newBareRemoteWithBranches(t)
	// The grandchild outlives the ceiling below by a wide margin: Merge's own
	// clone, fetch, WaitDelay, and push take seconds on a loaded CI builder.
	installGitShim(t, "merge", "sleep 60 &")
	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-")

	start := time.Now()
	err := g.Merge("agent/issue-1")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if elapsed > 30*time.Second {
		t.Fatalf("Merge took %s to return, want it not to wait on a grandchild holding the output pipe", elapsed)
	}

	verify := t.TempDir()
	testutil.GitRun(t, "", "clone", bare, verify)
	testutil.GitRun(t, verify, "checkout", "main")
	if _, err := os.Stat(filepath.Join(verify, "feature.txt")); err != nil {
		t.Errorf("main does not contain feature.txt after Merge: %v", err)
	}
}

// The op timeout has to cover Rebase's post-clone subprocesses (checkout,
// rebase) too, not just the clone.
func TestGitClient_Rebase_TimesOutOnHangingRebase(t *testing.T) {
	bare := newBareRemoteWithBranches(t)
	installHangingGitShim(t, "rebase")
	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-",
		WithOpTimeout(200*time.Millisecond))

	start := time.Now()
	err := g.Rebase("agent/issue-1")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Rebase against hanging rebase invocation: want error, got nil")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Rebase took %s to return, want it bounded by the configured op timeout", elapsed)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Rebase error = %q, want it to mention timing out", err.Error())
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Rebase error = %v, want errors.Is(err, context.DeadlineExceeded)", err)
	}
}

// The op timeout also has to cover Rebase's trailing force-push
// (gitplumbing.GitForcePush). Git skips the connection on a same-ref push and
// reports everything up to date, so the hook never runs unless main is
// advanced past the feature branch's base first, making the rebase produce a
// commit the remote lacks.
func TestGitClient_Rebase_TimesOutOnHangingPush(t *testing.T) {
	// Advance main before installing the hanging hook below, which would block
	// this setup push forever too.
	bare := newBareRemoteWithBranches(t)
	advance := t.TempDir()
	testutil.GitRun(t, "", "clone", bare, advance)
	testutil.GitRun(t, advance, "checkout", "main")
	testutil.GitRun(t, advance, "config", "user.email", "test@example.com")
	testutil.GitRun(t, advance, "config", "user.name", "Test")
	testutil.WriteFixture(t, advance, "later.txt", "later\n")
	testutil.GitRun(t, advance, "add", "later.txt")
	testutil.GitRun(t, advance, "commit", "-m", "later main commit")
	testutil.GitRun(t, advance, "push", "origin", "main")

	rel := filepath.Join("hooks", "pre-receive")
	testutil.WriteFixture(t, bare, rel, "#!/bin/sh\nsleep 999\n")
	if err := os.Chmod(filepath.Join(bare, rel), 0o755); err != nil {
		t.Fatalf("chmod pre-receive hook: %v", err)
	}

	// Every step gets its own op timeout, so it must outlast the local clone
	// and rebase on a loaded CI runner: 200ms once expired mid-rebase there,
	// failing before the push this test targets.
	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-",
		WithOpTimeout(2*time.Second))

	start := time.Now()
	err := g.Rebase("agent/issue-1")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Rebase against hanging push: want error, got nil")
	}
	if elapsed > 15*time.Second {
		t.Fatalf("Rebase took %s to return, want it bounded by the configured op timeout", elapsed)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Rebase error = %q, want it to mention timing out", err.Error())
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Rebase error = %v, want errors.Is(err, context.DeadlineExceeded)", err)
	}
	if !errors.Is(err, forge.ErrTransientPushFailure) {
		t.Fatalf("Rebase error = %v, want errors.Is(err, forge.ErrTransientPushFailure)", err)
	}
}

func TestGitClient_Rebase_ContentConflictReturnsErrMergeConflict(t *testing.T) {
	bare := conflictRemote(t, map[string]string{"shared.txt": "base\n"},
		func(w string) {
			testutil.WriteFixture(t, w, "shared.txt", "feature change\n")
			testutil.GitRun(t, w, "add", "shared.txt")
		},
		func(w string) {
			testutil.WriteFixture(t, w, "shared.txt", "conflicting main change\n")
			testutil.GitRun(t, w, "add", "shared.txt")
		})

	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-")
	if err := g.Rebase("agent/issue-1"); !errors.Is(err, forge.ErrMergeConflict) {
		t.Fatalf("Rebase: want forge.ErrMergeConflict, got: %v", err)
	}
}

func TestGitClient_Rebase_ModifyDeleteConflictReturnsErrMergeConflict(t *testing.T) {
	bare := conflictRemote(t, map[string]string{"shared.txt": "base\n"},
		func(w string) {
			testutil.WriteFixture(t, w, "shared.txt", "feature change\n")
			testutil.GitRun(t, w, "add", "shared.txt")
		},
		func(w string) { testutil.GitRun(t, w, "rm", "shared.txt") })

	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-")
	if err := g.Rebase("agent/issue-1"); !errors.Is(err, forge.ErrMergeConflict) {
		t.Fatalf("Rebase: want forge.ErrMergeConflict, got: %v", err)
	}
}

// Git prints neither "Merge conflict in" nor a content marker for a rename
// conflict, so this covers the "CONFLICT (" classification beyond content.
func TestGitClient_Rebase_RenameConflictReturnsErrMergeConflict(t *testing.T) {
	bare := conflictRemote(t, map[string]string{"shared.txt": "base\n"},
		func(w string) { testutil.GitRun(t, w, "mv", "shared.txt", "feature-name.txt") },
		func(w string) { testutil.GitRun(t, w, "mv", "shared.txt", "main-name.txt") })

	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-")
	if err := g.Rebase("agent/issue-1"); !errors.Is(err, forge.ErrMergeConflict) {
		t.Fatalf("Rebase: want forge.ErrMergeConflict, got: %v", err)
	}
}

// A rebase onto a base that does not exist fails without stopping on a
// conflict, so it must surface git's own message instead of masquerading as
// forge.ErrMergeConflict.
func TestGitClient_Rebase_MissingBaseIsNotConflict(t *testing.T) {
	// git translates its messages; the assertion below needs the C locale.
	t.Setenv("LC_ALL", "C")
	bare := newBareRemoteWithBranches(t)
	g := NewGitClient(bare, "no-such-base", "Test Bot", "bot@example.com", "agent/issue-")

	err := g.Rebase("agent/issue-1")
	if err == nil {
		t.Fatal("Rebase: want error for missing base, got nil")
	}
	if errors.Is(err, forge.ErrMergeConflict) {
		t.Fatalf("Rebase: missing base must not be forge.ErrMergeConflict: %v", err)
	}
	for _, want := range []string{"no-such-base", "invalid upstream"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Rebase error should carry git's own output (%q): %v", want, err)
		}
	}
}

// Mirrors the Merge hook-leak test for Rebase's non-conflict failure branch.
func TestGitClient_Rebase_HookOutputDoesNotLeakCredentials(t *testing.T) {
	const secret = "sometoken123"
	t.Setenv("LC_ALL", "C")
	// main must be ahead of the branch: pre-rebase does not fire when the
	// branch is already up to date.
	bare := conflictRemote(t, map[string]string{"shared.txt": "base\n"},
		func(w string) {
			testutil.WriteFixture(t, w, "feature.txt", "feature\n")
			testutil.GitRun(t, w, "add", "feature.txt")
		},
		func(w string) {
			testutil.WriteFixture(t, w, "later.txt", "later\n")
			testutil.GitRun(t, w, "add", "later.txt")
		})
	installLeakingHook(t, "pre-rebase", secret)

	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-")
	err := g.Rebase("agent/issue-1")
	if err == nil {
		t.Fatal("Rebase: want error from failing pre-rebase hook, got nil")
	}
	if errors.Is(err, forge.ErrMergeConflict) {
		t.Fatalf("Rebase: want non-conflict hook failure, got forge.ErrMergeConflict")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Rebase error leaks hook output credential: %v", err)
	}
	if !strings.Contains(err.Error(), "unable to access") {
		t.Fatalf("Rebase error should carry the redacted hook output: %v", err)
	}
}

func TestCloneToTemp_GitInDisablesAutoMaintenance(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	src := t.TempDir()
	testutil.GitRun(t, src, "init", "-q")
	testutil.GitRun(t, src, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init")
	_, gitIn, cleanup, err := cloneToTemp(src, "spindrift-git-forge-test-*", time.Minute)
	if err != nil {
		t.Fatalf("cloneToTemp: %v", err)
	}
	defer cleanup()
	for key, want := range map[string]string{"maintenance.auto": "false", "gc.auto": "0"} {
		out, err := gitIn(context.Background(), "config", "--get", key).Output()
		if err != nil {
			t.Fatalf("git config --get %s: %v", key, err)
		}
		if got := strings.TrimSpace(string(out)); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestCloneToTemp_PersistsAutoMaintenanceGuard(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	src := t.TempDir()
	testutil.GitRun(t, src, "init", "-q")
	testutil.GitRun(t, src, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init")
	dir, _, cleanup, err := cloneToTemp(src, "spindrift-git-forge-test-*", time.Minute)
	if err != nil {
		t.Fatalf("cloneToTemp: %v", err)
	}
	defer cleanup()
	for key, want := range map[string]string{"maintenance.auto": "false", "gc.auto": "0"} {
		if got := testutil.GitOutput(t, dir, "config", "--get", key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}
