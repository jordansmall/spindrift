package forge

// CodeForge is the seam through which the launcher manages PRs, CI, and
// merges. Implementations support PR+CI+merge (GitHub) or push-only (git).
type CodeForge interface {
	// OpenPRForBranch returns the open non-draft PR for branch, if any.
	OpenPRForBranch(branch string) (PR, bool, error)
	// PRForBranch returns the URL of any PR (any state) for branch, if any.
	PRForBranch(branch string) (string, bool, error)
	// PRState returns the state (OPEN/MERGED/CLOSED) of the given PR URL.
	PRState(url string) (string, error)
	// CheckState returns the aggregate CI rollup state for the PR's head commit.
	CheckState(url string) (RollupState, error)
	// ListPRFiles returns every path changed by the PR (added, modified, deleted).
	ListPRFiles(url string) ([]string, error)
	// Merge performs a rebase merge of the PR and deletes the branch.
	Merge(url string) error
	// Rebase rebases the PR's head branch onto its base and force-pushes.
	Rebase(prURL string) error
	// CanAutoMerge reports whether the repository allows GitHub's native auto-merge.
	CanAutoMerge() (bool, error)
	// EnqueueAutoMerge enqueues native auto-merge for the PR.
	EnqueueAutoMerge(prURL string) error
	// Probe checks code forge connectivity and returns the resolved repo slug.
	Probe() (string, error)
}
