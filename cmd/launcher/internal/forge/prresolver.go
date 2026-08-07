package forge

import "spindrift.dev/launcher/internal/retry"

// PRForIssue is the result of resolving the open PR for a dispatch issue's
// agent branch — the shared "does this forge have PRs, and what is the open
// PR for issue N" answer every call site across dispatch, settle, and the
// wave engine used to hand-roll after its own type assertion.
type PRForIssue struct {
	// Found reports whether an open PR exists for the issue's agent branch.
	Found bool
	// URL and IsDraft are only meaningful when Found is true. IsDraft
	// reports whether the resolved PR is a draft.
	URL     string
	IsDraft bool
}

// ResolveOpenPR resolves the open PR for issue num on cf's agent branch. A
// push-only Code Forge (no PRForge surface) and "no open PR yet" both
// resolve to a zero PRForIssue (Found: false) with no error — the single
// absent policy every caller shares; only a genuine lookup failure returns a
// non-nil error. Most callers only care whether a PR exists and check
// res.Found alone; res.IsDraft is populated for any caller that later needs
// the draft flag.
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

// ResolveOpenPRWithRetry resolves num's open PR like ResolveOpenPR, but
// retries a transient lookup failure (5xx, network blip — see
// isTransientForgeError) with backoff instead of propagating it immediately
// (issue #2323). A definitive "no open PR" result (Found: false, nil error)
// and any non-transient error both return on the first attempt. maxAttempts
// is clamped to at least 1. backoff.Do runs between attempts, never after
// the last one, so a caller with maxAttempts attempts sees at most
// maxAttempts-1 sleeps.
func ResolveOpenPRWithRetry(cf CodeForge, num string, backoff retry.LinearBackoff, maxAttempts int) (PRForIssue, error) {
	attempts := maxAttempts
	if attempts < 1 {
		attempts = 1
	}

	var res PRForIssue
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		res, err = ResolveOpenPR(cf, num)
		if err == nil || !isTransientForgeError(err) {
			return res, err
		}
		if attempt >= attempts {
			return res, err
		}
		backoff.Do(attempt)
	}
	return res, err
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
