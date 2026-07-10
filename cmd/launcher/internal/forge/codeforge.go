package forge

// CodeForge is the seam through which the launcher manages PRs, CI, and
// merges. Implementations support PR+CI+merge (GitHub) or push-only (git).
type CodeForge interface {
	// OpenPRForBranch returns the open non-draft PR for branch, if any. The
	// push-only git adapter has no PR concept and always returns not-found.
	OpenPRForBranch(branch string) (PR, bool, error)
	// PRForBranch returns the URL of any PR (any state) for branch, if any.
	// The push-only git adapter has no PR concept and always returns not-found.
	PRForBranch(branch string) (string, bool, error)
	// PRState returns the canonical state of the given PR URL.
	PRState(url string) (PRState, error)
	// CheckState returns the aggregate CI rollup state for the PR's head commit.
	CheckState(url string) (RollupState, error)
	// FailureDetail returns the failed check names plus a bounded log excerpt
	// for the PR's head commit, or "" when nothing is currently failing.
	// Best-effort: callers must treat a non-nil error as "detail unavailable"
	// and proceed without it rather than failing the caller's own operation.
	FailureDetail(url string) (string, error)
	// ListPRFiles returns every path changed by the PR (added, modified, deleted).
	ListPRFiles(url string) ([]string, error)
	// Merge lands ref onto the target branch: a rebase merge of the PR (github)
	// or a plain merge-and-push of the branch name (git, MERGE_MODE=immediate).
	Merge(ref string) error
	// Rebase rebases ref onto its base and force-pushes: the PR's head branch
	// (github) or the branch name itself (git).
	Rebase(ref string) error
	// CanAutoMerge reports whether the repository allows GitHub's native auto-merge.
	CanAutoMerge() (bool, error)
	// EnqueueAutoMerge enqueues native auto-merge for the PR.
	EnqueueAutoMerge(prURL string) error
	// Probe checks code forge connectivity and returns the resolved repo slug.
	Probe() (string, error)
	// PushOnly reports whether this adapter has no PR/CI/merge concept and
	// lands code by pushing directly to a branch (the git adapter) as
	// opposed to opening a PR and watching CI (github). Callers key
	// behavior off this capability instead of comparing adapter names.
	PushOnly() bool
	// AgentBranch returns the agent branch name for issue num, with the
	// branch prefix baked in at construction. The Code Forge seam's single
	// owner of the branch-prefix rule — callers never concatenate it
	// themselves.
	AgentBranch(num string) string
}
