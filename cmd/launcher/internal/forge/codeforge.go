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
	// Merge lands ref onto the target branch: a rebase merge of the PR (github)
	// or a plain merge-and-push of the branch name (git, MERGE_MODE=immediate).
	Merge(ref string) error
	// Rebase rebases ref onto its base and force-pushes: the PR's head branch
	// (github) or the branch name itself (git).
	Rebase(ref string) error
	// Probe checks code forge connectivity and returns the resolved repo slug.
	Probe() (string, error)
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
	// NeedsUpdate reports whether the PR's head is behind its base branch's
	// current tip (GitHub's mergeStateStatus BEHIND) — distinct from
	// Mergeable's conflict check: a PR can be BEHIND (its tested tree
	// predates a just-merged sibling) while still MERGEABLE (no textual
	// conflict). That gap let #670 and #672 land a combined compile break on
	// main even though each was individually green (issue #936).
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
}
