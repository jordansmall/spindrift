package forge

// CodeForge is the seam every adapter honors: agent branch naming, rebase,
// merge/landing under MERGE_MODE, and connectivity probe.
type CodeForge interface {
	// AgentBranch returns the agent branch name for issue num, with the branch
	// prefix baked in at construction. Sole owner of the branch-prefix rule —
	// callers never concatenate it themselves.
	AgentBranch(num string) string
	// BranchExists reports whether branch exists on the remote, independent of
	// any PR — the signal a bare `git push` with no PR opened yet (or a PR
	// already closed) still leaves behind. On the core surface because
	// Reconcile's gated orphan reset needs it from every adapter, not just
	// PR-shaped ones.
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

// BundleRelay is CODE_FORGE=local's optional pre-merge landing hook (ADR
// 0033): the Box cannot push to its read-only Accumulation-repo mount, so it
// leaves its finished branch as a git bundle in the writable outbox; before
// Merge(ref) can find that branch as a ref on the backing repo, the bundle
// must be relayed in.
type BundleRelay interface {
	// RelayBundle imports ref from the bundle file the Box left in outboxDir
	// into the Code Forge's backing repo, so a subsequent Merge(ref) finds
	// the branch. Returns an error, leaving the seam unlanded, when the
	// bundle is missing or malformed.
	RelayBundle(outboxDir, ref string) error
}

// LandingRef is CODE_FORGE=local's optional post-merge landing-reference
// resolver (ADR 0029, ADR 0033): once Merge has landed the seam's branch, it
// resolves the immutable Integration ref + commit sha the landing: field
// records — richer than the raw branch name RecordLanding gets for
// github/git. It takes no ref argument, unlike Merge/Rebase: the value is a
// property of the adapter's own fixed Integration branch, not of whichever
// branch was merged.
type LandingRef interface {
	LandingRef() (string, error)
}

// LandingRepair is CODE_FORGE=local's optional bookkeeping-repair surface
// (ADR 0029, ADR 0033): Reconcile's healing path for a seam whose merge landed
// but whose post-merge landing upgrade never ran, leaving a stale
// LandingBranchRef recorded instead of the rich LandingIntegrationRef.
// IntegrationTip takes the issue's resolved parent explicitly rather than
// leaning on the adapter's construction-time one, since Reconcile's sweep holds
// one Code Forge instance across a possibly mixed-parent batch.
type LandingRepair interface {
	// IntegrationTip resolves parent's own Integration branch to its current
	// landing-ready "<branch>@<sha>" reference — the value Reconcile's repair
	// records once LandingContained confirms the merge, in the same grammar
	// LandingRef produces for the fresh-merge path.
	IntegrationTip(parent string) (string, error)
}

// LandingContainmentQuery is CODE_FORGE=local's optional containment-check
// surface (ADR 0033): the sole no-network merge-observation seam reconcile's
// closing authority and the wave engine's dependent blocker gate both check a
// local Code Forge through. As with LandingRepair, scope's parent is an
// explicit argument, so one shared Code Forge instance can answer for any
// parent — a batch's broad ticket parent or a dependent's cross-seam parent
// (ADR 0033 D2) — in a single mixed pass.
type LandingContainmentQuery interface {
	// LandingContained reports whether landing's commit is already contained
	// in scope's own Integration branch, either as a plain git ancestor or by
	// patch-equivalence — a rebase-based land replays commits under new shas
	// that a pure ancestry check can no longer see. landing's commit sha comes
	// from its Kind: a LandingIntegrationRef supplies it directly; a
	// LandingBranchRef resolves it via the named branch's current tip; any
	// other shape (e.g. a PR URL reaching this local-only path) reports
	// contained=false, nil. An absent commit and a commit that hasn't yet
	// reached scope's Integration branch both report contained=false, nil —
	// the same "stays open, blocked" posture. A non-nil error is reserved for
	// a genuine local-git failure.
	LandingContained(landing Landing, scope SeedScope) (contained bool, err error)
}

// PRForge is the optional PR, CI-rollup, and auto-merge surface. Only adapters
// that open pull requests and watch CI implement it; the push-only git adapter
// does not. Callers discover it with a type assertion rather than a PushOnly
// capability flag.
type PRForge interface {
	// OpenPRForBranch returns the open PR for branch, if any, draft or not —
	// a stranded draft is exactly as adoptable as a ready PR.
	OpenPRForBranch(branch string) (PR, bool, error)
	// PRForBranch returns the URL of any PR (any state) for branch, if any.
	PRForBranch(branch string) (string, bool, error)
	PRState(url string) (PRState, error)
	// Mergeable returns the PR's content-mergeability state — whether the
	// PR's changes conflict with its base branch, as distinct from CI checks
	// or branch-protection gating.
	Mergeable(url string) (MergeableState, error)
	// CheckState returns the aggregate CI rollup state for the PR's head commit.
	CheckState(url string) (RollupState, error)
	// HeadCommitSHA returns the PR's current head commit SHA — the signal
	// selfHealGate compares before and after a fix pass to tell a genuine push
	// from a no-op fix pass, so a stale terminal rollup is never mistaken for a
	// fresh genuine red.
	HeadCommitSHA(url string) (string, error)
	// NeedsUpdate reports whether the PR's base branch has commits its head
	// branch has not yet incorporated — a pure git-ancestry fact, distinct
	// from Mergeable's conflict check: a PR can need updating (its tested tree
	// predates a just-merged sibling) while still being MERGEABLE. That gap has
	// landed a combined compile break on main from two individually-green PRs.
	NeedsUpdate(url string) (bool, error)
	// FailureDetail returns the failed check names plus a bounded log excerpt
	// for the PR's head commit, or "" when nothing is currently failing.
	// Best-effort: callers must treat a non-nil error as "detail unavailable"
	// and proceed without it rather than failing the caller's own operation.
	FailureDetail(url string) (string, error)
	// ListPRFiles returns every path changed by the PR (added, modified, deleted).
	ListPRFiles(url string) ([]string, error)
	// CanAutoMerge reports whether the repository allows GitHub's native auto-merge.
	CanAutoMerge() (bool, error)
	EnqueueAutoMerge(prURL string) error
	// MarkReady and MarkDraft flip the PR out of and back into draft. Both are
	// idempotent: marking a PR that is already in the target state succeeds
	// rather than reporting a failure.
	MarkReady(prURL string) error
	MarkDraft(prURL string) error
}

// DraftPRCreator is the optional Code Forge surface for host-side draft-PR
// creation: under BOX_FORGE_AND_ISSUE_ACCESS=read-only the Box holds no write
// token, so the Launcher opens the draft PR host-side from a
// title/body/base/head the Box supplies. Only meaningful for a PR-shaped forge.
type DraftPRCreator interface {
	// CreateDraftPR opens a draft PR from head onto base with the given title
	// and body, and returns its URL. created reports whether this call opened
	// a fresh PR (true) or adopted a pre-existing open PR for head after the
	// underlying create call refused it as a duplicate (false) — both real
	// adapters treat that refusal as idempotent success. Callers cannot always
	// treat the two the same: an adopted PR already has a title/body from
	// whoever opened it, so host-reconstructed text may only overwrite a PR
	// this call created fresh.
	CreateDraftPR(title, body, base, head string) (url string, created bool, err error)
}

// BranchProtectionForge is the optional branch-protection-query surface: only
// a forge with a protection API (github, forgejo) implements it; the push-only
// git adapter and CODE_FORGE=local's host-mediated adapter have no such
// concept to query.
type BranchProtectionForge interface {
	// BranchProtected reports whether branch has protection configured.
	// A non-nil error means the probe could not determine the answer (e.g. a
	// permission error) -- it never means "determined unprotected"; a
	// definitive "not protected" result is (false, nil).
	BranchProtected(branch string) (bool, error)
}

// BundleCommitSubjects is settle's read-only PR-intent-fallback hook: when a
// read-only Box's status=ready outcome carries no usable SPINDRIFT_PR_INTENT
// line, settle reconstructs a draft PR's title/body from the relayed branch's
// own commits host-side rather than blocking a finished hand-off. Only
// meaningful alongside BundleRelay + DraftPRCreator.
type BundleCommitSubjects interface {
	// CommitSubjects returns the one-line commit subjects the bundle at
	// outboxDir/seambundle.FileName carries for ref, relative to base,
	// oldest first.
	CommitSubjects(outboxDir, base, ref string) ([]string, error)
}
