package main

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"spindrift.dev/sdk/forge"
)

const (
	pageSize = 100
	// maxPages bounds every paging loop; a longer list fails rather than truncates.
	maxPages = 50
)

type rawPR struct {
	Number         int    `json:"number"`
	URL            string `json:"html_url"`
	State          string `json:"state"`
	Merged         bool   `json:"merged"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	// Mergeable is absent while Forgejo has not computed it.
	Mergeable *bool  `json:"mergeable"`
	Draft     bool   `json:"draft"`
	Title     string `json:"title"`
	Head      struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
}

// isDraftTitle reports a leading WIP marker, case-insensitively, among
// Forgejo's default work-in-progress prefixes: an instance may encode a draft
// in the title alone.
func isDraftTitle(title string) bool {
	upper := strings.ToUpper(strings.TrimSpace(title))
	return strings.HasPrefix(upper, "WIP:") || strings.HasPrefix(upper, "[WIP]")
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
		Mergeable: "UNKNOWN",
	}
	if r.Mergeable != nil {
		p.Mergeable = "CONFLICTING"
		if *r.Mergeable {
			p.Mergeable = "MERGEABLE"
		}
	}
	if p.Merged {
		p.MergeCommit = r.MergeCommitSHA
	}
	return p
}

// forgePath is a REST path under the target repository; rest starts with "/".
// t.API is the instance's base URL, so a sub-path install keeps its prefix.
func forgePath(t forge.Target, rest string) string { return "/api/v1/repos/" + t.Repo + rest }

func parseNumber(number string) (int, error) {
	n, err := strconv.Atoi(number)
	if err != nil || n < 1 || strconv.Itoa(n) != number {
		return 0, fmt.Errorf("number %q is not a positive integer", number)
	}
	return n, nil
}

// pageUntilEmpty reads path's pages until one is empty, the way the native
// adapter does: Forgejo caps `limit` below pageSize on some instances, so a
// short page does not mark the end. A page whose first item repeats the
// previous page's ends the walk too, for a forge that ignores `page`.
func pageUntilEmpty[T any](api, path string, q url.Values, key func(T) int) ([]T, error) {
	var items []T
	prevFirst := 0
	for page := 1; page <= maxPages; page++ {
		q.Set("limit", strconv.Itoa(pageSize))
		q.Set("page", strconv.Itoa(page))
		var batch []T
		if err := call("GET", api, path+"?"+q.Encode(), nil, &batch); err != nil {
			return nil, err
		}
		if len(batch) == 0 || (page > 1 && key(batch[0]) == prevFirst) {
			return items, nil
		}
		prevFirst = key(batch[0])
		items = append(items, batch...)
	}
	return nil, fmt.Errorf("GET %s: more than %d pages", path, maxPages)
}

// prsForBranch lists every PR and keeps those headed at branch: Forgejo's list
// has no head filter. Like the native adapter it matches on the head ref alone,
// so a fork PR of the same branch name is returned too.
func prsForBranch(t forge.Target, branch string) ([]forge.PR, error) {
	raw, err := pageUntilEmpty(t.API, forgePath(t, "/pulls"), url.Values{"state": {"all"}},
		func(r rawPR) int { return r.Number })
	if err != nil {
		return nil, err
	}
	prs := []forge.PR{}
	for _, r := range raw {
		if r.Head.Ref == branch {
			prs = append(prs, r.pr())
		}
	}
	// Newest first: PR numbers only grow.
	slices.SortFunc(prs, func(a, b forge.PR) int {
		x, _ := strconv.Atoi(a.Number)
		y, _ := strconv.Atoi(b.Number)
		return y - x
	})
	return prs, nil
}

func readPR(t forge.Target, number string) (rawPR, error) {
	var raw rawPR
	if _, err := parseNumber(number); err != nil {
		return raw, err
	}
	err := call("GET", t.API, forgePath(t, "/pulls/"+number), nil, &raw)
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
}

// latestStatuses is every context's newest status, in first-seen order. The
// endpoint lists every status ever reported for the sha, so one context
// repeats; IDs only grow, and first seen wins on equal or missing ones.
func latestStatuses(t forge.Target, sha string) ([]rawStatus, error) {
	all, err := pageUntilEmpty(t.API, forgePath(t, "/commits/"+url.PathEscape(sha)+"/statuses"), url.Values{},
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
// it is resolved from the statuses instead, as the native adapter does.
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
	if err := call("GET", t.API, forgePath(t, "/commits/"+url.PathEscape(sha)+"/status"), nil, &combined); err != nil {
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

// remote is the repository's identity, built from the configured base URL: no
// request, and no credential. A userinfo in the base is dropped, since core
// refuses a remote carrying one and git authenticates out of band.
func remote(t forge.Target) (forge.Remote, error) {
	owner, name, ok := strings.Cut(t.Repo, "/")
	if !ok || !validSegment(owner) || !validSegment(name) {
		return forge.Remote{}, fmt.Errorf("repo %q is not owner/name", t.Repo)
	}
	u, err := url.Parse(t.API)
	if err != nil {
		return forge.Remote{}, fmt.Errorf("base URL is not parseable: %w", err)
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	u.Path = strings.TrimSuffix(u.Path, "/") + "/" + t.Repo
	u.RawPath = ""
	web := u.String()
	return forge.Remote{CloneURL: web + ".git", WebURL: web}, nil
}

func validSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.Contains(s, "/")
}

// branchPath escapes branch for the URL path, keeping "/" a separator
// (agent/issue-N): Forgejo's branch route is a wildcard, and the host refuses
// an escaped slash. An empty, "." or ".." segment is refused: it would resolve
// outside the route subtree the host allows.
func branchPath(branch string) (string, error) {
	segs := strings.Split(branch, "/")
	for i, s := range segs {
		if s == "" || s == "." || s == ".." {
			return "", fmt.Errorf("branch %q has an empty, \".\" or \"..\" path segment", branch)
		}
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/"), nil
}

func branchExists(t forge.Target, branch string) (bool, error) {
	p, err := branchPath(branch)
	if err != nil {
		return false, err
	}
	path := forgePath(t, "/branches/"+p)
	resp, err := request("GET", t.API, path, nil)
	if err != nil {
		return false, err
	}
	switch {
	case is2xx(resp.Status):
		return true, nil
	case resp.Status == 404:
		return false, nil
	}
	return false, statusError("GET", path, resp.Status)
}

func deleteBranch(t forge.Target, branch string) error {
	p, err := branchPath(branch)
	if err != nil {
		return err
	}
	path := forgePath(t, "/branches/"+p)
	resp, err := request("DELETE", t.API, path, nil)
	if err != nil {
		return err
	}
	if !is2xx(resp.Status) {
		// A 404 is classed not-found, which core counts as a delete done.
		return statusError("DELETE", path, resp.Status)
	}
	return nil
}
