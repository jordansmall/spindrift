package forge

import "spindrift.dev/launcher/internal/retry"

// OpenPR is the result of resolving the open PR on a branch.
type OpenPR struct {
	Found bool
	// URL is only meaningful when Found is true.
	URL string
}

// ResolveOpenPR resolves the open PR on branch. The caller names the branch
// because its prefix is core policy, not a Code Forge fact.
// A push-only Code Forge (no PRForge) and "no open PR yet" both resolve to a
// zero OpenPR with no error, so every caller shares one absent policy;
// only a genuine lookup failure returns a non-nil error.
func ResolveOpenPR(cf CodeForge, branch string) (OpenPR, error) {
	return openPR(ResolveCodeForge(cf).PRForge, branch)
}

func openPR(pr PRForge, branch string) (OpenPR, error) {
	if pr == nil {
		return OpenPR{}, nil
	}
	got, found, err := pr.OpenPRForBranch(branch)
	if err != nil || !found {
		return OpenPR{}, err
	}
	return OpenPR{Found: true, URL: got.URL}, nil
}

// ResolveOpenPRWithRetry resolves branch's open PR like ResolveOpenPR, but retries
// a transient lookup failure (see isTransientForgeError) with backoff (issue
// #2323). A definitive "no open PR" and any non-transient error both return on
// the first attempt. maxAttempts is clamped to at least 1.
func ResolveOpenPRWithRetry(cf CodeForge, branch string, backoff retry.LinearBackoff, maxAttempts int) (OpenPR, error) {
	attempts := maxAttempts
	if attempts < 1 {
		attempts = 1
	}

	var res OpenPR
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		res, err = ResolveOpenPR(cf, branch)
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

// ResolveOpenPRFiles resolves branch's open PR and returns the paths it changes.
// It mirrors ResolveOpenPR's absent policy: a push-only Code Forge and "no open
// PR yet" both resolve to (nil, nil).
func ResolveOpenPRFiles(cf CodeForge, branch string) ([]string, error) {
	pr := ResolveCodeForge(cf).PRForge
	res, err := openPR(pr, branch)
	if err != nil || !res.Found {
		return nil, err
	}
	return pr.ListPRFiles(res.URL)
}
