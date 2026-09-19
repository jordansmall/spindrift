package forge

// CodeForge is the seam every adapter implements: agent branch naming, rebase,
// merge/landing under MERGE_MODE, and connectivity probe.
type CodeForge interface {
	// AgentBranch returns the agent branch name for issue num. The prefix is
	// baked in at construction; callers never concatenate it themselves.
	AgentBranch(num string) string
	// BranchExists reports whether branch exists on the remote, independent of
	// any PR, the signal a bare `git push` with no PR opened yet still leaves
	// behind. Reconcile's gated orphan reset (#1432, #600) needs it.
	BranchExists(branch string) (bool, error)
	// Merge lands ref onto the target branch: a rebase merge of the PR (github)
	// or a plain merge-and-push of the branch name (git, MERGE_MODE=immediate).
	Merge(ref string) error
	// Rebase rebases ref onto its base and force-pushes: the PR's head branch
	// (github) or the branch name itself (git).
	Rebase(ref string) error
	// Probe checks code forge connectivity and returns the resolved repo slug.
	Probe() (string, error)
}

// BundleRelay is CODE_FORGE=local's optional pre-merge landing hook (ADR 0033):
// the Box cannot push to its read-only Accumulation-repo mount, so it leaves
// its branch as a git bundle in the writable outbox, which must be relayed in
// before Merge(ref) can find that branch. Only the local adapter implements it.
type BundleRelay interface {
	// RelayBundle imports ref from the bundle file the Box left in outboxDir
	// into the Code Forge's backing repo. An error leaves the seam unlanded.
	RelayBundle(outboxDir, ref string) error
}

// LandingRef is CODE_FORGE=local's optional post-merge landing-reference
// resolver (ADR 0029, ADR 0033): it resolves the immutable Integration ref and
// commit sha the landing: field records. It takes no ref argument because that
// value belongs to the adapter's own fixed Integration branch, baked in at
// construction, not to whichever branch was merged.
type LandingRef interface {
	// LandingRef resolves the landing reference, once a merge has landed.
	LandingRef() (string, error)
}

// LandingRepair is CODE_FORGE=local's optional bookkeeping-repair interface (ADR
// 0029, ADR 0033, issue #1809): it heals a seam whose merge landed but whose
// post-merge LandingRef call never ran, leaving a stale LandingBranchRef
// recorded. Reconcile's sweep holds one Code Forge instance, constructed with
// an empty parent, across a mixed-parent batch, so IntegrationTip takes one.
type LandingRepair interface {
	// IntegrationTip resolves parent's own Integration branch to its current
	// landing-ready "<branch>@<sha>" reference, the same grammar LandingRef
	// produces for the fresh-merge path.
	IntegrationTip(parent string) (string, error)
}

// LandingContainmentQuery is CODE_FORGE=local's single no-network
// merge-observation seam (issues #2129, #1734, #2151, ADR 0033), used by both
// reconcile's closing authority and the wave engine's dependent blocker gate.
// scope's parent is an explicit argument, not the adapter's construction-time
// one, so one shared instance answers for any parent in a mixed pass.
type LandingContainmentQuery interface {
	// LandingContained reports whether landing's commit already sits in scope's
	// own Integration branch, by git ancestry or by patch-equivalence, since a
	// rebase-based land (issue #1889) replays commits under new shas. A commit
	// this cannot resolve, or that has not reached scope, reports (false, nil);
	// an error is reserved for a genuine local-git failure.
	LandingContained(landing Landing, scope SeedScope) (contained bool, err error)
}

// PRForge is the optional PR, CI-rollup, and auto-merge interface. Only adapters
// that open pull requests and watch CI implement it (github); the push-only git
// adapter does not. Callers discover it with a type assertion.
type PRForge interface {
	// OpenPRForBranch returns the open PR for branch, if any, draft or not
	// (issue #2408): a stranded draft is exactly as adoptable as a ready PR.
	OpenPRForBranch(branch string) (PR, bool, error)
	// PRForBranch returns the URL of any PR (any state) for branch, if any.
	PRForBranch(branch string) (string, bool, error)
	// PRState returns the canonical state of the given PR URL.
	PRState(url string) (PRState, error)
	// Mergeable reports whether the PR's changes conflict with its base branch,
	// as distinct from CI checks or branch-protection gating.
	Mergeable(url string) (MergeableState, error)
	// CheckState returns the aggregate CI rollup state for the PR's head commit.
	CheckState(url string) (RollupState, error)
	// HeadCommitSHA returns the PR's current head commit SHA. selfHealGate
	// compares it across a fix pass to tell a genuine push, which restarts CI,
	// from a no-op pass whose stale terminal rollup would read as a fresh red
	// (issue #1980).
	HeadCommitSHA(url string) (string, error)
	// NeedsUpdate reports whether the PR's base branch has commits its head
	// branch has not yet incorporated, a git-ancestry fact distinct from
	// Mergeable's conflict check: a PR can need updating and still be
	// MERGEABLE. That gap let #670 and #672 land a combined compile break on
	// main even though each was individually green (issue #936).
	NeedsUpdate(url string) (bool, error)
	// FailureDetail returns the failed check names plus a bounded log excerpt
	// for the PR's head commit, or "" when nothing is currently failing.
	// Best-effort: a non-nil error means detail unavailable, and callers must
	// proceed without it rather than failing their own operation.
	FailureDetail(url string) (string, error)
	// ListPRFiles returns every path changed by the PR (added, modified, deleted).
	ListPRFiles(url string) ([]string, error)
	// CanAutoMerge reports whether the repository allows GitHub's native auto-merge.
	CanAutoMerge() (bool, error)
	// EnqueueAutoMerge enqueues native auto-merge for the PR.
	EnqueueAutoMerge(prURL string) error
	// MarkReady flips the PR out of draft. Marking an already-ready PR succeeds
	// rather than reporting a failure.
	MarkReady(prURL string) error
	// MarkDraft flips the PR back to draft. Marking an already-draft PR
	// succeeds rather than reporting a failure.
	MarkDraft(prURL string) error
}

// DraftPRCreator is the optional host-side draft-PR creation interface (issue
// #1914): under BOX_FORGE_AND_ISSUE_ACCESS=read-only the Box holds no write
// token, so the Launcher opens the draft PR instead, from a title, body, base,
// and head the Box supplies. Only meaningful for a forge that also implements
// PRForge. No adapter implements it yet; issue #1916's startup gate names it.
type DraftPRCreator interface {
	// CreateDraftPR opens a draft PR from head onto base and returns its URL.
	// created is false when the adapter instead adopted a pre-existing open PR
	// for head after the create call refused it as a duplicate (issue #2407).
	// Settle's reconstructed-PR path (issue #2447) must not word over an
	// adopted PR's title and body, so it needs the two cases apart.
	CreateDraftPR(title, body, base, head string) (url string, created bool, err error)
}

// BranchProtectionForge is the optional branch-protection-query interface (issue
// #2570): only a forge with a protection API (github, forgejo) implements it.
// Callers discover it with a type assertion, as with PRForge.
type BranchProtectionForge interface {
	// BranchProtected reports whether branch has protection configured. A
	// non-nil error means the probe could not determine the answer, a
	// permission error for instance; a definitive "not protected" is
	// (false, nil).
	BranchProtected(branch string) (bool, error)
}

// BundleCommitSubjects is settle's read-only PR-intent fallback (issue #2447):
// when a read-only Box's status=ready outcome carries no usable
// SPINDRIFT_PR_INTENT line, settle reconstructs the draft PR's title and body
// host-side from the relayed branch's own commits rather than blocking a
// finished hand-off. Only meaningful alongside BundleRelay and DraftPRCreator.
type BundleCommitSubjects interface {
	// CommitSubjects returns the one-line commit subjects the bundle at
	// outboxDir/seambundle.FileName carries for ref, relative to base, oldest first.
	CommitSubjects(outboxDir, base, ref string) ([]string, error)
}
