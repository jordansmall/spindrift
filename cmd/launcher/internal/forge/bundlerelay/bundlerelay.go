// Package bundlerelay is the shared host-mediated bundle-relay helper backing
// both github's and forgejo's read-only RelayBundle: import a Box's code-out
// bundle into a fresh clone of the target repo and force-push it to origin,
// for a Box that cannot push directly
// (BOX_FORGE_AND_ISSUE_ACCESS=read-only). The backends differ only in how
// they clone the target repo, so that step is the sole parameter left to the
// caller; everything else lives here once.
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
// accepts the connection and then hangs server-side must not block the
// caller forever.
const RelayForcePushTimeout = 5 * time.Minute

// Relay imports ref from outboxDir/seambundle.FileName into a fresh clone of
// the target repo and force-pushes it to origin. clone must populate dir with
// an authenticated working clone and return its own fully-formatted error on
// failure -- Relay returns that error verbatim, since the closure is better
// placed to describe its own failure (forgejo redacts a tokened URL from its
// clone diagnostics first).
//
// A missing or malformed bundle is an error, never a silent no-op, so a
// broken hand-off blocks the seam instead of landing nothing. An absent
// bundle file returns forge.ErrBundleNotFound, the benign "Box wrote nothing"
// case; a present-but-unverifiable one returns a generic error the caller
// must not treat as a no-op.
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
	// ref came from a bundle fetch (refs/heads/ref created fresh in this
	// clone), so unlike Rebase's already-tracked head branch it has no
	// upstream for a bare force-with-lease to target -- an explicit
	// destination is required, first push or retried force-update alike.
	return gitplumbing.GitForcePush(ctx, dir, "-u", "origin", ref)
}

// CommitSubjects returns the one-line commit subjects that ref carries
// relative to base, oldest first -- settle's read-only PR-intent fallback,
// used to reconstruct a title/body host-side when the Box's outcome carries
// no usable PR-intent line. Unlike Relay it only runs `git log` against the
// clone: a read path that cannot mutate origin.
//
// clone must still populate dir with a full clone of the target repo, not an
// empty scratch dir: a bundle created as `base..branch` records base as a
// prerequisite commit, and fetching it into a repo lacking base's history
// fails with "Repository lacks these prerequisite commits" even though the
// payload only contains commits after base.
func CommitSubjects(backend, outboxDir, base, ref string, clone func(dir string) error) ([]string, error) {
	_, gitIn, cleanup, err := prepareBundleFetch(backend, outboxDir, ref, clone)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	// A --no-single-branch clone only creates a local branch for the clone's
	// own default branch; every other branch -- including base, whenever a
	// Target's BASE_BRANCH differs from its default -- exists only as
	// remote-tracking origin/base. Fall back to the bare name for a clone
	// that genuinely already has base as a local branch.
	baseRef := base
	if _, err := gitIn("rev-parse", "--verify", "origin/"+base).CombinedOutput(); err == nil {
		baseRef = "origin/" + base
	}

	// .Output(), not .CombinedOutput(): the bytes are parsed line-by-line as
	// data below, so any ambient warning git prints on stderr would otherwise
	// become a bogus subject -- and the reconstructed PR's title whenever it
	// sorts first.
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
// validate ref, confirm the bundle exists, create a scratch clone via clone,
// verify the bundle against it, and fetch ref into refs/heads/ref. Returns
// the clone's dir, a gitIn helper for the caller's follow-up command, and a
// cleanup func the caller must defer. On any error it has already cleaned up,
// so callers only defer cleanup once err is nil.
func prepareBundleFetch(backend, outboxDir, ref string, clone func(dir string) error) (dir string, gitIn func(args ...string) *exec.Cmd, cleanup func(), err error) {
	// Defense in depth: ref is launcher-controlled by the time it arrives, but
	// it interpolates directly into a refspec (and a `git log` revision range
	// for CommitSubjects), so guard it regardless.
	if ref == "" || strings.HasPrefix(ref, "-") {
		return "", nil, nil, fmt.Errorf("%s: relay bundle: invalid ref %q", backend, ref)
	}
	bundlePath := filepath.Join(outboxDir, seambundle.FileName)
	if _, err := os.Stat(bundlePath); err != nil {
		// An absent outbox directory collapses into this same case: "no dir"
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

	if err := clone(dir); err != nil {
		cleanup()
		return "", nil, nil, err
	}

	gitIn = func(args ...string) *exec.Cmd {
		return exec.Command("git", append([]string{"-C", dir}, args...)...)
	}
	// Verified against dir, not the ambient cwd: the bundle's prerequisite
	// commits must be reachable from *some* repo for `git bundle verify` to
	// succeed, and dir is the clone this function has in hand.
	if out, err := gitIn("bundle", "verify", bundlePath).CombinedOutput(); err != nil {
		cleanup()
		return "", nil, nil, fmt.Errorf("%s: malformed bundle %s: %w: %s", backend, bundlePath, err, out)
	}
	// The forced refspec lets a retried fix-pass's rebuilt bundle overwrite
	// the branch this clone may already know from the closure's own clone.
	if out, err := gitIn("fetch", bundlePath, "+"+ref+":refs/heads/"+ref).CombinedOutput(); err != nil {
		cleanup()
		return "", nil, nil, fmt.Errorf("%s: relay bundle: git fetch bundle: %w: %s", backend, err, out)
	}
	return dir, gitIn, cleanup, nil
}
