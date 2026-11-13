package main

import (
	"time"

	"spindrift.dev/sdk/tracker"
)

// countInterval is how often core re-asks count: it walks the label's open
// issues page by page, so it is spaced to stay clear of the primary rate limit.
const countInterval = 60 * time.Second

// trackerProbe is one authenticated read of the repository's issues, the surface the
// tracker needs: a bad credential answers 401 and a missing repository 404. A
// non-2xx is reported as its status, not an error, so core decides what an
// auth failure means.
func trackerProbe(t tracker.Target) (int, error) {
	resp, err := request("GET", t.API, repoPath(t)+"?state=open&per_page=1", nil)
	if err != nil {
		return 0, err
	}
	return resp.Status, nil
}

// count sizes exactly the set listIssues(q) returns, pull requests excluded.
// Count takes no freshness flag, so there is no conditional-request cache to
// keep the way the native adapter's demand probe has.
func count(t tracker.Target, q tracker.Query) (int, error) {
	n := 0
	err := pages(t.API, repoPath(t)+listQuery(q), func(raw []rawIssue) bool {
		for _, r := range raw {
			if r.PullRequest == nil {
				n++
			}
		}
		return false
	})
	return n, err
}
