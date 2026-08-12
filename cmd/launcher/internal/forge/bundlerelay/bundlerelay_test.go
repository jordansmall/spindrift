package bundlerelay

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/seambundle"
)

// localClone returns a clone closure that does a plain local `git clone
// bare dir` -- standing in for a backend's own gh/token-authenticated
// clone, since Relay's contract only requires that dir end up a working
// clone of the target repo, not how it got there.
func localClone(bare string) func(dir string) error {
	return func(dir string) error {
		if _, err := exec.Command("git", "clone", "--no-single-branch", bare, dir).CombinedOutput(); err != nil {
			return err
		}
		return nil
	}
}

// seedRelayBundleCommits mirrors forgetest.SeedRelayBundle but seeds branch
// with one commit per subject in subjects, in order, then writes a bundle of
// base..branch -- letting a test prove CommitSubjects preserves oldest-first
// order across more than one commit, not just the trivial single-commit case.
func seedRelayBundleCommits(t *testing.T, bare, base, outboxDir, branch string, subjects ...string) {
	t.Helper()
	work := t.TempDir()
	forgetest.Run(t, "", "clone", bare, work)
	forgetest.Run(t, work, "checkout", base)
	forgetest.Run(t, work, "checkout", "-b", branch)
	for _, subject := range subjects {
		forgetest.WriteFile(t, filepath.Join(work, "feature.txt"), subject+"\n")
		forgetest.Run(t, work, "add", "feature.txt")
		forgetest.Run(t, work, "commit", "-m", subject)
	}
	forgetest.Run(t, work, "bundle", "create", filepath.Join(outboxDir, seambundle.FileName), base+".."+branch)
}

func newBundleRelayHarness(t *testing.T) *forgetest.GitRepoFixture {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")
	repo := forgetest.NewGitRepoFixture(t, "main")
	// A raw `git init --bare` leaves HEAD pointing at whatever
	// init.defaultBranch happened to be at creation time (often "master"),
	// regardless of which branch NewGitRepoFixture actually pushed -- unlike
	// a real forge remote, which always keeps HEAD correctly pointed at its
	// actual default branch. Point it at "main" explicitly so a
	// --no-single-branch clone of repo.Bare (localClone) resolves "main" as
	// a local branch, not just the remote-tracking "origin/main" --
	// CommitSubjects's base argument needs "main" itself to resolve for
	// `git log base..ref` to work, the same way it would against a real
	// forge clone.
	if out, err := exec.Command("git", "-C", repo.Bare, "symbolic-ref", "HEAD", "refs/heads/main").CombinedOutput(); err != nil {
		t.Fatalf("set bare repo HEAD to refs/heads/main: %v: %s", err, out)
	}
	return repo
}

// TestRelay_PushesRefToOrigin asserts a valid bundle relays ref to origin
// via the caller-supplied clone closure, and origin's ref ends up pointing
// at the bundled commit.
func TestRelay_PushesRefToOrigin(t *testing.T) {
	repo := newBundleRelayHarness(t)
	outbox := t.TempDir()
	branch := "agent/issue-2212"
	wantSHA := forgetest.SeedRelayBundle(t, repo.Bare, "main", outbox, branch)

	if err := Relay("test", outbox, branch, localClone(repo.Bare)); err != nil {
		t.Fatalf("Relay: %v", err)
	}

	if got := forgetest.RevParse(t, repo.Bare, "refs/heads/"+branch); got != wantSHA {
		t.Errorf("refs/heads/%s = %s, want %s", branch, got, wantSHA)
	}
}

// TestRelay_InvalidRefErrorsBeforeFilesystemWork asserts an empty or
// dash-prefixed ref is rejected up front, before Relay ever touches the
// filesystem (outboxDir is a nonexistent path here, so any filesystem work
// would itself fail differently).
func TestRelay_InvalidRefErrorsBeforeFilesystemWork(t *testing.T) {
	for _, ref := range []string{"", "-x"} {
		t.Run(ref, func(t *testing.T) {
			called := false
			err := Relay("test", "/nonexistent/outbox/dir", ref, func(dir string) error {
				called = true
				return nil
			})
			if err == nil {
				t.Fatal("Relay with invalid ref: got nil error, want one")
			}
			if !strings.Contains(err.Error(), "invalid ref") {
				t.Errorf("Relay with invalid ref: err = %v, want it to mention %q", err, "invalid ref")
			}
			if called {
				t.Error("Relay with invalid ref: clone closure was called, want it skipped")
			}
		})
	}
}

// TestRelay_MissingBundleErrors asserts an empty outbox (the Box never wrote
// a bundle) blocks the seam via an error that wraps forge.ErrBundleNotFound.
func TestRelay_MissingBundleErrors(t *testing.T) {
	repo := newBundleRelayHarness(t)
	outbox := t.TempDir()

	err := Relay("test", outbox, "agent/issue-2212", localClone(repo.Bare))
	if err == nil {
		t.Fatal("Relay with no bundle file present: got nil error, want one")
	}
	if !errors.Is(err, forge.ErrBundleNotFound) {
		t.Errorf("Relay with no bundle file present: err = %v, want errors.Is(err, forge.ErrBundleNotFound)", err)
	}
}

// TestRelay_MissingOutboxDirErrors asserts a wholly-absent outbox directory
// collapses into the same forge.ErrBundleNotFound case as a present dir with
// no bundle file in it.
func TestRelay_MissingOutboxDirErrors(t *testing.T) {
	repo := newBundleRelayHarness(t)
	outbox := filepath.Join(t.TempDir(), "does-not-exist")

	err := Relay("test", outbox, "agent/issue-2212", localClone(repo.Bare))
	if err == nil {
		t.Fatal("Relay with no outbox dir present: got nil error, want one")
	}
	if !errors.Is(err, forge.ErrBundleNotFound) {
		t.Errorf("Relay with no outbox dir present: err = %v, want errors.Is(err, forge.ErrBundleNotFound)", err)
	}
}

// TestRelay_MalformedBundleErrors asserts a corrupt bundle file is rejected
// by `git bundle verify` with a generic error, not forge.ErrBundleNotFound.
func TestRelay_MalformedBundleErrors(t *testing.T) {
	repo := newBundleRelayHarness(t)
	outbox := t.TempDir()
	forgetest.WriteFile(t, filepath.Join(outbox, seambundle.FileName), "not a bundle")

	err := Relay("test", outbox, "agent/issue-2212", localClone(repo.Bare))
	if err == nil {
		t.Fatal("Relay with a malformed bundle file: got nil error, want one")
	}
	if errors.Is(err, forge.ErrBundleNotFound) {
		t.Errorf("Relay with a malformed bundle file: err = %v, want a generic error, not forge.ErrBundleNotFound", err)
	}
}

// TestRelay_CloneErrorPropagatesVerbatim asserts a clone closure's own
// fully-formatted error is returned unchanged, not re-wrapped by Relay.
func TestRelay_CloneErrorPropagatesVerbatim(t *testing.T) {
	repo := newBundleRelayHarness(t)
	outbox := t.TempDir()
	forgetest.SeedRelayBundle(t, repo.Bare, "main", outbox, "agent/issue-2212")

	sentinel := errors.New("sentinel clone failure")
	err := Relay("test", outbox, "agent/issue-2212", func(dir string) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Relay with a failing clone closure: err = %v, want exactly sentinel %v", err, sentinel)
	}
	if err != sentinel {
		t.Errorf("Relay with a failing clone closure: err = %v (%T), want the exact sentinel value unwrapped", err, sentinel)
	}
}

// TestCommitSubjects_ReturnsSubjectOldestFirst asserts a single-commit branch
// yields that one commit's subject.
func TestCommitSubjects_ReturnsSubjectOldestFirst(t *testing.T) {
	repo := newBundleRelayHarness(t)
	outbox := t.TempDir()
	branch := "agent/issue-2447"
	forgetest.SeedRelayBundle(t, repo.Bare, "main", outbox, branch)

	got, err := CommitSubjects("test", outbox, "main", branch, localClone(repo.Bare))
	if err != nil {
		t.Fatalf("CommitSubjects: %v", err)
	}
	want := []string{"feature"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CommitSubjects = %v, want %v", got, want)
	}
}

// TestCommitSubjects_MultipleCommitsOldestFirst asserts a multi-commit branch
// returns subjects in commit order (oldest first), not reversed -- proving
// the --reverse flag on the underlying `git log` actually took effect rather
// than happening to pass with a single commit where order is unobservable.
func TestCommitSubjects_MultipleCommitsOldestFirst(t *testing.T) {
	repo := newBundleRelayHarness(t)
	outbox := t.TempDir()
	branch := "agent/issue-2447"
	seedRelayBundleCommits(t, repo.Bare, "main", outbox, branch, "first", "second")

	got, err := CommitSubjects("test", outbox, "main", branch, localClone(repo.Bare))
	if err != nil {
		t.Fatalf("CommitSubjects: %v", err)
	}
	want := []string{"first", "second"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CommitSubjects = %v, want %v", got, want)
	}
}

// TestCommitSubjects_ResolvesNonDefaultBaseBranch asserts CommitSubjects
// resolves base even when base is not the scratch clone's default (HEAD)
// branch -- "non-default" here meaning whatever branch the clone's HEAD
// actually points at, "main" in this harness. A --no-single-branch clone
// (localClone) only checks out a local branch for the clone's default
// branch; every other branch is fetched as a remote-tracking
// origin/<branch>, never a local branch of the same name. This matters
// because a Target's BASE_BRANCH config need not be its default branch --
// before the fix, `git log base..ref` against the bare base name failed for
// any such non-default base with an "unknown revision" error, since base
// only existed as origin/base in the scratch clone.
func TestCommitSubjects_ResolvesNonDefaultBaseBranch(t *testing.T) {
	repo := newBundleRelayHarness(t)
	outbox := t.TempDir()
	branch := "agent/issue-2447"

	// Push "release" as a second branch off "main" directly into repo.Bare,
	// without ever touching HEAD there -- "main" stays the clone's default
	// branch and "release" is a genuine non-default one.
	work := t.TempDir()
	forgetest.Run(t, "", "clone", repo.Bare, work)
	forgetest.Run(t, work, "checkout", "-b", "release")
	forgetest.Run(t, work, "push", "origin", "release")

	forgetest.SeedRelayBundle(t, repo.Bare, "release", outbox, branch)

	got, err := CommitSubjects("test", outbox, "release", branch, localClone(repo.Bare))
	if err != nil {
		t.Fatalf("CommitSubjects: %v", err)
	}
	want := []string{"feature"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CommitSubjects = %v, want %v", got, want)
	}
}

// TestCommitSubjects_MissingBundleErrors asserts an empty outbox (the Box
// never wrote a bundle) errors via forge.ErrBundleNotFound, mirroring Relay's
// own TestRelay_MissingBundleErrors.
func TestCommitSubjects_MissingBundleErrors(t *testing.T) {
	repo := newBundleRelayHarness(t)
	outbox := t.TempDir()

	_, err := CommitSubjects("test", outbox, "main", "agent/issue-2447", localClone(repo.Bare))
	if err == nil {
		t.Fatal("CommitSubjects with no bundle file present: got nil error, want one")
	}
	if !errors.Is(err, forge.ErrBundleNotFound) {
		t.Errorf("CommitSubjects with no bundle file present: err = %v, want errors.Is(err, forge.ErrBundleNotFound)", err)
	}
}

// TestCommitSubjects_MalformedBundleErrors asserts a corrupt bundle file is
// rejected by `git bundle verify` with a generic error, not
// forge.ErrBundleNotFound, mirroring Relay's own
// TestRelay_MalformedBundleErrors.
func TestCommitSubjects_MalformedBundleErrors(t *testing.T) {
	repo := newBundleRelayHarness(t)
	outbox := t.TempDir()
	forgetest.WriteFile(t, filepath.Join(outbox, seambundle.FileName), "not a bundle")

	_, err := CommitSubjects("test", outbox, "main", "agent/issue-2447", localClone(repo.Bare))
	if err == nil {
		t.Fatal("CommitSubjects with a malformed bundle file: got nil error, want one")
	}
	if errors.Is(err, forge.ErrBundleNotFound) {
		t.Errorf("CommitSubjects with a malformed bundle file: err = %v, want a generic error, not forge.ErrBundleNotFound", err)
	}
}

// TestCommitSubjects_DoesNotMutateOrigin asserts CommitSubjects never pushes
// or otherwise mutates repo.Bare -- unlike Relay, whose whole job is to land
// ref there, CommitSubjects only reads what the bundle carries.
func TestCommitSubjects_DoesNotMutateOrigin(t *testing.T) {
	repo := newBundleRelayHarness(t)
	outbox := t.TempDir()
	branch := "agent/issue-2447"
	forgetest.SeedRelayBundle(t, repo.Bare, "main", outbox, branch)

	if _, err := CommitSubjects("test", outbox, "main", branch, localClone(repo.Bare)); err != nil {
		t.Fatalf("CommitSubjects: %v", err)
	}

	out, err := exec.Command("git", "-C", repo.Bare, "rev-parse", "--verify", "refs/heads/"+branch).CombinedOutput()
	if err == nil {
		t.Errorf("CommitSubjects must not push to origin: refs/heads/%s exists in repo.Bare: %s", branch, out)
	}
}

// installGitLogStderrNoiseShim puts a "git" shim ahead of the real one on
// PATH that, for a `git ... log ...` invocation only (matching CommitSubjects'
// own `git -C dir log ...` shape, where $3 is the subcommand), first writes
// noise to stderr and then execs the real git log with the same arguments
// unchanged, so stdout still carries the genuine commit subject(s). Every
// other subcommand (clone, rev-parse, bundle, fetch, checkout) delegates to
// the real git binary untouched -- this mirrors
// git_test.go's installHangingGitRebaseShim.
func installGitLogStderrNoiseShim(t *testing.T, noise string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("look up real git: %v", err)
	}
	shimDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$3\" = \"log\" ]; then\n" +
		"  echo '" + noise + "' >&2\n" +
		"fi\n" +
		"exec " + realGit + " \"$@\"\n"
	shim := filepath.Join(shimDir, "git")
	forgetest.WriteFile(t, shim, script)
	if err := os.Chmod(shim, 0o755); err != nil {
		t.Fatalf("chmod git shim: %v", err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestCommitSubjects_IgnoresGitStderrNoise asserts that ambient stderr noise
// from the underlying `git log` invocation (e.g. an advice/hint message)
// never leaks into the parsed subjects slice -- that slice becomes the
// reconstructed PR's title (first subject) and body bullet list in settle's
// reconstructPRText, so stderr noise sorting first would otherwise become a
// bogus fake PR title.
func TestCommitSubjects_IgnoresGitStderrNoise(t *testing.T) {
	repo := newBundleRelayHarness(t)
	outbox := t.TempDir()
	branch := "agent/issue-2447"
	forgetest.SeedRelayBundle(t, repo.Bare, "main", outbox, branch)

	installGitLogStderrNoiseShim(t, "hint: this is not a commit subject")

	got, err := CommitSubjects("test", outbox, "main", branch, localClone(repo.Bare))
	if err != nil {
		t.Fatalf("CommitSubjects: %v", err)
	}
	want := []string{"feature"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CommitSubjects = %v, want %v (git log stderr noise must not leak into parsed subjects)", got, want)
	}
}
