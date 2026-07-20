package local

import (
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/git"
)

// IntegrationBranch returns the per-broad-ticket Integration branch name
// Merge lands seams onto inside the Accumulation repo (ADR 0033), keyed on
// the local issue's parent field.
func IntegrationBranch(parent string) string {
	return "integration/" + parent
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
func NewLocalCodeForge(repoPath, baseBranch, parent, userName, userEmail, branchPrefix string, opts ...git.Option) forge.CodeForge {
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
	repoPath, baseBranch, parent string
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
