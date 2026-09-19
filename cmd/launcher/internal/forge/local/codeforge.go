package local

import (
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/git"
)

// IntegrationBranch returns the Integration branch Merge lands a broad
// ticket's seams onto inside the Accumulation repo (ADR 0033).
func IntegrationBranch(parent SanitizedParent) string {
	return "integration/" + parent.String()
}

// NewLocalCodeForge returns a forge.CodeForge that lands a seam's branch onto
// IntegrationBranch(parent) in the bare Accumulation repo at repoPath (ADR
// 0033), delegating to the git adapter with repoPath where a remote URL would
// go. baseBranch is the seed branch RelayBundle creates integration/<parent>
// from on the parent's first seam.
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

type localCodeForge struct {
	forge.CodeForge
	repoPath, baseBranch string
	parent               SanitizedParent
	userName, userEmail  string
}

// RelayBundle imports ref from the bundle the Box left in outboxDir into the
// Accumulation repo. The Box's repo mount is read-only, so the branch was
// never pushed and Merge's fetch from repoPath would miss it. It also creates
// IntegrationBranch from baseBranch's tip when this is the parent's first seam
// to land, since Merge assumes its base branch already exists.
func (l *localCodeForge) RelayBundle(outboxDir, ref string) error {
	if err := relayBundle(l.repoPath, outboxDir, ref); err != nil {
		return err
	}
	return ensureIntegrationBranch(l.repoPath, l.baseBranch, IntegrationBranch(l.parent))
}

var _ forge.BundleRelay = (*localCodeForge)(nil)

// Merge rebases branch onto the Integration branch's tip and fast-forwards it
// there instead of the embedded git client's `git merge --no-ff`, because the
// Integration branch must stay linear (ADR 0033, issue #1889). A rebase that
// stops on a conflict returns forge.ErrMergeConflict and leaves the
// Integration branch untouched.
func (l *localCodeForge) Merge(branch string) error {
	return rebaseLand(l.repoPath, branch, IntegrationBranch(l.parent), l.userName, l.userEmail)
}

// LandingRef returns the Integration branch's tip as "<branch>@<sha>", the
// immutable landing reference ADR 0029/0033 expects once a merge has landed.
func (l *localCodeForge) LandingRef() (string, error) {
	return landingRef(l.repoPath, IntegrationBranch(l.parent))
}

// IntegrationTip resolves the given parent's Integration branch, not l's own
// construction-time parent, to its "<branch>@<sha>" reference for reconcile's
// healing path (forge.LandingRepair; ADR 0029, ADR 0033, issue #1809).
func (l *localCodeForge) IntegrationTip(parent string) (string, error) {
	return landingRef(l.repoPath, IntegrationBranch(SanitizedParent{token: parent}))
}

// LandingContained reports without network access whether landing sits in
// scope's Integration branch, not l's construction-time parent (issue #2129,
// issue #1734, ADR 0033, issue #2151). Ancestry alone misses a rebase-based
// land (issue #1889), which replays commits under new shas, so it falls back
// to patch-equivalence. An unresolvable landing reports contained=false, nil.
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

// landingSHA resolves landing's own commit sha. A missing branch, and any
// shape carrying no commit such as a PR URL reaching this local-only path,
// report ok=false with a nil error rather than failing the caller.
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
