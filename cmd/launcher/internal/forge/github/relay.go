package github

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/bundlerelay"
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
// repo/labels/branchPrefix, same PRForge surface via embedding, same opts)
// plus RelayBundle, the host-mediated hand-off for a Box that cannot push
// directly (issue #1918).
func NewReadOnlyCodeForge(repo string, labels forge.DispatchLabels, branchPrefix string, opts ...ExecOption) forge.CodeForge {
	return &readOnlyCodeForge{execClient: NewExecClient(repo, labels, branchPrefix, opts...)}
}

// RelayBundle imports ref from outboxDir/seambundle.FileName into a fresh
// clone of the target repo and force-pushes it to origin with the
// launcher's own gh-cli credential -- the github counterpart of local's
// RelayBundle (forge/local/bundle.go), which only ever imports into its own
// bare backing repo since there is no remote to push to. A missing or
// malformed bundle is returned as an error, never a silent no-op, so a
// broken hand-off blocks the seam instead of landing nothing (mirroring
// local's own bundle-relay failure posture). The two failure modes are
// distinguished (issue #2096): an absent bundle file returns
// forge.ErrBundleNotFound, the benign "Box wrote nothing" case, while a
// bundle that is present but unreadable or fails `git bundle verify` returns
// a generic error, since that's a genuine relay failure the caller should
// not treat as a no-op.
func (c *readOnlyCodeForge) RelayBundle(outboxDir, ref string) error {
	return bundlerelay.Relay("github", outboxDir, ref, func(dir string) error {
		if out, err := exec.Command("gh", "repo", "clone", c.repo, dir, "--", "--no-single-branch").CombinedOutput(); err != nil {
			return fmt.Errorf("github: relay bundle: gh repo clone: %w: %s", err, out)
		}
		return nil
	})
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
//
// Idempotent against a retried call for the same head (issue #2407 slice
// 1): a create that races or repeats an earlier host-mediated create for
// the same branch fails with gh's "a pull request ... already exists"
// stderr, not a distinct sentinel error. Rather than surface that as a
// failure -- which would wrongly report a settled hand-off as blocked --
// this treats it as a signal to adopt: it resolves the branch's own open
// PR via OpenPRForBranch (embedded from execClient) and returns that PR's
// URL with no error. If OpenPRForBranch can't resolve an open PR for that
// head (e.g. only a closed/merged PR exists, or the lookup itself errors),
// the original create error is returned unmasked -- adoption is only ever
// additive, never a way to swallow a genuine failure.
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
		createErr := fmt.Errorf("github: create draft PR: gh pr create: %w: %s", err, strings.TrimSpace(stderr.String()))
		if strings.Contains(stderr.String(), "already exists") {
			if pr, ok, openErr := c.OpenPRForBranch(head); openErr == nil && ok {
				return pr.URL, nil
			}
		}
		return "", createErr
	}
	return strings.TrimSpace(string(out)), nil
}
