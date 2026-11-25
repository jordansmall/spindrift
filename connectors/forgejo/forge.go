package main

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/forge"
)

const pageSize = 100

type rawPR struct {
	Number         int    `json:"number"`
	URL            string `json:"html_url"`
	State          string `json:"state"`
	Merged         bool   `json:"merged"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	// Mergeable is a plain bool: false also covers a still-running conflict
	// check and a WIP pull. Core's merge classification re-reads once.
	Mergeable bool   `json:"mergeable"`
	Draft     bool   `json:"draft"`
	Title     string `json:"title"`
	Head      struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (r rawPR) pr() forge.PR {
	p := forge.PR{
		Number:    strconv.Itoa(r.Number),
		URL:       r.URL,
		State:     r.State,
		Merged:    r.Merged,
		Draft:     r.Draft || isDraftTitle(r.Title),
		Head:      r.Head.Ref,
		HeadSHA:   r.Head.SHA,
		Mergeable: "CONFLICTING",
	}
	if r.Mergeable {
		p.Mergeable = "MERGEABLE"
	}
	if p.Merged {
		p.MergeCommit = r.MergeCommitSHA
	}
	return p
}

// forgePath is a REST path under the target repository; rest starts with "/".
// t.API is the instance's base URL, so a sub-path install keeps its prefix.
func forgePath(t forge.Target, rest string) string { return "/api/v1/repos/" + t.Repo + rest }

// send is SendJSON to api+path with the answer's status and headers dropped; a
// caller that judges the status itself calls SendJSON.
func send(method, api, path string, in, out any) error {
	_, err := connector.SendJSON(connector.Request{Method: method, URL: api + path}, in, out)
	return err
}

func parseNumber(number string) (int, error) {
	n, err := strconv.Atoi(number)
	if err != nil || n < 1 || strconv.Itoa(n) != number {
		return 0, fmt.Errorf("number %q is not a positive integer", number)
	}
	return n, nil
}

// pageUntilEmpty reads path's pages until one is empty: Forgejo caps `limit`
// below pageSize on some instances, so a short page does not mark the end. A
// page whose first item repeats the previous page's ends the walk too, for a
// forge that ignores `page`. There is no page bound: Forgejo caps each page at
// its MAX_RESPONSE_ITEMS, so a large repo's PR list legitimately runs past any
// fixed count.
func pageUntilEmpty[T any, K comparable](api, path string, q url.Values, key func(T) K) ([]T, error) {
	var items []T
	var prevFirst K
	for page := 1; ; page++ {
		q.Set("limit", strconv.Itoa(pageSize))
		q.Set("page", strconv.Itoa(page))
		var batch []T
		if err := send("GET", api, path+"?"+q.Encode(), nil, &batch); err != nil {
			return nil, err
		}
		if len(batch) == 0 || (page > 1 && key(batch[0]) == prevFirst) {
			return items, nil
		}
		prevFirst = key(batch[0])
		items = append(items, batch...)
	}
}

// prsForBranch lists every PR and keeps those headed at branch: Forgejo's list
// has no head filter. It matches on the head ref alone, so a fork PR of the
// same branch name is returned too.
func prsForBranch(t forge.Target, branch string) ([]forge.PR, error) {
	raw, err := pageUntilEmpty(t.API, forgePath(t, "/pulls"), url.Values{"state": {"all"}},
		func(r rawPR) int { return r.Number })
	if err != nil {
		return nil, err
	}
	// Newest first: PR numbers only grow.
	slices.SortFunc(raw, func(a, b rawPR) int { return b.Number - a.Number })
	prs := []forge.PR{}
	for _, r := range raw {
		if r.Head.Ref == branch {
			prs = append(prs, r.pr())
		}
	}
	return prs, nil
}

func readPR(t forge.Target, number string) (rawPR, error) {
	var raw rawPR
	if _, err := parseNumber(number); err != nil {
		return raw, err
	}
	err := send("GET", t.API, forgePath(t, "/pulls/"+number), nil, &raw)
	return raw, err
}

func pr(t forge.Target, number string) (forge.PR, error) {
	raw, err := readPR(t, number)
	if err != nil {
		return forge.PR{}, err
	}
	return raw.pr(), nil
}

type rawStatus struct {
	ID          int    `json:"id"`
	Context     string `json:"context"`
	State       string `json:"state"`
	Description string `json:"description"`
	TargetURL   string `json:"target_url"`
}

// latestStatuses is every context's newest status, in first-seen order. The
// endpoint lists every status ever reported for the sha, so one context
// repeats; IDs only grow, and first seen wins on equal or missing ones.
func latestStatuses(t forge.Target, sha string) ([]rawStatus, error) {
	seg, err := connector.PathSegment(sha)
	if err != nil {
		return nil, err
	}
	all, err := pageUntilEmpty(t.API, forgePath(t, "/commits/"+seg+"/statuses"), url.Values{},
		func(s rawStatus) int { return s.ID })
	if err != nil {
		return nil, err
	}
	var out []rawStatus
	at := map[string]int{}
	for _, s := range all {
		i, seen := at[s.Context]
		switch {
		case !seen:
			at[s.Context] = len(out)
			out = append(out, s)
		case s.ID > out[i].ID:
			out[i] = s
		}
	}
	return out, nil
}

// rollupStates maps the combined status Forgejo reports to the vocabulary of
// forge.Rollup. Skipped ranks best, so it is the combined state only when every
// context was skipped.
var rollupStates = map[string]string{
	"success": "SUCCESS",
	"pending": "PENDING",
	"failure": "FAILURE",
	"error":   "ERROR",
	"skipped": "SUCCESS",
}

// checks reads the PR head commit's commit statuses. The combined state is the
// worst newest status per context, so a "warning" can mask a pending context:
// it is resolved from the statuses instead.
func checks(t forge.Target, number string) (forge.Rollup, error) {
	raw, err := readPR(t, number)
	if err != nil {
		return forge.Rollup{}, err
	}
	sha := raw.Head.SHA
	var combined struct {
		State      string `json:"state"`
		TotalCount int    `json:"total_count"`
	}
	seg, err := connector.PathSegment(sha)
	if err != nil {
		return forge.Rollup{}, err
	}
	if err := send("GET", t.API, forgePath(t, "/commits/"+seg+"/status"), nil, &combined); err != nil {
		return forge.Rollup{}, err
	}
	out := forge.Rollup{Checks: []forge.Check{}}
	if combined.TotalCount == 0 {
		return out, nil
	}
	state, known := rollupStates[combined.State]
	if !known && combined.State != "warning" {
		return forge.Rollup{}, fmt.Errorf("unrecognized combined commit status state %q", combined.State)
	}
	statuses, err := latestStatuses(t, sha)
	if err != nil {
		return forge.Rollup{}, err
	}
	for _, s := range statuses {
		out.Checks = append(out.Checks, forge.Check{
			Kind: "StatusContext", Name: s.Context, Conclusion: strings.ToUpper(s.State), Summary: s.Description,
			JobID: jobID(t, s.TargetURL),
		})
	}
	out.State = state
	if !known {
		out.State = resolveWarning(statuses)
	}
	return out, nil
}

// resolveWarning is red if any context is, pending if any still runs, and
// otherwise success: warnings are advisory.
func resolveWarning(statuses []rawStatus) string {
	state := "SUCCESS"
	for _, s := range statuses {
		switch s.State {
		case "failure", "error":
			return strings.ToUpper(s.State)
		case "pending":
			state = "PENDING"
		}
	}
	return state
}

func validRepo(repo string) bool {
	owner, name, ok := strings.Cut(repo, "/")
	_, ownerErr := connector.PathSegment(owner)
	_, nameErr := connector.PathSegment(name)
	return ok && ownerErr == nil && nameErr == nil
}

// branchPath escapes branch for the URL path, keeping "/" a separator
// (agent/issue-N): Forgejo's branch route is a wildcard, and the host refuses
// an escaped slash. An empty, "." or ".." segment is refused: it would resolve
// outside the route subtree the host allows.
func branchPath(branch string) (string, error) {
	segs := strings.Split(branch, "/")
	for i, s := range segs {
		var err error
		if segs[i], err = connector.PathSegment(s); err != nil {
			return "", fmt.Errorf("branch %q: %w", branch, err)
		}
	}
	return strings.Join(segs, "/"), nil
}

func branchExists(t forge.Target, branch string) (bool, error) {
	p, err := branchPath(branch)
	if err != nil {
		return false, err
	}
	path := forgePath(t, "/branches/"+p)
	resp, err := connector.SendJSON(connector.Request{Method: "GET", URL: t.API + path}, nil, nil)
	if resp.Status == 404 {
		return false, nil
	}
	return err == nil, err
}

func deleteBranch(t forge.Target, branch string) error {
	p, err := branchPath(branch)
	if err != nil {
		return err
	}
	// A 404 is classed not-found, which core counts as a delete done.
	return send("DELETE", t.API, forgePath(t, "/branches/"+p), nil, nil)
}

// jobPathSegs counts the segments of a job page path below the base URL:
// <owner>/<repo>/actions/runs/<n>/jobs/<m>.
const jobPathSegs = 7

// jobID is "<run number>/<job index>" for a status link that is an Actions job
// page of the target repo, else "". The link is remote-supplied, so only a
// relative one (no scheme, no host) is accepted, as Forgejo emits it; the
// segments ahead of the job path must be the base URL's own sub-path exactly
// and the repo must match: the log read is authorised for the target repo
// alone.
func jobID(t forge.Target, targetURL string) string {
	u, err := url.Parse(targetURL)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return ""
	}
	base, err := url.Parse(t.API)
	if err != nil {
		return ""
	}
	var baseSegs []string
	if p := strings.Trim(base.Path, "/"); p != "" {
		baseSegs = strings.Split(p, "/")
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) != len(baseSegs)+jobPathSegs || !slices.Equal(segs[:len(baseSegs)], baseSegs) {
		return ""
	}
	tail := segs[len(baseSegs):]
	if !strings.EqualFold(tail[0]+"/"+tail[1], t.Repo) || tail[2] != "actions" || tail[3] != "runs" || tail[5] != "jobs" {
		return ""
	}
	run, err := strconv.ParseUint(tail[4], 10, 64)
	if err != nil || run == 0 { // Forgejo applies run_number only when > 0
		return ""
	}
	job, err := strconv.ParseUint(tail[6], 10, 64)
	if err != nil {
		return ""
	}
	return strconv.FormatUint(run, 10) + "/" + strconv.FormatUint(job, 10)
}

// parseJobID splits a jobID's "<run>/<job>", refusing run 0 (run_number is only applied when > 0).
func parseJobID(id string) (run, job uint64, err error) {
	r, j, ok := strings.Cut(id, "/")
	if ok {
		if run, err = strconv.ParseUint(r, 10, 64); err == nil {
			job, err = strconv.ParseUint(j, 10, 64)
		}
	}
	if !ok || err != nil || run == 0 {
		return 0, 0, fmt.Errorf("job id %q is not <run>/<job>", id)
	}
	return run, job, nil
}

// jobLog reads a job's log through the REST API: web routes ignore API
// tokens. The status link names the run by its per-repo number and the job by
// its index, so each is resolved to an API id first. Both index the run's jobs
// by id order (models/actions GetRunJobsByRunID). A pre-16 instance has none of
// these routes and answers 404, an error core drops: no log, not a failed
// detail.
func jobLog(t forge.Target, id string) (string, error) {
	run, job, err := parseJobID(id)
	if err != nil {
		return "", err
	}
	if !validRepo(t.Repo) {
		return "", fmt.Errorf("repo %q is not owner/name", t.Repo)
	}
	var runs struct {
		WorkflowRuns []struct {
			ID uint64 `json:"id"`
		} `json:"workflow_runs"`
	}
	q := url.Values{"run_number": {strconv.FormatUint(run, 10)}}
	if err := send("GET", t.API, forgePath(t, "/actions/runs?"+q.Encode()), nil, &runs); err != nil {
		return "", err
	}
	if len(runs.WorkflowRuns) == 0 {
		return "", fmt.Errorf("no workflow run numbered %d", run)
	}
	var jobs []struct {
		ID uint64 `json:"id"`
	}
	runPath := forgePath(t, "/actions/runs/"+strconv.FormatUint(runs.WorkflowRuns[0].ID, 10)+"/jobs")
	if err := send("GET", t.API, runPath, nil, &jobs); err != nil {
		return "", err
	}
	if job >= uint64(len(jobs)) {
		return "", fmt.Errorf("run %d has no job at index %d", run, job)
	}
	logPath := forgePath(t, "/actions/jobs/"+strconv.FormatUint(jobs[job].ID, 10)+"/logs")
	resp, err := connector.SendJSON(connector.Request{Method: "GET", URL: t.API + logPath}, nil, nil)
	if err != nil {
		return "", err
	}
	return string(resp.Body), nil
}
