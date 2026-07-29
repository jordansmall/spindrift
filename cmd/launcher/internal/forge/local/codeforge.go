package local

import (
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/git"
)

// IntegrationBranch returns the per-broad-ticket Integration branch name
// Merge lands seams onto inside the Accumulation repo (ADR 0033), keyed on
// the local issue's parent field.
func IntegrationBranch(parent SanitizedParent) string {
	return "integration/" + parent.String()
}

// NewLocalCodeForge returns a forge.CodeForge that lands a seam's branch
// host-side onto IntegrationBranch(parent) inside the bare Accumulation repo
// at repoPath (ADR 0033). baseBranch is the operator's real base branch —
// what SeedAccumulationRepo seeds the repo with — distinct from
// IntegrationBranch(parent): the very first seam of a broad ticket lands
// before any integration/<parent> ref exists, so RelayBundle creates it from
// baseBranch's tip on demand (see ensureIntegrationBranch). It reuses the git
// adapter's substrate wholesale — branch naming and the temp-clone -> merge
// -> push-ref landing helper — by delegating to it with the Accumulation
// repo path standing in for a remote URL (git clones and pushes to a local
// bare repo the same way it does a remote one) and IntegrationBranch(parent)
// standing in for the base branch. Like git, it implements forge.CodeForge
// only, never forge.PRForge — the Accumulation repo has no PR or CI concept.
// It additionally implements the optional forge.BundleRelay and
// forge.LandingRef hooks (neither git nor github do): the Box's read-only
// repo mount means Merge's usual "the branch is already pushed" assumption
// doesn't hold here, so the branch must be relayed in from the Box's
// code-out bundle first.
func NewLocalCodeForge(repoPath, baseBranch string, parent SanitizedParent, userName, userEmail, branchPrefix string, opts ...git.Option) forge.CodeForge {
	return &localCodeForge{
		CodeForge:  git.NewGitClient(repoPath, IntegrationBranch(parent), userName, userEmail, branchPrefix, opts...),
		repoPath:   repoPath,
		baseBranch: baseBranch,
		parent:     parent,
		userName:   userName,
		userEmail:  userEmail,
	}
}

// localCodeForge wraps the git adapter's CodeForge with the host-side hooks
// CODE_FORGE=local needs around it (ADR 0033): relaying a Box's code-out
// bundle in (creating the Integration branch on first use), resolving the
// landed Integration ref + commit sha after Merge, and landing itself (see
// Merge below). Every other method (AgentBranch, Rebase, Probe,
// BranchExists) is the embedded git client's, unchanged.
type localCodeForge struct {
	forge.CodeForge
	repoPath, baseBranch string
	parent               SanitizedParent
	userName, userEmail  string
}

// RelayBundle imports ref from the bundle the Box left in outboxDir into the
// Accumulation repo, so the embedded git client's Merge(ref) — which fetches
// ref from repoPath itself — finds it. It also ensures IntegrationBranch
// exists, creating it from baseBranch's tip when this is the parent's first
// seam to land — Merge assumes its base branch already exists (true for
// git/github's real remotes), which integration/<parent> only is once some
// earlier seam created it.
func (l *localCodeForge) RelayBundle(outboxDir, ref string) error {
	if err := relayBundle(l.repoPath, outboxDir, ref); err != nil {
		return err
	}
	return ensureIntegrationBranch(l.repoPath, l.baseBranch, IntegrationBranch(l.parent))
}

// Merge overrides the embedded git client's `git merge --no-ff` landing
// (ADR 0033, issue #1889): the Integration branch must stay linear with zero
// merge commits, unconditionally, unlike the remote git/github forges' own
// --no-ff behavior, which this leaves untouched. Landing rebases branch onto
// the Integration branch's current tip and fast-forwards it there instead of
// merging FETCH_HEAD in. A rebase that cannot complete automatically returns
// forge.ErrMergeConflict and leaves the Integration branch untouched — the
// same "stays unlanded and blocked" posture ADR 0033 gives a conflicting
// merge, reached here via a conflicting rebase instead.
func (l *localCodeForge) Merge(branch string) error {
	return rebaseLand(l.repoPath, branch, IntegrationBranch(l.parent), l.userName, l.userEmail)
}

// LandingRef resolves the Integration branch's current tip commit sha,
// returned alongside the branch name as "<integration-branch>@<sha>" — the
// immutable landing: reference ADR 0029/0033 expects once a merge has
// landed onto it.
func (l *localCodeForge) LandingRef() (string, error) {
	return landingRef(l.repoPath, IntegrationBranch(l.parent))
}

// IntegrationTip implements the optional forge.LandingRepair surface (ADR
// 0029, ADR 0033, issue #1809) — reconcile's healing-path resolution of
// parent's own Integration branch (explicit, not l's own construction-time
// parent) to its current landing-ready "<branch>@<sha>" reference, the value
// a confirmed LandingContained repair records.
func (l *localCodeForge) IntegrationTip(parent string) (string, error) {
	return landingRef(l.repoPath, IntegrationBranch(SanitizedParent{token: parent}))
}

// LandingContained implements the optional forge.LandingContainmentQuery
// surface (issue #2129, issue #1734, ADR 0033, issue #2151) — the single
// no-network containment check replacing the three narrower single-purpose
// methods this issue collapsed into it: a no-scope self-verification check,
// a bookkeeping-repair ancestry check that used to live on LandingRepair,
// and this method's own prior narrower single-shape query. It resolves
// landing's own commit sha via landingSHA, then checks it against scope's
// own Integration branch
// (explicit, not l's own construction-time parent), via ancestry first and —
// mirroring the pre-collapse rebase-aware fallback — patch-equivalence
// second, since a rebase-based land (issue #1889) replays commits under new
// shas an ancestry check alone can't see. A landing landingSHA can't resolve
// reports contained=false, nil, the same "not ready" posture a genuine
// containment miss gets.
func (l *localCodeForge) LandingContained(landing forge.Landing, scope forge.SeedScope) (bool, error) {
	sha, ok, err := landingSHA(l.repoPath, landing)
	if err != nil || !ok {
		return false, err
	}
	integrationBranch := IntegrationBranch(SanitizedParent{token: scope.Parent()})
	contained, err := isMergedIntoIntegration(l.repoPath, sha, integrationBranch)
	if err != nil || contained {
		return contained, err
	}
	return patchEquivalentToIntegration(l.repoPath, sha, integrationBranch)
}

// landingSHA resolves landing's own commit sha, the value LandingContained
// checks against scope's Integration branch: a LandingIntegrationRef (the
// rich post-merge form) already carries it directly; a LandingBranchRef (the
// raw, pre-merge record settle's outcome line wrote before any post-merge
// upgrade) resolves it by looking up branch's current tip inside repoPath,
// reporting ok=false with a nil error when the branch is absent (never
// relayed, or a since-abandoned attempt); any other shape (e.g. a PR URL
// reaching this local-only path) reports ok=false, nil outright — there is
// no commit to resolve.
func landingSHA(repoPath string, landing forge.Landing) (sha string, ok bool, err error) {
	switch landing.Kind {
	case forge.LandingIntegrationRef:
		return landing.SHA, true, nil
	case forge.LandingBranchRef:
		return branchTipSHA(repoPath, landing.Branch)
	default:
		return "", false, nil
	}
}
