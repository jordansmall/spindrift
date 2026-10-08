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

// invalidArg reports whether s is empty or starts with "-", the shared guard
// against a git positional arg parsing as an option.
func invalidArg(s string) bool {
	return s == "" || strings.HasPrefix(s, "-")
}

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

// PushBranch fetches localRef from the git repo at srcDir into a fresh clone
// of the target repo and pushes it to origin as branch, creating it (issue
// #4071, ADR 0057; create-only per issue #4104). Unlike Relay, which fetches a
// ref out of a one-shot bundle file, this fetches straight from a live local
// repo. It refuses branch == base up front, and any branch that already
// exists on origin at push time: its one caller, the butler patch rung, only
// ever publishes a just-filed finding's fresh agent branch.
func PushBranch(backend, srcDir, localRef, branch, base string, clone func(dir string) error) error {
	// Defense in depth, as prepareBundleFetch's ref guard: srcDir, localRef,
	// and branch reach git as positional args, where a leading "-" would
	// parse as an option. base never reaches git, but an empty or
	// leading-"-" base names no real branch, so the guard below would pass
	// every branch.
	if invalidArg(srcDir) {
		return fmt.Errorf("%s: push branch: invalid srcDir %q", backend, srcDir)
	}
	if invalidArg(localRef) {
		return fmt.Errorf("%s: push branch: invalid localRef %q", backend, localRef)
	}
	if invalidArg(branch) {
		return fmt.Errorf("%s: push branch: invalid branch %q", backend, branch)
	}
	if invalidArg(base) {
		return fmt.Errorf("%s: push branch: invalid base %q", backend, base)
	}
	// The create-only lease below refuses the base branch too, but only after
	// a clone and a fetch (issue #4104). Normalize both sides first: a
	// "refs/heads/"-qualified branch must still compare equal to a bare
	// base name.
	if strings.TrimPrefix(branch, "refs/heads/") == strings.TrimPrefix(base, "refs/heads/") {
		return fmt.Errorf("%s: push branch: branch %q is the base branch", backend, branch)
	}
	dir, gitIn, cleanup, err := cloneScratch(backend, "push branch", clone)
	if err != nil {
		return err
	}
	defer cleanup()

	if out, err := gitIn("fetch", srcDir, localRef).CombinedOutput(); err != nil {
		return fmt.Errorf("%s: push branch: git fetch %s %s: %w: %s", backend, srcDir, localRef, err, out)
	}
	// A fetch straight into refs/heads/branch would refuse when branch is the
	// clone's own checked-out default branch, so land FETCH_HEAD via a
	// checkout -B instead.
	if out, err := gitIn("checkout", "-B", branch, "FETCH_HEAD").CombinedOutput(); err != nil {
		return fmt.Errorf("%s: push branch: git checkout -B %s FETCH_HEAD: %w: %s", backend, branch, err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), RelayForcePushTimeout)
	defer cancel()
	// The empty expect means refs/heads/branch must not exist on origin. For
	// this ref it overrides the bare --force-with-lease's clone-relative
	// lease, which covers only the clone-to-push window (issue #4104).
	lease := "--force-with-lease=refs/heads/" + branch + ":"
	if err := gitplumbing.GitForcePush(ctx, dir, lease, "-u", "origin", branch); err != nil {
		// "stale info" is git's lease-mismatch marker, here only ever an
		// existing branch; a timeout or hook rejection passes through as is.
		if !strings.Contains(err.Error(), "stale info") {
			return err
		}
		return fmt.Errorf("%s: push branch: %s already exists on origin, create-only push refused: %w", backend, branch, err)
	}
	return nil
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

// cloneScratch mkdtemps a scratch dir, runs clone against it, and wires up a
// gitIn helper bound to that dir, the preamble Relay, CommitSubjects, and
// PushBranch all share; op prefixes its mkdtemp error to match the caller's. On
// any error it has already cleaned up, so callers defer the returned cleanup
// only once err is nil.
func cloneScratch(backend, op string, clone func(dir string) error) (dir string, gitIn func(args ...string) *exec.Cmd, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", "spindrift-relay-*")
	if err != nil {
		return "", nil, nil, fmt.Errorf("%s: %s: mkdtemp: %w", backend, op, err)
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

	// A fetch in the scratch clone can fork a detached `git maintenance
	// --auto` that is still writing .git/objects/pack when cleanup's
	// os.RemoveAll (or a caller's t.TempDir cleanup) runs, so disable auto
	// maintenance and gc.
	gitIn = func(args ...string) *exec.Cmd {
		return exec.Command("git", append([]string{"-C", dir, "-c", "gc.auto=0", "-c", "maintenance.auto=false"}, args...)...)
	}
	return dir, gitIn, cleanup, nil
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
	if invalidArg(ref) {
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
	dir, gitIn, cleanup, err = cloneScratch(backend, "relay bundle", clone)
	if err != nil {
		return "", nil, nil, err
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
