package forgejo

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/gitplumbing"
	"spindrift.dev/launcher/internal/seambundle"
)

// forgejoRelayForcePushTimeout bounds RelayBundle's force-push subprocess,
// mirroring github's rebaseForcePushTimeout — a remote that accepts the
// connection and then hangs server-side must not block the caller forever.
const forgejoRelayForcePushTimeout = 5 * time.Minute

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
func NewReadOnlyForgejoCodeForge(cfg ForgejoCodeForgeConfig) forge.CodeForge {
	cf := NewForgejoCodeForge(cfg).(*forgejoCodeForge)
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
	// Defense in depth, matching github's own RelayBundle: settle derives ref
	// from cf.AgentBranch(num) host-side and never forwards untrusted input
	// here, so ref is launcher-controlled by the time it reaches this method.
	// It still interpolates directly into a refspec and a checkout argument,
	// so guard it the same way regardless of that guarantee holding upstream.
	if ref == "" || strings.HasPrefix(ref, "-") {
		return fmt.Errorf("forgejo: relay bundle: invalid ref %q", ref)
	}
	bundlePath := filepath.Join(outboxDir, seambundle.FileName)
	if _, err := os.Stat(bundlePath); err != nil {
		// An absent outbox directory collapses into this same case: a missing
		// dir also yields os.IsNotExist, and "no dir" means "nothing to relay"
		// just as "no bundle file" does -- both are the benign empty-range case.
		if os.IsNotExist(err) {
			return fmt.Errorf("forgejo: relay bundle: %w: %s", forge.ErrBundleNotFound, bundlePath)
		}
		return fmt.Errorf("forgejo: relay bundle: %w", err)
	}

	dir, err := os.MkdirTemp("", "spindrift-relay-*")
	if err != nil {
		return fmt.Errorf("forgejo: relay bundle: mkdtemp: %w", err)
	}
	defer os.RemoveAll(dir)

	// c.remote carries the token as userinfo; CombinedOutput is deliberately
	// discarded from the returned error (unlike the git-in-dir calls below)
	// since git's own clone diagnostics can echo the tokened URL back
	// verbatim on failure.
	if _, err := exec.Command("git", "clone", "--no-single-branch", c.remote, dir).CombinedOutput(); err != nil {
		return fmt.Errorf("forgejo: relay bundle: git clone %s: %w", forge.RedactURLCredentials(c.remote), err)
	}

	gitIn := func(args ...string) *exec.Cmd {
		return exec.Command("git", append([]string{"-C", dir}, args...)...)
	}

	// Verified against dir, not the ambient cwd: the bundle's prerequisite
	// commit(s) -- everything on the base side of the Box's base..branch
	// range -- must be reachable from *some* repo for `git bundle verify` to
	// succeed, and dir (the clone just made from origin) is the one this
	// method has in hand.
	if out, err := gitIn("bundle", "verify", bundlePath).CombinedOutput(); err != nil {
		return fmt.Errorf("forgejo: malformed bundle %s: %w: %s", bundlePath, err, out)
	}

	// The forced refspec lets a retried fix-pass's rebuilt bundle overwrite
	// the branch this clone may already know about from the initial clone.
	if out, err := gitIn("fetch", bundlePath, "+"+ref+":refs/heads/"+ref).CombinedOutput(); err != nil {
		return fmt.Errorf("forgejo: relay bundle: git fetch bundle: %w: %s", err, out)
	}
	if out, err := gitIn("checkout", ref).CombinedOutput(); err != nil {
		return fmt.Errorf("forgejo: relay bundle: git checkout %s: %w: %s", ref, err, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), forgejoRelayForcePushTimeout)
	defer cancel()
	// Unlike Rebase's already-tracked head branch, ref came from a bundle
	// fetch (refs/heads/ref created fresh in this clone), so it has no
	// upstream for a bare force-with-lease to target -- an explicit
	// destination is required, first push or retried force-update alike.
	return gitplumbing.GitForcePush(ctx, dir, "-u", "origin", ref)
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
func (c *readOnlyCodeForge) CreateDraftPR(title, body, base, head string) (string, error) {
	reqBody := map[string]any{
		"title": forgejoWIPPrefix + " " + title,
		"head":  head,
		"base":  base,
		"body":  body,
	}
	var payload forgejoPullPayload
	status, err := c.rest.do(http.MethodPost, c.rest.repoPath()+"/pulls", reqBody, &payload)
	if err != nil {
		return "", fmt.Errorf("forgejo: create draft PR: %w", err)
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("forgejo: create draft PR: unexpected status %d", status)
	}
	return payload.HTMLURL, nil
}

var _ forge.BundleRelay = (*readOnlyCodeForge)(nil)
var _ forge.DraftPRCreator = (*readOnlyCodeForge)(nil)
