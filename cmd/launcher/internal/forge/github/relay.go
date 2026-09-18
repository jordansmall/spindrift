package github

import (
	"bytes"
	"os/exec"
	"strings"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/bundlerelay"
)

// readOnlyCodeForge exists so that only the read-only constructor satisfies
// forge.BundleRelay, which settle type-asserts (ready.go). If NewExecClient
// satisfied it too, settle would relay a bundle the read-write Box never
// wrote and block every github land.
type readOnlyCodeForge struct {
	*execClient
}

// NewReadOnlyCodeForge returns the gh-exec adapter for
// BOX_FORGE_AND_ISSUE_ACCESS=read-only. It adds the host-mediated bundle
// hand-off to NewExecClient, which a Box that cannot push needs (issue #1918).
func NewReadOnlyCodeForge(repo string, labels forge.DispatchLabels, branchPrefix string, opts ...ExecOption) forge.CodeForge {
	return &readOnlyCodeForge{execClient: NewExecClient(repo, labels, branchPrefix, opts...)}
}

// RelayBundle imports ref from the bundle in outboxDir into a fresh clone and
// force-pushes it to origin with the launcher's own gh-cli credential. A
// missing bundle returns forge.ErrBundleNotFound, the benign "Box wrote
// nothing" case; a bundle that is present but unreadable or fails `git bundle
// verify` returns an error so a broken hand-off blocks the seam (issue #2096).
func (c *readOnlyCodeForge) RelayBundle(outboxDir, ref string) error {
	return bundlerelay.Relay("github", outboxDir, ref, func(dir string) error {
		if _, err := exec.Command("gh", "repo", "clone", c.repo, dir, "--", "--no-single-branch").Output(); err != nil {
			return ghCommandErr("github: relay bundle: gh repo clone", err)
		}
		return nil
	})
}

// CommitSubjects returns the bundle's one-line commit subjects for ref
// relative to base, oldest first, for settle's read-only PR-intent fallback
// (issue #2447). It never checks anything out or pushes, so it cannot mutate
// the remote.
func (c *readOnlyCodeForge) CommitSubjects(outboxDir, base, ref string) ([]string, error) {
	return bundlerelay.CommitSubjects("github", outboxDir, base, ref, func(dir string) error {
		if _, err := exec.Command("gh", "repo", "clone", c.repo, dir, "--", "--no-single-branch").Output(); err != nil {
			return ghCommandErr("github: commit subjects: gh repo clone", err)
		}
		return nil
	})
}

var _ forge.BundleCommitSubjects = (*readOnlyCodeForge)(nil)
var _ forge.BundleRelay = (*readOnlyCodeForge)(nil)
var _ forge.DraftPRCreator = (*readOnlyCodeForge)(nil)

// CreateDraftPR opens a draft PR from head onto base for a read-only Box that
// cannot run `gh pr create` itself (issue #1919). head and base are branch
// names in c.repo, never a fork's owner:branch form. Only readOnlyCodeForge
// satisfies forge.DraftPRCreator, so a read-write land never makes a
// host-side create that conflicts with the Box's own.
func (c *readOnlyCodeForge) CreateDraftPR(title, body, base, head string) (string, bool, error) {
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
		createErr := ghCommandErrText("github: create draft PR: gh pr create", err, stderr.String())
		// A retried or raced host-side create (issue #2407) fails with gh's
		// "already exists" stderr, not a sentinel error. Adopt the branch's
		// open PR rather than report a settled hand-off as blocked, returning
		// created=false so settle's reconstructed-PR path (issue #2447) knows
		// the title and body are not the ones supplied here.
		if strings.Contains(stderr.String(), "already exists") {
			if pr, ok, openErr := c.OpenPRForBranch(head); openErr == nil && ok {
				return pr.URL, false, nil
			}
		}
		return "", false, createErr
	}
	return strings.TrimSpace(string(out)), true, nil
}
