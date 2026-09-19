// Package bundlerelay imports a Box's code-out bundle into a fresh clone of
// the target repo and force-pushes it to origin, for a Box that cannot push
// directly (BOX_FORGE_AND_ISSUE_ACCESS=read-only, issue #2212). Cloning is
// the one step the caller supplies, because github and forgejo authenticate
// differently; every other step is identical and lives here.
package bundlerelay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/gitplumbing"
	"spindrift.dev/launcher/internal/seambundle"
)

// RelayForcePushTimeout bounds Relay's trailing force-push so a remote that
// accepts the connection and then hangs server-side cannot block the caller
// forever.
const RelayForcePushTimeout = 5 * time.Minute

// Relay imports ref from outboxDir/seambundle.FileName into a fresh clone of
// the target repo and force-pushes it to origin (issue #2212). A missing or
// malformed bundle is an error, never a silent no-op: an absent bundle file
// returns forge.ErrBundleNotFound, the benign "Box wrote nothing" case, and an
// unreadable or unverifiable one returns a generic error (issue #2096).
func Relay(backend, outboxDir, ref string, clone func(dir string) error) error {
	dir, gitIn, cleanup, err := prepareBundleFetch(backend, outboxDir, ref, clone)
	if err != nil {
		return err
	}
	defer cleanup()

	if out, err := gitIn("checkout", ref).CombinedOutput(); err != nil {
		return fmt.Errorf("%s: relay bundle: git checkout %s: %w: %s", backend, ref, err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), RelayForcePushTimeout)
	defer cancel()
	// ref came from a bundle fetch, so refs/heads/ref is fresh in this clone
	// and has no upstream for a bare force-with-lease to target. The
	// destination must be explicit, first push or retried force-update alike.
	return gitplumbing.GitForcePush(ctx, dir, "-u", "origin", ref)
}

// CommitSubjects returns the one-line commit subjects ref carries relative to
// base, oldest first, from the bundle at outboxDir/seambundle.FileName. It
// backs settle's read-only PR-intent fallback (issue #2447) and reports an
// absent bundle as forge.ErrBundleNotFound, as Relay does.
func CommitSubjects(backend, outboxDir, base, ref string, clone func(dir string) error) ([]string, error) {
	_, gitIn, cleanup, err := prepareBundleFetch(backend, outboxDir, ref, clone)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	// A --no-single-branch clone creates a local branch only for the clone's
	// own default branch, so base exists only as the remote-tracking
	// origin/base whenever a Target's BASE_BRANCH differs from it. Fall back to
	// the bare name for a clone that already has base as a local branch.
	baseRef := base
	if _, err := gitIn("rev-parse", "--verify", "origin/"+base).CombinedOutput(); err == nil {
		baseRef = "origin/" + base
	}

	// .Output(), not .CombinedOutput(): these bytes are parsed line-by-line as
	// commit subjects, so any warning git prints on stderr would become a bogus
	// subject and, whenever it sorts first, the reconstructed PR's title
	// (settle's reconstructPRText).
	out, err := gitIn("log", "--format=%s", "--reverse", baseRef+".."+ref).Output()
	if err != nil {
		var stderr []byte
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr = exitErr.Stderr
		}
		return nil, fmt.Errorf("%s: relay bundle: git log %s..%s: %w: %s", backend, baseRef, ref, err, stderr)
	}
	var subjects []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			subjects = append(subjects, line)
		}
	}
	return subjects, nil
}

// prepareBundleFetch is the shared preamble behind Relay and CommitSubjects:
// validate ref, confirm the bundle at outboxDir/seambundle.FileName exists,
// clone the target repo into a scratch dir, verify the bundle against that
// clone, and fetch ref into refs/heads/ref. On any error it has already
// cleaned up, so callers defer the returned cleanup only once err is nil.
func prepareBundleFetch(backend, outboxDir, ref string, clone func(dir string) error) (dir string, gitIn func(args ...string) *exec.Cmd, cleanup func(), err error) {
	// Defense in depth: callers derive ref from cf.AgentBranch(num) host-side,
	// but it still interpolates into a refspec and, for CommitSubjects, a `git
	// log` revision range, so guard it regardless of that holding upstream.
	if ref == "" || strings.HasPrefix(ref, "-") {
		return "", nil, nil, fmt.Errorf("%s: relay bundle: invalid ref %q", backend, ref)
	}
	bundlePath := filepath.Join(outboxDir, seambundle.FileName)
	if _, err := os.Stat(bundlePath); err != nil {
		// An absent outbox directory also yields os.IsNotExist, and "no dir"
		// means "nothing to relay" just as "no bundle file" does.
		if os.IsNotExist(err) {
			return "", nil, nil, fmt.Errorf("%s: relay bundle: %w: %s", backend, forge.ErrBundleNotFound, bundlePath)
		}
		return "", nil, nil, fmt.Errorf("%s: relay bundle: %w", backend, err)
	}
	dir, err = os.MkdirTemp("", "spindrift-relay-*")
	if err != nil {
		return "", nil, nil, fmt.Errorf("%s: relay bundle: mkdtemp: %w", backend, err)
	}
	cleanup = func() { os.RemoveAll(dir) }

	// clone must populate dir with a full, authenticated clone of the target
	// repo, not an empty scratch dir, and must return its own fully-formatted
	// error: callers return it verbatim, because forgejo redacts its token from
	// clone diagnostics first.
	if err := clone(dir); err != nil {
		cleanup()
		return "", nil, nil, err
	}

	gitIn = func(args ...string) *exec.Cmd {
		return exec.Command("git", append([]string{"-C", dir}, args...)...)
	}
	// Verified against dir, not the ambient cwd: `git bundle verify` needs the
	// bundle's prerequisite commits reachable from some repo, and dir is the
	// clone the closure just made. A `base..branch` bundle lists base as a
	// prerequisite, so a clone lacking base's history fails with "Repository
	// lacks these prerequisite commits" though the payload holds only new work.
	if out, err := gitIn("bundle", "verify", bundlePath).CombinedOutput(); err != nil {
		cleanup()
		return "", nil, nil, fmt.Errorf("%s: malformed bundle %s: %w: %s", backend, bundlePath, err, out)
	}
	// The forced refspec lets a retried fix-pass's rebuilt bundle overwrite the
	// branch this clone may already know from the closure's own initial clone.
	if out, err := gitIn("fetch", bundlePath, "+"+ref+":refs/heads/"+ref).CombinedOutput(); err != nil {
		cleanup()
		return "", nil, nil, fmt.Errorf("%s: relay bundle: git fetch bundle: %w: %s", backend, err, out)
	}
	return dir, gitIn, cleanup, nil
}
