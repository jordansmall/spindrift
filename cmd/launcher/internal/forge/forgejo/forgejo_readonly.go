package forgejo

import (
	"errors"
	"fmt"
	"net/http"
	"os/exec"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/bundlerelay"
	"spindrift.dev/launcher/internal/forge/rest"
)

// readOnlyCodeForge adds forge.BundleRelay and forge.DraftPRCreator to
// *forgejoCodeForge, so settle's generic type assertion (ready.go) keys off
// which constructor built the adapter rather than a runtime mode check.
// NewForgejoCodeForge (read-write, the Box pushes in-box) must never satisfy
// them, or settle would relay a bundle nobody wrote and block every land.
type readOnlyCodeForge struct {
	*forgejoCodeForge
}

// NewReadOnlyForgejoCodeForge returns the Forgejo adapter for
// BOX_FORGE_AND_ISSUE_ACCESS=read-only: NewForgejoCodeForge plus RelayBundle
// and CreateDraftPR, the host-mediated hand-off for a Box that cannot push or
// open a PR itself (issues #1918, #1919).
func NewReadOnlyForgejoCodeForge(cfg ForgejoCodeForgeConfig, tracker forge.IssueTracker) forge.CodeForge {
	cf := NewForgejoCodeForge(cfg, tracker).(*forgejoCodeForge)
	return &readOnlyCodeForge{forgejoCodeForge: cf}
}

// NewReadOnlyForgejoCodeForgeForTest mirrors NewForgejoCodeForgeForTest for
// the read-only wrapper.
func NewReadOnlyForgejoCodeForgeForTest(cfg ForgejoCodeForgeConfig, tracker forge.IssueTracker, gitRemoteURL string) forge.CodeForge {
	cf := newForgejoCodeForge(cfg, tracker, gitRemoteURL)
	return &readOnlyCodeForge{forgejoCodeForge: cf}
}

// RelayBundle imports ref from outboxDir/seambundle.FileName into a fresh
// clone of the target repo and force-pushes it to origin. An absent bundle
// returns forge.ErrBundleNotFound, the benign "Box wrote nothing" case.
// A present bundle that is unreadable or fails verification returns a generic
// error, so a broken hand-off blocks the seam instead of landing nothing.
func (c *readOnlyCodeForge) RelayBundle(outboxDir, ref string) error {
	return bundlerelay.Relay("forgejo", outboxDir, ref, func(dir string) error {
		// c.remote carries the token as userinfo, so the clone's CombinedOutput
		// stays out of the error: git's diagnostics echo the tokened URL back.
		if _, err := exec.Command("git", "clone", "--no-single-branch", c.remote, dir).CombinedOutput(); err != nil {
			return fmt.Errorf("forgejo: relay bundle: git clone %s: %w", forge.RedactURLCredentials(c.remote), err)
		}
		return nil
	})
}

// CommitSubjects returns the one-line commit subjects the bundle at
// outboxDir/seambundle.FileName carries for ref, relative to base, oldest
// first, for settle's read-only PR-intent fallback (issue #2447). Unlike
// RelayBundle it never checks anything out or pushes, so it cannot mutate the
// remote.
func (c *readOnlyCodeForge) CommitSubjects(outboxDir, base, ref string) ([]string, error) {
	return bundlerelay.CommitSubjects("forgejo", outboxDir, base, ref, func(dir string) error {
		// c.remote carries the token as userinfo, so the clone's CombinedOutput
		// stays out of the error: git's diagnostics echo the tokened URL back.
		if _, err := exec.Command("git", "clone", "--no-single-branch", c.remote, dir).CombinedOutput(); err != nil {
			return fmt.Errorf("forgejo: relay bundle: git clone %s: %w", forge.RedactURLCredentials(c.remote), err)
		}
		return nil
	})
}

// CreateDraftPR opens a draft PR from head onto base. Forgejo has no
// create-time draft field and encodes the state as a title prefix
// (forgejoWIPPrefix), which MarkReady strips before merge. A retried create
// for the same head adopts that branch's open PR and reports created=false,
// so a caller (issues #2407, #2447) knows the title and body are not its own.
func (c *readOnlyCodeForge) CreateDraftPR(title, body, base, head string) (string, bool, error) {
	reqBody := map[string]any{
		"title": forgejoWIPPrefix + " " + title,
		"head":  head,
		"base":  base,
		"body":  body,
	}
	var payload forgejoPullPayload
	err := c.rest.Do(http.MethodPost, c.repoPath()+"/pulls", reqBody, &payload)
	if err == nil {
		return payload.HTMLURL, true, nil
	}
	createErr := fmt.Errorf("forgejo: create draft PR: %w", err)
	// A 409 means a PR for this head already exists. forgejoStatusMap maps that
	// status onto errMergeRefused too, so match the create call's own
	// StatusError rather than errors.Is, which would also match a 405.
	// OpenPRForBranch is draft-inclusive (issue #2408), and this PR is a draft.
	var statusErr rest.StatusError
	if errors.As(err, &statusErr) && statusErr.Status == http.StatusConflict {
		if pr, ok, openErr := c.OpenPRForBranch(head); openErr == nil && ok {
			return pr.URL, false, nil
		}
	}
	return "", false, createErr
}

var _ forge.BundleRelay = (*readOnlyCodeForge)(nil)
var _ forge.DraftPRCreator = (*readOnlyCodeForge)(nil)
var _ forge.BundleCommitSubjects = (*readOnlyCodeForge)(nil)
