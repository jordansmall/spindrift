package forge

// CodeForge is the seam every adapter honors: agent branch naming, rebase,
// merge/landing under MERGE_MODE, and connectivity probe. Both adapters
// (github, the push-only git remote) implement it with real behavior — no
// stubs.
type CodeForge interface {
	// AgentBranch returns the agent branch name for issue num, with the
	// branch prefix baked in at construction. The Code Forge seam's single
	// owner of the branch-prefix rule — callers never concatenate it
	// themselves.
	AgentBranch(num string) string
	// BranchExists reports whether branch exists on the remote, independent
	// of any PR — the signal a bare `git push` with no PR opened yet (or a
	// PR already closed) still leaves behind. Reconcile's gated orphan reset
	// (#1432, #600) needs this alongside PRForge's PR-shaped checks, so it
	// lives on the core CodeForge surface every adapter honors.
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
// leaves its finished branch as a git bundle in the writable outbox instead;
// before Merge(ref) can find that branch as a ref on the backing repo, the
// bundle must be relayed in. Discovered via type assertion, like PRForge and
// LandingRecorder — only the local adapter implements it.
type BundleRelay interface {
	// RelayBundle imports ref from the bundle file the Box left in outboxDir
	// into the Code Forge's backing repo, so a subsequent Merge(ref) finds
	// the branch. Returns an error, leaving the seam unlanded, when the
	// bundle is missing or malformed.
	RelayBundle(outboxDir, ref string) error
}

// LandingRef is CODE_FORGE=local's optional post-merge landing-reference
// resolver (ADR 0029, ADR 0033): once Merge has landed the seam's branch,
// LandingRef resolves the immutable Integration ref + commit sha the
// landing: field records — richer than the raw branch name RecordLanding
// gets for github/git. Discovered via type assertion; only the local
// adapter implements it. Unlike Merge/Rebase, it takes no ref argument: the
// value it resolves is a property of the adapter's own fixed Integration
// branch (baked in at construction), not of whichever branch was merged.
type LandingRef interface {
	// LandingRef resolves the landing reference, once a merge has landed.
	LandingRef() (string, error)
}

// LandingVerifier is CODE_FORGE=local's optional no-network merge-observation
// surface (ADR 0029, ADR 0033): reconcile's sole closing authority extends
// here for a Code Forge with no PR concept to check instead. Discovered via
// type assertion, like PRForge; only the local adapter implements it.
type LandingVerifier interface {
	// VerifyLanding reports whether landing — the immutable ref
	// RecordLanding persisted — is merged into the adapter's own Integration
	// branch, checked via local git ancestry, never a network call. A
	// malformed landing (not this adapter's "<branch>@<sha>" shape) and a
	// landing whose commit is not an ancestor of the Integration branch's
	// current tip (the merge that recorded it in fact conflicted) both
	// report merged=false with a nil error — either way reconcile leaves the
	// seam-issue open rather than closing it, the same "stays open, blocked"
	// posture. A non-nil error is reserved for a genuine local-git failure
	// (e.g. the Accumulation repo itself is unreadable).
	VerifyLanding(landing string) (merged bool, err error)
}

// PRForge is the optional PR, CI-rollup, and auto-merge surface. Only
// adapters that open pull requests and watch CI implement it (github); the
// push-only git adapter does not. Callers discover it with a type assertion —
// `pr, ok := cf.(PRForge)` — the standard Go optional-interface pattern,
// rather than a PushOnly capability flag.
type PRForge interface {
	// OpenPRForBranch returns the open non-draft PR for branch, if any.
	OpenPRForBranch(branch string) (PR, bool, error)
	// PRForBranch returns the URL of any PR (any state) for branch, if any.
	PRForBranch(branch string) (string, bool, error)
	// PRState returns the canonical state of the given PR URL.
	PRState(url string) (PRState, error)
	// Mergeable returns the PR's content-mergeability state — whether the
	// PR's changes conflict with its base branch, as distinct from CI checks
	// or branch-protection gating.
	Mergeable(url string) (MergeableState, error)
	// CheckState returns the aggregate CI rollup state for the PR's head commit.
	CheckState(url string) (RollupState, error)
	// NeedsUpdate reports whether the PR's base branch has commits its head
	// branch has not yet incorporated — a pure git-ancestry fact, distinct
	// from Mergeable's conflict check: a PR can need updating (its tested
	// tree predates a just-merged sibling) while still being MERGEABLE (no
	// textual conflict). That gap let #670 and #672 land a combined compile
	// break on main even though each was individually green (issue #936).
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
	// EnqueueAutoMerge enqueues native auto-merge for the PR.
	EnqueueAutoMerge(prURL string) error
	// MarkReady flips the PR out of draft. Marking an already-ready PR is
	// idempotent: it succeeds without error rather than reporting a failure.
	MarkReady(prURL string) error
}
