package github

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/forge"
)

var _ forge.DemandCounter = (*execClient)(nil)

// demandProbeInterval is how often the daemon re-counts GitHub demand. An
// unchanged page answers 304, which is free against the primary rate limit,
// but a changed one costs a request (up to demandMaxPages when PRs crowd page
// 1), so the cadence stays relaxed.
const demandProbeInterval = 60 * time.Second

// demandPageSize is the REST per_page maximum, and the size of one probe page.
// Pull requests share the endpoint and take slots on the page, so CountReady
// reads deeper pages when they crowd out the issues.
const demandPageSize = 100

// demandMaxPages bounds the pages one probe reads. Deeper pages are read only
// when page 1 is read in full (changed, fresh, or uncached), but a PR-heavy
// label then costs a request per page, so past this depth the probe settles
// for an under-count.
const demandMaxPages = 5

// demandCache is the last 200 response CountReady saw: page 1's validator to
// send back and the count summed over the pages read, clamped to
// forge.ResultPageLimit.
type demandCache struct {
	etag  string
	count int
}

// CountReady implements forge.DemandCounter with one conditional request for
// the first page of open Dispatchable issues, newest-updated first, plus
// unconditional deeper pages when pull requests crowd it.
//
// The page is sorted by update time, not creation: relabelling an old issue
// bumps its updated_at but leaves the created-order first page untouched, so a
// created-sorted 304 would hide the newly ready issue. A 304 on page 1 returns
// the cached count at no primary-rate-limit cost. fresh skips If-None-Match,
// forcing a full read after a count the child contradicted.
//
// A full page 1 (PRs share the endpoint) is followed by deeper pages, summed
// up to forge.ResultPageLimit issues and demandMaxPages pages. Page 1's ETag
// does not cover them; ADR 0059 covers the resulting staleness and over-count.
func (e *execClient) CountReady(fresh bool) (int, error) {
	e.demandMu.Lock()
	defer e.demandMu.Unlock()

	path := fmt.Sprintf("repos/%s/issues?state=open&labels=%s&sort=updated&direction=desc&per_page=%d",
		e.repo, url.QueryEscape(e.labels.Label(forge.Dispatchable)), demandPageSize)
	count, entries, etag := 0, demandPageSize, ""
	for page := 1; entries >= demandPageSize && count < forge.ResultPageLimit && page <= demandMaxPages; page++ {
		pagePath, inm := path, ""
		if page > 1 {
			pagePath = fmt.Sprintf("%s&page=%d", path, page)
		} else if !fresh && e.demandCache != nil {
			inm = e.demandCache.etag
		}
		resp, err := fetchDemandPage(pagePath, inm)
		if err != nil {
			return 0, fmt.Errorf("page %d: %w", page, err)
		}
		if resp.status == 304 {
			if page == 1 {
				return e.cachedCount()
			}
			// No If-None-Match goes out on a deeper page, so a 304 is a protocol
			// surprise whose empty body would otherwise read as a short page.
			return 0, fmt.Errorf("page %d: gh api issues: unexpected HTTP status %d", page, resp.status)
		}
		issues, n, err := countPage(resp.body)
		if err != nil {
			return 0, fmt.Errorf("page %d: gh api issues: %w", page, err)
		}
		if page == 1 {
			etag = resp.etag
		}
		count += issues
		entries = n
	}
	count = min(count, forge.ResultPageLimit)
	e.demandCache = &demandCache{etag: etag, count: count}
	return count, nil
}

// fetchDemandPage reads one issues page, conditional on etag when non-empty.
// It returns a 200 or 304 response; every other outcome is an error.
func fetchDemandPage(path, etag string) (ghResponse, error) {
	args := []string{"api", path, "-i"}
	if etag != "" {
		args = append(args, "-H", "If-None-Match: "+etag)
	}
	out, err := exec.Command("gh", args...).Output()
	if err != nil {
		// gh exits 1 on a 304 yet still prints the status line and headers.
		cmdErr := ghCommandErr("gh api issues", err)
		resp, perr := parseGhInclude(out)
		if perr != nil {
			return ghResponse{}, cmdErr
		}
		if resp.status == 304 {
			return resp, nil
		}
		var rl *forge.RateLimitError
		if errors.As(cmdErr, &rl) {
			rl.Reset = resp.rateLimitReset(time.Now())
		}
		return ghResponse{}, fmt.Errorf("%w (HTTP status %d)", cmdErr, resp.status)
	}
	resp, err := parseGhInclude(out)
	if err != nil {
		return ghResponse{}, fmt.Errorf("gh api issues: %w", err)
	}
	if resp.status != 200 && resp.status != 304 {
		return ghResponse{}, fmt.Errorf("gh api issues: unexpected HTTP status %d", resp.status)
	}
	return resp, nil
}

// ProbeInterval implements forge.DemandCounter.
func (e *execClient) ProbeInterval() time.Duration { return demandProbeInterval }

// countPage counts the entries of a REST issues page that are issues:
// the endpoint also lists pull requests, marked by a pull_request key, which
// `gh issue list` (and so ListIssues) never returns. It also returns the
// page's total entry count, PRs included, so the caller can tell a full page
// (another may follow) from a short one.
func countPage(body []byte) (issues, entries int, err error) {
	var page []map[string]json.RawMessage
	if err := json.Unmarshal(body, &page); err != nil {
		return 0, 0, fmt.Errorf("decode issues page: %w", err)
	}
	for _, it := range page {
		if _, isPR := it["pull_request"]; !isPR {
			issues++
		}
	}
	return issues, len(page), nil
}

// ghResponse is the parsed output of `gh api -i`.
type ghResponse struct {
	status int
	etag   string
	body   []byte

	retryAfter     string // Retry-After, delta-seconds
	rateRemaining  string // X-RateLimit-Remaining
	rateResetEpoch string // X-RateLimit-Reset, Unix seconds
}

// rateLimitReset is when the limit lifts per the response headers, or zero when
// they name none. Retry-After wins, being the only signal for a secondary
// limit; X-RateLimit-Reset counts only once the primary quota is spent, since
// it is sent on every response.
func (r ghResponse) rateLimitReset(now time.Time) time.Time {
	if n, err := strconv.Atoi(r.retryAfter); err == nil && n >= 0 {
		return now.Add(time.Duration(n) * time.Second)
	}
	if r.rateRemaining == "0" {
		if n, err := strconv.ParseInt(r.rateResetEpoch, 10, 64); err == nil && n > 0 {
			return time.Unix(n, 0)
		}
	}
	return time.Time{}
}

// parseGhInclude splits `gh api -i` output into the status code, the ETag and
// rate-limit headers (names compare case-insensitively; gh prints Go-canonical
// "Etag" and "X-Ratelimit-Reset"), and the body. Header lines end in CRLF;
// bare LF is tolerated.
func parseGhInclude(out []byte) (ghResponse, error) {
	var r ghResponse
	rest := out
	first := true
	for {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			if first {
				return r, errors.New("no HTTP status line in gh output")
			}
			return r, nil
		}
		line := strings.TrimSuffix(string(rest[:i]), "\r")
		rest = rest[i+1:]
		if first {
			first = false
			fields := strings.Fields(line)
			if len(fields) < 2 || !strings.HasPrefix(fields[0], "HTTP/") {
				return r, fmt.Errorf("malformed HTTP status line %q", line)
			}
			code, err := strconv.Atoi(fields[1])
			if err != nil {
				return r, fmt.Errorf("malformed HTTP status line %q", line)
			}
			r.status = code
			continue
		}
		if line == "" {
			r.body = rest
			return r, nil
		}
		name, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "etag":
			r.etag = val
		case "retry-after":
			r.retryAfter = val
		case "x-ratelimit-remaining":
			r.rateRemaining = val
		case "x-ratelimit-reset":
			r.rateResetEpoch = val
		}
	}
}

// cachedCount answers a 304. The header is only ever sent with a cache entry,
// so a 304 without one is a protocol surprise, not a count of zero.
func (e *execClient) cachedCount() (int, error) {
	if e.demandCache == nil {
		return 0, errors.New("gh api issues: 304 Not Modified with no cached count")
	}
	return e.demandCache.count, nil
}
