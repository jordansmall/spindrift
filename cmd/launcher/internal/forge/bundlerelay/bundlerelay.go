// Package bundlerelay is the shared host-mediated bundle-relay helper
// backing both github's and forgejo's read-only RelayBundle (issue #2212):
// import a Box's code-out bundle into a fresh clone of the target repo and
// force-push it to origin, for a Box that cannot push directly
// (BOX_FORGE_AND_ISSUE_ACCESS=read-only). The two backends differ only in
// how they clone the target repo -- github uses `gh repo clone` with its own
// gh-cli credential, forgejo uses `git clone` against a token-bearing remote
// URL with credential redaction on failure -- so that one step is the sole
// parameter left to the caller; everything else (ref validation, bundle
// presence/validity checks, fetch, checkout, force-push) is identical and
// lives here once.
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
// the target repo and force-pushes it to origin -- the shared body behind
// both github's and forgejo's RelayBundle (issue #2212). clone is the one
// step Relay does not own: it must populate dir with a working clone of the
// target repo, authenticated however that backend authenticates (gh-cli for
// github, a token-bearing remote URL for forgejo), and return its own
// fully-formatted error on failure -- Relay returns that error verbatim,
// never re-wrapping it, since the closure is already in the best position to
// describe its own failure (e.g. forgejo redacts a tokened URL from its
// clone diagnostics before this ever gets called).
//
// A missing or malformed bundle is returned as an error, never a silent
// no-op, so a broken hand-off blocks the seam instead of landing nothing.
// The two failure modes are distinguished (issue #2096): an absent bundle
// file returns forge.ErrBundleNotFound, the benign "Box wrote nothing" case,
// while a bundle that is present but unreadable or fails `git bundle verify`
// returns a generic error, since that's a genuine relay failure the caller
// should not treat as a no-op.
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
	// Unlike Rebase's already-tracked head branch, ref came from a bundle
	// fetch (refs/heads/ref created fresh in this clone), so it has no
	// upstream for a bare force-with-lease to target -- an explicit
	// destination is required, first push or retried force-update alike.
	return gitplumbing.GitForcePush(ctx, dir, "-u", "origin", ref)
}

// CommitSubjects returns the one-line commit subjects that ref carries
// relative to base, according to the bundle at outboxDir/seambundle.FileName,
// oldest first -- settle's read-only PR-intent-fallback hook (issue #2447):
// when a read-only Box's status=ready outcome carries no usable PR-intent
// line, settle still has the relayed branch's own commits to reconstruct a
// title/body from host-side, rather than blocking the hand-off outright.
//
// It shares Relay's own preamble (ref/bundle validation, a temp clone via
// clone, bundle verify, bundle fetch into refs/heads/ref) via
// prepareBundleFetch, including the same forge.ErrBundleNotFound-vs-generic-
// error split for an absent-vs-malformed bundle. Where Relay then checks ref
// out and force-pushes it to origin, CommitSubjects instead runs `git log`
// against the clone and returns its subjects -- it never checks anything out
// and never pushes, so unlike Relay it cannot mutate origin; this is a read
// path only.
//
// clone must still populate dir with a full clone of the target repo, not an
// empty scratch dir, exactly as Relay requires: a bundle created as
// `base..branch` records base as a prerequisite commit its own history must
// satisfy before `git bundle verify`/`fetch` will accept it -- proven
// empirically, fetching such a bundle into a repo that lacks base's own
// history fails with "Repository lacks these prerequisite commits", even
// though the bundle's payload only contains commits after base.
func CommitSubjects(backend, outboxDir, base, ref string, clone func(dir string) error) ([]string, error) {
	_, gitIn, cleanup, err := prepareBundleFetch(backend, outboxDir, ref, clone)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	// A --no-single-branch clone (every real clone closure) only checks out a
	// local branch for the clone's own default branch (wherever its HEAD
	// points); every other branch -- including base, whenever a Target's
	// BASE_BRANCH config differs from its default branch -- exists only as
	// the remote-tracking origin/base, never a local branch of the same
	// name. Prefer origin/base when it resolves; fall back to the bare base
	// name for a clone that isn't a full --no-single-branch clone, or that
	// genuinely already has base as a local branch.
	baseRef := base
	if _, err := gitIn("rev-parse", "--verify", "origin/"+base).CombinedOutput(); err == nil {
		baseRef = "origin/" + base
	}

	// .Output(), not .CombinedOutput(): the returned bytes are parsed
	// line-by-line as data (commit subjects) below, so stdout must never be
	// conflated with stderr the way .CombinedOutput() would -- any ambient
	// warning/hint git prints on stderr would otherwise silently become a
	// bogus fake subject, which becomes the reconstructed PR's title
	// (settle's reconstructPRText) whenever it sorts first.
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

// prepareBundleFetch is the shared preamble behind both Relay and
// CommitSubjects: validate ref, confirm the bundle at
// outboxDir/seambundle.FileName exists, create a scratch clone of the target
// repo via clone, verify the bundle against that clone, and fetch ref from it
// into refs/heads/ref. It returns the scratch clone's dir, a gitIn helper
// (`git -C dir ...`) for the caller's own follow-up command (checkout+push
// for Relay, log for CommitSubjects), and a cleanup func the caller must
// defer to remove the scratch clone. On any error it has already cleaned up
// after itself, so callers only need to defer cleanup once err is nil.
func prepareBundleFetch(backend, outboxDir, ref string, clone func(dir string) error) (dir string, gitIn func(args ...string) *exec.Cmd, cleanup func(), err error) {
	// Defense in depth: callers derive ref from cf.AgentBranch(num) host-side
	// and never forward untrusted input here, so ref is launcher-controlled by
	// the time it reaches this function. It still interpolates directly into a
	// refspec (and, for CommitSubjects, a `git log` revision range), so guard
	// it the same way regardless of that guarantee holding upstream.
	if ref == "" || strings.HasPrefix(ref, "-") {
		return "", nil, nil, fmt.Errorf("%s: relay bundle: invalid ref %q", backend, ref)
	}
	bundlePath := filepath.Join(outboxDir, seambundle.FileName)
	if _, err := os.Stat(bundlePath); err != nil {
		// An absent outbox directory collapses into this same case: a missing
		// dir also yields os.IsNotExist, and "no dir" means "nothing to relay"
		// just as "no bundle file" does -- both are the benign empty-range case.
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
	// commit(s) -- everything on the base side of the Box's base..branch
	// range -- must be reachable from *some* repo for `git bundle verify` to
	// succeed, and dir (the clone the closure just made from origin) is the
	// one this function has in hand.
	if out, err := gitIn("bundle", "verify", bundlePath).CombinedOutput(); err != nil {
		cleanup()
		return "", nil, nil, fmt.Errorf("%s: malformed bundle %s: %w: %s", backend, bundlePath, err, out)
	}
	// The forced refspec lets a retried fix-pass's rebuilt bundle overwrite
	// the branch this clone may already know about from the closure's own
	// initial clone/fetch.
	if out, err := gitIn("fetch", bundlePath, "+"+ref+":refs/heads/"+ref).CombinedOutput(); err != nil {
		cleanup()
		return "", nil, nil, fmt.Errorf("%s: relay bundle: git fetch bundle: %w: %s", backend, err, out)
	}
	return dir, gitIn, cleanup, nil
}
