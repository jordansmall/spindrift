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
// generic BundleRelay type-assertion (ready.go) -- depend on which
// constructor built it, not on a runtime mode check inside a single shared
// method set. NewForgejoCodeForge (BOX_FORGE_AND_ISSUE_ACCESS=read-write, the
// Box pushes in-box) must never satisfy forge.BundleRelay or
// forge.DraftPRCreator, or settle's generic relay-before-merge would try to
// relay a bundle that was never written and block every read-write forgejo
// land.
type readOnlyCodeForge struct {
	*forgejoCodeForge
}

// NewReadOnlyForgejoCodeForge returns the Forgejo adapter used under
// BOX_FORGE_AND_ISSUE_ACCESS=read-only: identical to NewForgejoCodeForge
// (same REST/git plumbing, same PRForge surface via embedding) plus
// RelayBundle and CreateDraftPR, the host-mediated hand-off for a Box that
// cannot push or open a PR directly (mirroring github's
// NewReadOnlyCodeForge, issue #1918/#1919).
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
// clone of the target repo and force-pushes it to origin with the launcher's
// own token-authenticated remote -- the forgejo counterpart of github's
// RelayBundle (github/relay.go). A missing or malformed bundle is returned
// as an error, never a silent no-op, so a broken hand-off blocks the seam
// instead of landing nothing. The two failure modes are distinguished: an
// absent bundle file returns forge.ErrBundleNotFound, the benign "Box wrote
// nothing" case, while a bundle that is present but unreadable or fails
// `git bundle verify` returns a generic error, since that's a genuine relay
// failure the caller should not treat as a no-op.
func (c *readOnlyCodeForge) RelayBundle(outboxDir, ref string) error {
	return bundlerelay.Relay("forgejo", outboxDir, ref, func(dir string) error {
		// c.remote carries the token as userinfo; CombinedOutput is deliberately
		// discarded from the returned error (unlike the git-in-dir calls inside
		// bundlerelay.Relay) since git's own clone diagnostics can echo the
		// tokened URL back verbatim on failure.
		if _, err := exec.Command("git", "clone", "--no-single-branch", c.remote, dir).CombinedOutput(); err != nil {
			return fmt.Errorf("forgejo: relay bundle: git clone %s: %w", forge.RedactURLCredentials(c.remote), err)
		}
		return nil
	})
}

// CreateDraftPR opens a draft PR from head onto base via Forgejo's REST pull
// create endpoint -- the host-side counterpart to a Box's own in-box PR
// creation under read-write, only reachable here because
// NewReadOnlyForgejoCodeForge wraps *forgejoCodeForge with it:
// NewForgejoCodeForge must never satisfy forge.DraftPRCreator, the same
// isolation RelayBundle has, or a read-write forgejo land would call an
// unneeded, possibly-conflicting host-side create. Forgejo encodes draft
// state as a title prefix rather than a first-class create-time field
// (forgejoWIPPrefix), the same convention MarkReady/MarkDraft read and
// write; MarkReady strips it before merge.
//
// Idempotent against a retried call for the same head (issue #2407 slice
// 2, mirroring github's CreateDraftPR, relay.go): a create that races or
// repeats an earlier host-mediated create for the same branch fails with
// 409 Conflict -- Forgejo's "a pull request for this head already exists"
// signal on this endpoint. That status is shared, via forgejoStatusMap,
// with the merge endpoint's own "not mergeable" refusal (errMergeRefused),
// so this checks the create call's own rest.StatusError rather than
// errors.Is against errMergeRefused, which would also match a 405 and would
// conflate two endpoints' unrelated meanings for the same status. On a
// precise 409, this resolves the branch's own open PR via OpenPRForBranch
// and returns that PR's URL with no error -- adoption, not failure.
// OpenPRForBranch includes drafts (issue #2408), so it reaches the PR a
// retried call collides with on 409, which is always a draft itself
// (CreateDraftPR always creates a draft, the forgejoWIPPrefix title above).
// If OpenPRForBranch can't resolve an open PR for that head (e.g. only a
// closed/merged PR exists, or the lookup itself errors), the original
// create error is returned unmasked -- adoption is only ever additive,
// never a way to swallow a genuine failure. Any other (non-409) failure is
// returned exactly as before.
func (c *readOnlyCodeForge) CreateDraftPR(title, body, base, head string) (string, error) {
	reqBody := map[string]any{
		"title": forgejoWIPPrefix + " " + title,
		"head":  head,
		"base":  base,
		"body":  body,
	}
	var payload forgejoPullPayload
	err := c.rest.Do(http.MethodPost, c.repoPath()+"/pulls", reqBody, &payload)
	if err == nil {
		return payload.HTMLURL, nil
	}
	createErr := fmt.Errorf("forgejo: create draft PR: %w", err)
	var statusErr rest.StatusError
	if errors.As(err, &statusErr) && statusErr.Status == http.StatusConflict {
		if pr, ok, openErr := c.OpenPRForBranch(head); openErr == nil && ok {
			return pr.URL, nil
		}
	}
	return "", createErr
}

var _ forge.BundleRelay = (*readOnlyCodeForge)(nil)
var _ forge.DraftPRCreator = (*readOnlyCodeForge)(nil)
