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
	// Defense in depth: callers derive ref from cf.AgentBranch(num) host-side
	// and never forward untrusted input here, so ref is launcher-controlled by
	// the time it reaches this function. It still interpolates directly into a
	// refspec and a checkout argument, so guard it the same way regardless of
	// that guarantee holding upstream.
	if ref == "" || strings.HasPrefix(ref, "-") {
		return fmt.Errorf("%s: relay bundle: invalid ref %q", backend, ref)
	}
	bundlePath := filepath.Join(outboxDir, seambundle.FileName)
	if _, err := os.Stat(bundlePath); err != nil {
		// An absent outbox directory collapses into this same case: a missing
		// dir also yields os.IsNotExist, and "no dir" means "nothing to relay"
		// just as "no bundle file" does -- both are the benign empty-range case.
		if os.IsNotExist(err) {
			return fmt.Errorf("%s: relay bundle: %w: %s", backend, forge.ErrBundleNotFound, bundlePath)
		}
		return fmt.Errorf("%s: relay bundle: %w", backend, err)
	}
	dir, err := os.MkdirTemp("", "spindrift-relay-*")
	if err != nil {
		return fmt.Errorf("%s: relay bundle: mkdtemp: %w", backend, err)
	}
	defer os.RemoveAll(dir)

	if err := clone(dir); err != nil {
		return err
	}

	gitIn := func(args ...string) *exec.Cmd {
		return exec.Command("git", append([]string{"-C", dir}, args...)...)
	}
	// Verified against dir, not the ambient cwd: the bundle's prerequisite
	// commit(s) -- everything on the base side of the Box's base..branch
	// range -- must be reachable from *some* repo for `git bundle verify` to
	// succeed, and dir (the clone the closure just made from origin) is the
	// one this function has in hand.
	if out, err := gitIn("bundle", "verify", bundlePath).CombinedOutput(); err != nil {
		return fmt.Errorf("%s: malformed bundle %s: %w: %s", backend, bundlePath, err, out)
	}
	// The forced refspec lets a retried fix-pass's rebuilt bundle overwrite
	// the branch this clone may already know about from the closure's own
	// initial clone/fetch.
	if out, err := gitIn("fetch", bundlePath, "+"+ref+":refs/heads/"+ref).CombinedOutput(); err != nil {
		return fmt.Errorf("%s: relay bundle: git fetch bundle: %w: %s", backend, err, out)
	}
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
