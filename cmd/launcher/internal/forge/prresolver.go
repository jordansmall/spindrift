package forge

// PRForIssue is the result of resolving the open PR for a dispatch issue's
// agent branch — the shared "does this forge have PRs, and what is the open
// PR for issue N" answer every call site across dispatch, settle, and the
// wave engine used to hand-roll after its own type assertion.
type PRForIssue struct {
	// Found reports whether an open PR exists for the issue's agent branch.
	Found bool
	// URL and IsDraft are only meaningful when Found is true.
	URL     string
	IsDraft bool
}

// ResolveOpenPR resolves the open PR for issue num on cf's agent branch. A
// push-only Code Forge (no PRForge surface) and "no open PR yet" both
// resolve to a zero PRForIssue (Found: false) with no error — the single
// absent policy every caller shares; only a genuine lookup failure returns a
// non-nil error. Callers that need the draft flag read res.IsDraft; callers
// that only care whether a PR exists check res.Found alone.
func ResolveOpenPR(cf CodeForge, num string) (PRForIssue, error) {
	pr, ok := cf.(PRForge)
	if !ok {
		return PRForIssue{}, nil
	}
	got, found, err := pr.OpenPRForBranch(cf.AgentBranch(num))
	if err != nil || !found {
		return PRForIssue{}, err
	}
	return PRForIssue{Found: true, URL: got.URL, IsDraft: got.IsDraft}, nil
}
