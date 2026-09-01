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

// readOnlyCodeForge wraps *forgejoCodeForge with forge.BundleRelay and
// forge.DraftPRCreator, so the interfaces it satisfies -- and thus settle's
// generic BundleRelay type-assertion -- depend on which constructor built it
// rather than a runtime mode check. NewForgejoCodeForge (read-write, the Box
// pushes in-box) must never satisfy either interface, or settle's generic
// relay-before-merge would try to relay a bundle that was never written and
// block every read-write forgejo land.
type readOnlyCodeForge struct {
	*forgejoCodeForge
}

// NewReadOnlyForgejoCodeForge returns the Forgejo adapter used under
// BOX_FORGE_AND_ISSUE_ACCESS=read-only: NewForgejoCodeForge plus RelayBundle
// and CreateDraftPR, the host-mediated hand-off for a Box that cannot push or
// open a PR directly.
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

// RelayBundle imports ref from outboxDir/seambundle.FileName into a fresh clone
// of the target repo and force-pushes it to origin with the launcher's own
// token-authenticated remote. A missing or malformed bundle is an error, never a
// silent no-op, so a broken hand-off blocks the seam instead of landing nothing:
// an absent bundle file returns forge.ErrBundleNotFound (the benign "Box wrote
// nothing" case), while a present-but-unreadable or verify-failing bundle
// returns a generic error.
func (c *readOnlyCodeForge) RelayBundle(outboxDir, ref string) error {
	return bundlerelay.Relay("forgejo", outboxDir, ref, func(dir string) error {
		// c.remote carries the token as userinfo, and git's clone diagnostics can
		// echo the tokened URL back verbatim, so CombinedOutput is deliberately
		// dropped from the returned error.
		if _, err := exec.Command("git", "clone", "--no-single-branch", c.remote, dir).CombinedOutput(); err != nil {
			return fmt.Errorf("forgejo: relay bundle: git clone %s: %w", forge.RedactURLCredentials(c.remote), err)
		}
		return nil
	})
}

// CommitSubjects returns the one-line commit subjects the bundle at
// outboxDir/seambundle.FileName carries for ref, relative to base, oldest first
// -- settle's read-only PR-intent fallback. Unlike RelayBundle it never checks
// anything out or pushes, so it cannot mutate the remote.
func (c *readOnlyCodeForge) CommitSubjects(outboxDir, base, ref string) ([]string, error) {
	return bundlerelay.CommitSubjects("forgejo", outboxDir, base, ref, func(dir string) error {
		// See RelayBundle: the tokened remote must not reach the returned error.
		if _, err := exec.Command("git", "clone", "--no-single-branch", c.remote, dir).CombinedOutput(); err != nil {
			return fmt.Errorf("forgejo: relay bundle: git clone %s: %w", forge.RedactURLCredentials(c.remote), err)
		}
		return nil
	})
}

// CreateDraftPR opens a draft PR from head onto base via Forgejo's REST pull
// create endpoint. Forgejo encodes draft state as a title prefix
// (forgejoWIPPrefix) rather than a create-time field, the same convention
// MarkReady/MarkDraft read and write.
//
// It is idempotent against a retried call for the same head: a repeated
// host-mediated create fails with 409 Conflict, and this then adopts the
// branch's existing open PR via OpenPRForBranch and returns its URL with
// created=false, so a caller (settle's reconstructed-PR path) knows the PR's
// title/body are not the ones just supplied. The 409 check reads the create
// call's own rest.StatusError rather than errors.Is against errMergeRefused,
// which shares forgejoStatusMap's mapping and would also match a 405 from the
// unrelated merge endpoint. If no open PR can be resolved for that head, the
// original create error is returned unmasked -- adoption is additive, never a
// way to swallow a genuine failure.
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
