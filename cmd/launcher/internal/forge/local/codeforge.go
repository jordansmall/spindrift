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
	}
}

// localCodeForge wraps the git adapter's CodeForge with the host-side hooks
// CODE_FORGE=local needs around it (ADR 0033): relaying a Box's code-out
// bundle in (creating the Integration branch on first use), and resolving
// the landed Integration ref + commit sha after Merge. Every other method
// (AgentBranch, Rebase, Probe, BranchExists, and Merge itself) is the
// embedded git client's, unchanged.
type localCodeForge struct {
	forge.CodeForge
	repoPath, baseBranch string
	parent               SanitizedParent
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

// LandingRef resolves the Integration branch's current tip commit sha,
// returned alongside the branch name as "<integration-branch>@<sha>" — the
// immutable landing: reference ADR 0029/0033 expects once a merge has
// landed onto it.
func (l *localCodeForge) LandingRef() (string, error) {
	return landingRef(l.repoPath, IntegrationBranch(l.parent))
}

// VerifyLanding reports whether landing is merged into its own named
// Integration branch, with no network call (forge.LandingVerifier, ADR
// 0029, ADR 0033) — reconcile's sole closing authority calls this instead of
// a PRForge check when cf has no PR concept. landing is self-describing
// ("<branch>@<sha>", ADR 0033), so verification never depends on which
// parent this particular adapter instance was constructed with (issue
// #1734) — one shared instance correctly verifies every parent's seams in a
// mixed batch, not just its own. A landing that doesn't parse (the raw
// agent-branch name settle recorded before a merge was attempted) is
// reported unmerged rather than an error — the same "stays open" posture a
// genuine ancestry miss gets.
func (l *localCodeForge) VerifyLanding(landing string) (bool, error) {
	branch, sha, ok := parseLandingRef(landing)
	if !ok {
		return false, nil
	}
	return isMergedIntoIntegration(l.repoPath, sha, branch)
}

// BranchMergedIntoIntegration implements the optional forge.LandingRepair
// surface (ADR 0029, ADR 0033, issue #1809) — reconcile's healing-path check
// for a stuck LandingBranchRef: is branch, resolved against parent's own
// Integration branch (explicit, not l's own construction-time parent — see
// forge.LandingRepair), an ancestor of it. A branch the Accumulation repo has
// never seen (never relayed, or a since-abandoned attempt) reports
// merged=false, nil, the same "stays open" posture as a genuinely unmerged
// one.
func (l *localCodeForge) BranchMergedIntoIntegration(branch, parent string) (bool, error) {
	sha, ok, err := branchTipSHA(l.repoPath, branch)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	return isMergedIntoIntegration(l.repoPath, sha, IntegrationBranch(parent))
}

// IntegrationTip implements the optional forge.LandingRepair surface (ADR
// 0029, ADR 0033, issue #1809) — reconcile's healing-path resolution of
// parent's own Integration branch (explicit, not l's own construction-time
// parent) to its current landing-ready "<branch>@<sha>" reference, the value
// a confirmed BranchMergedIntoIntegration repair records.
func (l *localCodeForge) IntegrationTip(parent string) (string, error) {
	return landingRef(l.repoPath, IntegrationBranch(parent))
}
