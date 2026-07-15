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

// ResolveOpenPRFiles resolves num's open PR and returns the paths it
// changes, absorbing the PRForge assertion so callers don't need their own
// after ResolveOpenPR already made one. Mirrors ResolveOpenPR's absent
// policy: a push-only Code Forge and "no open PR yet" both resolve to (nil,
// nil); a found PR's ListPRFiles failure propagates as a non-nil error.
func ResolveOpenPRFiles(cf CodeForge, num string) ([]string, error) {
	res, err := ResolveOpenPR(cf, num)
	if err != nil || !res.Found {
		return nil, err
	}
	// res.Found is only true when cf implements PRForge (ResolveOpenPR's own
	// contract), so this assertion always succeeds here.
	pr := cf.(PRForge)
	return pr.ListPRFiles(res.URL)
}
