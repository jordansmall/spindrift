package forge

import "spindrift.dev/launcher/internal/retry"

// PRForIssue is the result of resolving the open PR for a dispatch issue's
// agent branch.
type PRForIssue struct {
	Found bool
	// URL is only meaningful when Found is true.
	URL string
}

// ResolveOpenPR resolves the open PR for issue num on cf's agent branch.
// A push-only Code Forge (no PRForge) and "no open PR yet" both resolve to a
// zero PRForIssue with no error, so every caller shares one absent policy;
// only a genuine lookup failure returns a non-nil error.
func ResolveOpenPR(cf CodeForge, num string) (PRForIssue, error) {
	pr, ok := cf.(PRForge)
	if !ok {
		return PRForIssue{}, nil
	}
	got, found, err := pr.OpenPRForBranch(cf.AgentBranch(num))
	if err != nil || !found {
		return PRForIssue{}, err
	}
	return PRForIssue{Found: true, URL: got.URL}, nil
}

// ResolveOpenPRWithRetry resolves num's open PR like ResolveOpenPR, but retries
// a transient lookup failure (see isTransientForgeError) with backoff (issue
// #2323). A definitive "no open PR" and any non-transient error both return on
// the first attempt. maxAttempts is clamped to at least 1.
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

// ResolveOpenPRFiles resolves num's open PR and returns the paths it changes.
// It mirrors ResolveOpenPR's absent policy: a push-only Code Forge and "no open
// PR yet" both resolve to (nil, nil).
func ResolveOpenPRFiles(cf CodeForge, num string) ([]string, error) {
	res, err := ResolveOpenPR(cf, num)
	if err != nil || !res.Found {
		return nil, err
	}
	// ResolveOpenPR only reports Found when cf implements PRForge, so this
	// assertion always succeeds.
	pr := cf.(PRForge)
	return pr.ListPRFiles(res.URL)
}
