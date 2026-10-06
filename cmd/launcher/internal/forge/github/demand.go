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
// but a changed one costs a request, so the cadence stays relaxed.
const demandProbeInterval = 60 * time.Second

// demandPageSize is the one page CountReady reads, and the REST per_page
// maximum.
const demandPageSize = 100

// demandCache is the last 200 response CountReady saw: the validator to send
// back and the count it produced.
type demandCache struct {
	etag  string
	count int
}

// CountReady implements forge.DemandCounter with one conditional request for
// the first page of open Dispatchable issues, newest-updated first.
//
// The page is sorted by update time, not creation: relabelling an old issue
// bumps its updated_at but leaves the created-order first page untouched, so a
// created-sorted 304 would hide the newly ready issue. A 304 returns the
// cached count and costs nothing against the primary rate limit. fresh skips
// If-None-Match, forcing a full read after a count the child contradicted.
func (e *execClient) CountReady(fresh bool) (int, error) {
	e.demandMu.Lock()
	defer e.demandMu.Unlock()

	path := fmt.Sprintf("repos/%s/issues?state=open&labels=%s&sort=updated&direction=desc&per_page=%d",
		e.repo, url.QueryEscape(e.labels.Label(forge.Dispatchable)), demandPageSize)
	args := []string{"api", path, "-i"}
	if !fresh && e.demandCache != nil && e.demandCache.etag != "" {
		args = append(args, "-H", "If-None-Match: "+e.demandCache.etag)
	}
	cmd := exec.Command("gh", args...)
	out, err := cmd.Output()
	if err != nil {
		// gh exits 1 on a 304 yet still prints the status line and headers.
		cmdErr := ghCommandErr("gh api issues", err)
		resp, perr := parseGhInclude(out)
		if perr != nil {
			return 0, cmdErr
		}
		if resp.status == 304 {
			return e.cachedCount()
		}
		var rl *forge.RateLimitError
		if errors.As(cmdErr, &rl) {
			rl.Reset = resp.rateLimitReset(time.Now())
		}
		return 0, fmt.Errorf("%w (HTTP status %d)", cmdErr, resp.status)
	}
	resp, err := parseGhInclude(out)
	if err != nil {
		return 0, fmt.Errorf("gh api issues: %w", err)
	}
	if resp.status == 304 {
		return e.cachedCount()
	}
	if resp.status != 200 {
		return 0, fmt.Errorf("gh api issues: unexpected HTTP status %d", resp.status)
	}
	count, err := countNonPRItems(resp.body)
	if err != nil {
		return 0, fmt.Errorf("gh api issues: %w", err)
	}
	e.demandCache = &demandCache{etag: resp.etag, count: count}
	return count, nil
}

// ProbeInterval implements forge.DemandCounter.
func (e *execClient) ProbeInterval() time.Duration { return demandProbeInterval }

// countNonPRItems counts the entries of a REST issues page that are issues:
// the endpoint also lists pull requests, marked by a pull_request key, which
// `gh issue list` (and so ListIssues) never returns.
func countNonPRItems(body []byte) (int, error) {
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(body, &items); err != nil {
		return 0, fmt.Errorf("decode issues page: %w", err)
	}
	n := 0
	for _, it := range items {
		if _, isPR := it["pull_request"]; !isPR {
			n++
		}
	}
	return n, nil
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
