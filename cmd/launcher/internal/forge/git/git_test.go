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

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func gitWriteFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
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

	gitRun(t, "", "init", "--bare", bare)
	gitRun(t, "", "clone", bare, work)
	gitRun(t, work, "checkout", "-B", "main")
	gitRun(t, work, "config", "user.email", "test@example.com")
	gitRun(t, work, "config", "user.name", "Test")
	gitWriteFile(t, filepath.Join(work, "base.txt"), "base\n")
	gitRun(t, work, "add", "base.txt")
	gitRun(t, work, "commit", "-m", "base")
	gitRun(t, work, "push", "-u", "origin", "main")

	gitRun(t, work, "checkout", "-b", "agent/issue-1")
	gitWriteFile(t, filepath.Join(work, "feature.txt"), "feature\n")
	gitRun(t, work, "add", "feature.txt")
	gitRun(t, work, "commit", "-m", "feature")
	gitRun(t, work, "push", "-u", "origin", "agent/issue-1")

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
	gitRun(t, "", "clone", bare, verify)
	gitRun(t, verify, "checkout", "main")
	if _, err := os.Stat(filepath.Join(verify, "feature.txt")); err != nil {
		t.Errorf("main does not contain feature.txt after Merge: %v", err)
	}
}

func TestGitClient_Merge_ConflictReturnsErrMergeConflict(t *testing.T) {
	dir := t.TempDir()
	bare := filepath.Join(dir, "origin.git")
	work := filepath.Join(dir, "work")

	gitRun(t, "", "init", "--bare", bare)
	gitRun(t, "", "clone", bare, work)
	gitRun(t, work, "checkout", "-B", "main")
	gitRun(t, work, "config", "user.email", "test@example.com")
	gitRun(t, work, "config", "user.name", "Test")
	gitWriteFile(t, filepath.Join(work, "shared.txt"), "base\n")
	gitRun(t, work, "add", "shared.txt")
	gitRun(t, work, "commit", "-m", "base")
	gitRun(t, work, "push", "-u", "origin", "main")

	gitRun(t, work, "checkout", "-b", "agent/issue-1")
	gitWriteFile(t, filepath.Join(work, "shared.txt"), "feature change\n")
	gitRun(t, work, "add", "shared.txt")
	gitRun(t, work, "commit", "-m", "feature")
	gitRun(t, work, "push", "-u", "origin", "agent/issue-1")

	gitRun(t, work, "checkout", "main")
	gitWriteFile(t, filepath.Join(work, "shared.txt"), "conflicting main change\n")
	gitRun(t, work, "add", "shared.txt")
	gitRun(t, work, "commit", "-m", "conflicting")
	gitRun(t, work, "push", "origin", "main")

	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-")
	err := g.Merge("agent/issue-1")
	if err != forge.ErrMergeConflict {
		t.Fatalf("Merge: want forge.ErrMergeConflict, got: %v", err)
	}
}

func TestGitClient_Rebase_ForcePushesRebasedBranch(t *testing.T) {
	dir := t.TempDir()
	bare := filepath.Join(dir, "origin.git")
	work := filepath.Join(dir, "work")

	gitRun(t, "", "init", "--bare", bare)
	gitRun(t, "", "clone", bare, work)
	gitRun(t, work, "checkout", "-B", "main")
	gitRun(t, work, "config", "user.email", "test@example.com")
	gitRun(t, work, "config", "user.name", "Test")
	gitWriteFile(t, filepath.Join(work, "base.txt"), "base\n")
	gitRun(t, work, "add", "base.txt")
	gitRun(t, work, "commit", "-m", "base")
	gitRun(t, work, "push", "-u", "origin", "main")

	gitRun(t, work, "checkout", "-b", "agent/issue-1")
	gitWriteFile(t, filepath.Join(work, "feature.txt"), "feature\n")
	gitRun(t, work, "add", "feature.txt")
	gitRun(t, work, "commit", "-m", "feature")
	gitRun(t, work, "push", "-u", "origin", "agent/issue-1")

	// Advance main so the feature branch is now behind.
	gitRun(t, work, "checkout", "main")
	gitWriteFile(t, filepath.Join(work, "later.txt"), "later\n")
	gitRun(t, work, "add", "later.txt")
	gitRun(t, work, "commit", "-m", "later main commit")
	gitRun(t, work, "push", "origin", "main")

	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-")
	if err := g.Rebase("agent/issue-1"); err != nil {
		t.Fatalf("Rebase: %v", err)
	}

	verify := t.TempDir()
	gitRun(t, "", "clone", bare, verify)
	gitRun(t, verify, "checkout", "agent/issue-1")
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
	gitRun(t, "", "clone", bare, verify)
	gitRun(t, verify, "checkout", "main")
	out := exec.Command("git", "-C", verify, "log", "-1", "--format=%an <%ae>", "main")
	got, err := out.Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if strings.TrimSpace(string(got)) != "Spindrift Bot <bot@example.com>" {
		t.Errorf("merge commit identity = %q, want \"Spindrift Bot <bot@example.com>\"", strings.TrimSpace(string(got)))
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

// This test crosses the same public-comment trust boundary as the clone and
// probe tests above, but reaches it through git.go's non-conflict merge
// branch: the credential text comes from a rejecting hook's own output rather
// than from the remote URL.
func TestGitClient_Merge_HookOutputDoesNotLeakCredentials(t *testing.T) {
	const secret = "sometoken123"
	bare := newBareRemoteWithBranches(t)

	// init.templateDir is how the hook reaches the fresh clone cloneToTemp
	// makes. The hook stands in for a merge driver that leaks a
	// credential-bearing URL: it rejects the merge and writes the credential
	// to stderr, which mergeCmd captures.
	home := t.TempDir()
	templateDir := filepath.Join(home, "template")
	hooksDir := filepath.Join(templateDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hookPath := filepath.Join(hooksDir, "pre-merge-commit")
	gitWriteFile(t, hookPath, "#!/bin/sh\necho 'fatal: unable to access "+
		"https://oauth2:"+secret+"@git.example.com/org/repo.git/' >&2\nexit 1\n")
	if err := os.Chmod(hookPath, 0o755); err != nil {
		t.Fatal(err)
	}
	gitWriteFile(t, filepath.Join(home, ".gitconfig"), "[init]\n\ttemplateDir = "+templateDir+"\n")
	t.Setenv("HOME", home)

	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-")
	err := g.Merge("agent/issue-1")
	if err == nil {
		t.Fatal("Merge: want error from failing pre-commit hook, got nil")
	}
	if err == forge.ErrMergeConflict {
		t.Fatal("Merge: want non-conflict hook failure, got forge.ErrMergeConflict")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Merge error leaks hook output credential: %v", err)
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
	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-")

	exists, err := g.BranchExists("agent/issue-1")
	if err != nil {
		t.Fatalf("BranchExists(agent/issue-1): %v", err)
	}
	if !exists {
		t.Error("BranchExists(agent/issue-1) = false, want true — the branch was pushed")
	}

	exists, err = g.BranchExists("agent/issue-999")
	if err != nil {
		t.Fatalf("BranchExists(agent/issue-999): %v", err)
	}
	if exists {
		t.Error("BranchExists(agent/issue-999) = true, want false — the branch was never pushed")
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
	hook := filepath.Join(bare, "hooks", "pre-receive")
	gitWriteFile(t, hook, "#!/bin/sh\nsleep 999\n")
	if err := os.Chmod(hook, 0o755); err != nil {
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

// installHangingGitRebaseShim puts a "git" shim ahead of the real one on
// PATH. It sleeps forever on a non-abort `git ... rebase <ref>` and passes
// every other subcommand through. Rebase's checkout and rebase steps run
// locally, with no network round trip that hangingRemoteURL or a pre-receive
// hook could hang, so the shim is the only deterministic way to stall them.
func installHangingGitRebaseShim(t *testing.T) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("look up real git: %v", err)
	}
	shimDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$3\" = \"rebase\" ] && [ \"$4\" != \"--abort\" ]; then\n" +
		"  sleep 999\n" +
		"fi\n" +
		"exec " + realGit + " \"$@\"\n"
	shim := filepath.Join(shimDir, "git")
	gitWriteFile(t, shim, script)
	if err := os.Chmod(shim, 0o755); err != nil {
		t.Fatalf("chmod git shim: %v", err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// The op timeout has to cover Rebase's post-clone subprocesses (checkout,
// rebase) too, not just the clone.
func TestGitClient_Rebase_TimesOutOnHangingRebase(t *testing.T) {
	bare := newBareRemoteWithBranches(t)
	installHangingGitRebaseShim(t)
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
	gitRun(t, "", "clone", bare, advance)
	gitRun(t, advance, "checkout", "main")
	gitRun(t, advance, "config", "user.email", "test@example.com")
	gitRun(t, advance, "config", "user.name", "Test")
	gitWriteFile(t, filepath.Join(advance, "later.txt"), "later\n")
	gitRun(t, advance, "add", "later.txt")
	gitRun(t, advance, "commit", "-m", "later main commit")
	gitRun(t, advance, "push", "origin", "main")

	hook := filepath.Join(bare, "hooks", "pre-receive")
	gitWriteFile(t, hook, "#!/bin/sh\nsleep 999\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatalf("chmod pre-receive hook: %v", err)
	}

	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-",
		WithOpTimeout(200*time.Millisecond))

	start := time.Now()
	err := g.Rebase("agent/issue-1")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Rebase against hanging push: want error, got nil")
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
