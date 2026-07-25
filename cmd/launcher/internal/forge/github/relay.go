package github

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/gitplumbing"
	"spindrift.dev/launcher/internal/seambundle"
)

// readOnlyCodeForge wraps execClient with forge.BundleRelay, so that the
// interface it satisfies -- and thus settle's generic BundleRelay
// type-assertion (ready.go) -- depends on which constructor built it, not on
// a runtime mode check inside a single shared method set. NewExecClient
// (BOX_FORGE_AND_ISSUE_ACCESS=read-write, the Box pushes in-box) must never
// satisfy forge.BundleRelay, or settle would try to relay a bundle that was
// never written and block every read-write github land.
type readOnlyCodeForge struct {
	*execClient
}

// NewReadOnlyCodeForge returns the gh-exec adapter used under
// BOX_FORGE_AND_ISSUE_ACCESS=read-only: identical to NewExecClient (same
// repo/labels/branchPrefix, same PRForge surface via embedding) plus
// RelayBundle, the host-mediated hand-off for a Box that cannot push
// directly (issue #1918).
func NewReadOnlyCodeForge(repo string, labels forge.DispatchLabels, branchPrefix string, verdictLabels ...forge.VerdictLabels) forge.CodeForge {
	return &readOnlyCodeForge{execClient: NewExecClient(repo, labels, branchPrefix, verdictLabels...)}
}

// RelayBundle imports ref from outboxDir/seambundle.FileName into a fresh
// clone of the target repo and force-pushes it to origin with the
// launcher's own gh-cli credential -- the github counterpart of local's
// RelayBundle (forge/local/bundle.go), which only ever imports into its own
// bare backing repo since there is no remote to push to. A missing or
// malformed bundle is returned as an error, never a silent no-op, so a
// broken hand-off blocks the seam instead of landing nothing (mirroring
// local's own bundle-relay failure posture).
func (c *readOnlyCodeForge) RelayBundle(outboxDir, ref string) error {
	// Defense in depth, matching local's own relayBundle: settle derives ref
	// from cf.AgentBranch(num) host-side (issue #1949) and never forwards the
	// outcome line's own landing= field here, so ref is launcher-controlled
	// by the time it reaches this method. It still interpolates directly into
	// a refspec and a checkout argument, so guard it the same way regardless
	// of that guarantee holding upstream.
	if ref == "" || strings.HasPrefix(ref, "-") {
		return fmt.Errorf("github: relay bundle: invalid ref %q", ref)
	}
	bundlePath := filepath.Join(outboxDir, seambundle.FileName)
	if _, err := os.Stat(bundlePath); err != nil {
		return fmt.Errorf("github: relay bundle: %w", err)
	}

	dir, err := os.MkdirTemp("", "spindrift-relay-*")
	if err != nil {
		return fmt.Errorf("github: relay bundle: mkdtemp: %w", err)
	}
	defer os.RemoveAll(dir)

	if out, err := exec.Command("gh", "repo", "clone", c.repo, dir, "--", "--no-single-branch").CombinedOutput(); err != nil {
		return fmt.Errorf("github: relay bundle: gh repo clone: %w: %s", err, out)
	}

	gitIn := func(args ...string) *exec.Cmd {
		return exec.Command("git", append([]string{"-C", dir}, args...)...)
	}

	// Verified against dir, not the ambient cwd (unlike local's relayBundle,
	// which verifies against its own bare backing repo directly): the
	// bundle's prerequisite commit(s) -- everything on the base side of the
	// Box's base..branch range -- must be reachable from *some* repo for
	// `git bundle verify` to succeed, and dir (the clone just made from
	// origin) is the one this method has in hand.
	if out, err := gitIn("bundle", "verify", bundlePath).CombinedOutput(); err != nil {
		return fmt.Errorf("github: malformed bundle %s: %w: %s", bundlePath, err, out)
	}

	// The forced refspec (matching local's own relayBundle) lets a retried
	// fix-pass's rebuilt bundle overwrite the branch this clone may already
	// know about from `gh repo clone`'s own initial fetch.
	if out, err := gitIn("fetch", bundlePath, "+"+ref+":refs/heads/"+ref).CombinedOutput(); err != nil {
		return fmt.Errorf("github: relay bundle: git fetch bundle: %w: %s", err, out)
	}
	if out, err := gitIn("checkout", ref).CombinedOutput(); err != nil {
		return fmt.Errorf("github: relay bundle: git checkout %s: %w: %s", ref, err, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), rebaseForcePushTimeout)
	defer cancel()
	// Unlike Rebase's already-tracked head branch, ref came from a bundle
	// fetch (refs/heads/ref created fresh in this clone), so it has no
	// upstream for a bare force-with-lease to target -- an explicit
	// destination is required, first push or retried force-update alike.
	return gitplumbing.GitForcePush(ctx, dir, "-u", "origin", ref)
}

// CreateDraftPR opens a draft PR from head onto base via `gh pr create` --
// the host-side counterpart to the Box's own in-box `gh pr create` under
// read-write (issue #1919), only reachable here because
// NewReadOnlyCodeForge wraps execClient with it: NewExecClient must never
// satisfy forge.DraftPRCreator, the same isolation RelayBundle has, or a
// read-write github land would call an unneeded, possibly-conflicting
// host-side create. Runs cwd-independently (no local clone required): head
// and base are branch names in c.repo itself, never a fork's owner:branch
// form, matching every agent PR branch's own in-repo convention.
func (c *readOnlyCodeForge) CreateDraftPR(title, body, base, head string) (string, error) {
	var stderr bytes.Buffer
	cmd := exec.Command("gh", "pr", "create",
		"--repo", c.repo,
		"--draft",
		"--base", base,
		"--head", head,
		"--title", title,
		"--body", body,
	)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("github: create draft PR: gh pr create: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}
