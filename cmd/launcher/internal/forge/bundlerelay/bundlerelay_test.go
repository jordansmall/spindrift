package bundlerelay

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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

// seedRelayBundle clones bare, creates branch one commit ahead of base
// carrying a marker file, and writes a git bundle of base..branch to
// outboxDir/seambundle.FileName -- standing in for the Box's code-out.
// Returns branch's HEAD sha.
func seedRelayBundle(t *testing.T, bare, base, outboxDir, branch string) string {
	t.Helper()
	work := t.TempDir()
	run(t, "", "clone", bare, work)
	run(t, work, "checkout", base)
	run(t, work, "checkout", "-b", branch)
	writeFile(t, filepath.Join(work, "feature.txt"), "feature\n")
	run(t, work, "add", "feature.txt")
	run(t, work, "commit", "-m", "feature")
	run(t, work, "bundle", "create", filepath.Join(outboxDir, seambundle.FileName), base+".."+branch)
	return revParse(t, work, branch)
}

// run runs `git -C dir args...`, failing t on error.
func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// revParse returns the commit ref resolves to inside the repo at dir.
func revParse(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", ref).CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse %s in %s: %v: %s", ref, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// writeFile writes contents to path, failing t on error.
func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newBundleRelayHarness(t *testing.T) *forgetest.GitRepoFixture {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")
	return forgetest.NewGitRepoFixture(t, "main")
}

// TestRelay_PushesRefToOrigin asserts a valid bundle relays ref to origin
// via the caller-supplied clone closure, and origin's ref ends up pointing
// at the bundled commit.
func TestRelay_PushesRefToOrigin(t *testing.T) {
	repo := newBundleRelayHarness(t)
	outbox := t.TempDir()
	branch := "agent/issue-2212"
	wantSHA := seedRelayBundle(t, repo.Bare, "main", outbox, branch)

	if err := Relay("test", outbox, branch, localClone(repo.Bare)); err != nil {
		t.Fatalf("Relay: %v", err)
	}

	if got := revParse(t, repo.Bare, "refs/heads/"+branch); got != wantSHA {
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
	writeFile(t, filepath.Join(outbox, seambundle.FileName), "not a bundle")

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
	seedRelayBundle(t, repo.Bare, "main", outbox, "agent/issue-2212")

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
