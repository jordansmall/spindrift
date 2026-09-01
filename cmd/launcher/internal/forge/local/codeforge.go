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
// distinct from IntegrationBranch(parent), which the first seam of a broad
// ticket creates on demand from baseBranch's tip (see
// ensureIntegrationBranch). It delegates to the git adapter with the
// Accumulation repo path standing in for a remote URL and
// IntegrationBranch(parent) for the base branch. It implements
// forge.CodeForge only, never forge.PRForge — the Accumulation repo has no
// PR or CI concept — plus the optional forge.BundleRelay and
// forge.LandingRef hooks, which git and github do not need: the Box's
// read-only repo mount breaks Merge's usual "the branch is already pushed"
// assumption, so the branch must be relayed in from the code-out bundle.
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
// CODE_FORGE=local needs (ADR 0033): bundle relay, landing-ref resolution,
// and landing itself. Every other method is the embedded git client's.
type localCodeForge struct {
	forge.CodeForge
	repoPath, baseBranch string
	parent               SanitizedParent
	userName, userEmail  string
}

// RelayBundle imports ref from the bundle the Box left in outboxDir into the
// Accumulation repo, so the embedded git client's Merge(ref) finds it. It
// also creates IntegrationBranch from baseBranch's tip on the parent's first
// seam: Merge assumes its base branch already exists, which is true of
// git/github's real remotes but not of integration/<parent>.
func (l *localCodeForge) RelayBundle(outboxDir, ref string) error {
	if err := relayBundle(l.repoPath, outboxDir, ref); err != nil {
		return err
	}
	return ensureIntegrationBranch(l.repoPath, l.baseBranch, IntegrationBranch(l.parent))
}

var _ forge.BundleRelay = (*localCodeForge)(nil)

// Merge overrides the embedded git client's `git merge --no-ff` landing
// (ADR 0033): the Integration branch must stay linear with zero merge
// commits, unconditionally, so landing rebases branch onto the Integration
// tip and fast-forwards instead. A rebase that cannot complete automatically
// returns forge.ErrMergeConflict and leaves the Integration branch
// untouched — ADR 0033's "stays unlanded and blocked" posture.
func (l *localCodeForge) Merge(branch string) error {
	return rebaseLand(l.repoPath, branch, IntegrationBranch(l.parent), l.userName, l.userEmail)
}

// LandingRef resolves the Integration branch's current tip as
// "<integration-branch>@<sha>" — the immutable landing: reference ADR
// 0029/0033 expects once a merge has landed onto it.
func (l *localCodeForge) LandingRef() (string, error) {
	return landingRef(l.repoPath, IntegrationBranch(l.parent))
}

// IntegrationTip implements the optional forge.LandingRepair surface (ADR
// 0029, ADR 0033): reconcile's healing path resolves parent's Integration
// branch — explicit, not l's construction-time parent — to the
// "<branch>@<sha>" reference a confirmed LandingContained repair records.
func (l *localCodeForge) IntegrationTip(parent string) (string, error) {
	return landingRef(l.repoPath, IntegrationBranch(SanitizedParent{token: parent}))
}

// LandingContained implements the optional forge.LandingContainmentQuery
// surface (ADR 0033) — a no-network containment check. It resolves landing's
// commit sha, then checks it against scope's Integration branch (explicit,
// not l's construction-time parent) by ancestry first and patch-equivalence
// second, since a rebase-based land replays commits under new shas that an
// ancestry check alone can't see. A landing whose sha can't be resolved
// reports contained=false, nil — the same "not ready" posture a genuine
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

// landingSHA resolves landing's own commit sha. A LandingIntegrationRef
// carries it directly; a LandingBranchRef (the pre-merge record settle's
// outcome line wrote) resolves branch's current tip in repoPath, reporting
// ok=false with a nil error when the branch is absent. Any other shape
// reports ok=false, nil — there is no commit to resolve.
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
