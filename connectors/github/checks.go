package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"spindrift.dev/sdk/tracker"
)

// countInterval is how often core re-asks count: it walks the label's open
// issues page by page (up to countMaxPages), so it is spaced to stay clear of
// the primary rate limit.
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

// countMaxPages bounds the pages one count reads: past it a PR-heavy label
// settles for an under-count rather than a request per page.
const countMaxPages = 5

// countEntry is the last full walk of one first-page request: page 1's ETag to
// send back and the count it produced.
type countEntry struct {
	etag  string
	count int
}

// countCache is keyed by the page-1 URL. It lives as long as the Connector
// instance; core drops and reloads a Connector after a failed call, which
// empties it, so the next poll just does a full read.
var countCache = map[string]countEntry{}

// countQuery selects page 1 of the open issues q names, newest-updated first.
// The update sort is load-bearing: relabelling an old issue bumps its
// updated_at and so changes page 1, whereas a created-order page 1 would not
// and a 304 would hide the newly labelled issue.
func countQuery(q tracker.Query) string {
	s := "?state=open&sort=updated&direction=desc&per_page=" + strconv.Itoa(pageSize)
	if q.Label != "" {
		s += "&labels=" + url.QueryEscape(q.Label)
	}
	return s
}

// count sizes the open issues q names, pull requests excluded. Page 1 is
// conditional on the cached ETag unless fresh, and a 304 returns the cached
// count. Deeper pages are read unconditionally while full (PRs share the
// endpoint), up to limit issues (when limit > 0) and countMaxPages pages; the
// result is clamped to limit. Page 1's ETag does not cover the deeper pages,
// so a cached count can lag them.
func count(t tracker.Target, q tracker.Query, limit int, fresh bool) (int, error) {
	path := repoPath(t) + countQuery(q)
	key := t.API + path
	cached, haveCache := countCache[key]
	n, etag := 0, ""
	for page := 1; page <= countMaxPages && (limit <= 0 || n < limit); page++ {
		pagePath := path + "&page=" + strconv.Itoa(page)
		conditional := page == 1 && !fresh && haveCache
		var extra map[string]string
		if conditional {
			extra = map[string]string{"If-None-Match": cached.etag}
		}
		resp, err := requestWith("GET", t.API, pagePath, nil, extra)
		if err != nil {
			return 0, err
		}
		if resp.Status == 304 && conditional {
			return cached.count, nil
		}
		if !is2xx(resp.Status) {
			// A 304 with no If-None-Match sent lands here too: a protocol
			// surprise whose empty body would otherwise read as a short page.
			return 0, statusError("GET", pagePath, resp.Status)
		}
		if page == 1 {
			// Read before the next request replaces the latest response.
			etag, _ = responseHeader("ETag")
		}
		var raw []rawIssue
		if err := json.Unmarshal(resp.Body, &raw); err != nil {
			return 0, fmt.Errorf("GET %s: decode: %w", pagePath, err)
		}
		for _, r := range raw {
			if r.PullRequest == nil {
				n++
			}
		}
		if len(raw) < pageSize {
			break
		}
	}
	if limit > 0 {
		n = min(n, limit)
	}
	if etag == "" {
		delete(countCache, key)
	} else {
		countCache[key] = countEntry{etag: etag, count: n}
	}
	return n, nil
}
