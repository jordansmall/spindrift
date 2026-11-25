// Command connectorname is a forge Connector (ADR 0073): REST calls sent
// through the host's http_request Host call, which attaches the credential.
// The scaffold answers core's branch and PR primitives against a
// forgejo-shaped REST backend, which is what the conformance suite's scripted
// backend serves. Make each function below speak your forge's API instead, and
// declare the routes it calls in routes.nix. Set forge.PRs.Open to let the host
// open the PR for a branch it pushed itself.
package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/forge"
)

func init() {
	forge.Register(forge.Reader{
		PRsForBranch: prsForBranch,
		PR:           pr,
		Checks:       checks,
		JobLog:       jobLog,
	})
	forge.RegisterBranches(forge.Branches{
		BranchExists: branchExists,
		DeleteBranch: deleteBranch,
		Probe:        probe,
	})
	forge.RegisterPRs(forge.PRs{
		Behind:           behind,
		Files:            files,
		SetDraft:         setDraft,
		Comment:          comment,
		Merge:            merge,
		AutoMergeAllowed: autoMergeAllowed,
		EnqueueAutoMerge: enqueueAutoMerge,
	})
}

// send issues one request and returns the backend's answer whatever its
// status. A transport failure is Transient, so core may retry it.
func send(method, u string, body any) (connector.Response, error) {
	req := connector.Request{Method: method, URL: u}
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return connector.Response{}, err
		}
		req.Body = string(b)
		req.Headers = map[string]string{"Content-Type": "application/json"}
	}
	resp, err := connector.HTTPRequest(req)
	if err != nil {
		return connector.Response{}, connector.Errorf(connector.Transient, "forge backend unreachable: %w", err)
	}
	return resp, nil
}

func statusErr(status int) error {
	return connector.Errorf(connector.ClassForStatus(status), "forge backend answered status %d", status)
}

// call returns a 2xx answer's body; any other status is classified by status.
func call(method, u string, body any) ([]byte, error) {
	resp, err := send(method, u, body)
	if err != nil {
		return nil, err
	}
	if resp.Status/100 != 2 {
		return nil, statusErr(resp.Status)
	}
	return resp.Body, nil
}

// get decodes a 2xx JSON answer into v.
func get(u string, v any) error {
	body, err := call(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

// repoURL is the repository's API root; Target.API must be the API root the
// routes sit under (the `base` host plus `/api/v1`, per routes.nix).
func repoURL(t forge.Target) string { return t.API + "/repos/" + t.Repo }

// checkNumber refuses a host-supplied number or job id that is not a positive
// integer, so it cannot add path segments or a query to a URL. what names the
// value in the error.
func checkNumber(what, v string) error {
	if n, err := strconv.Atoi(v); err != nil || n < 1 || strconv.Itoa(n) != v {
		return fmt.Errorf("%s %q is not a positive integer", what, v)
	}
	return nil
}

// pullURL, issueURL and jobLogURL are where a host-supplied number enters a
// path, so each guards it.
func pullURL(t forge.Target, number string) (string, error) {
	if err := checkNumber("number", number); err != nil {
		return "", err
	}
	return repoURL(t) + "/pulls/" + number, nil
}

func issueURL(t forge.Target, number string) (string, error) {
	if err := checkNumber("number", number); err != nil {
		return "", err
	}
	return repoURL(t) + "/issues/" + number, nil
}

func jobLogURL(t forge.Target, jobID string) (string, error) {
	if err := checkNumber("job id", jobID); err != nil {
		return "", err
	}
	return repoURL(t) + "/actions/jobs/" + jobID + "/logs", nil
}

const pageSize = 50

// pageUntilEmpty reads path's pages until one is empty. A page whose first item
// repeats the previous page's ends the walk too, for a forge that ignores
// `page`, as the conformance suite's scripted backend does.
func pageUntilEmpty[T any, K comparable](path string, q url.Values, key func(T) K) ([]T, error) {
	q = maps.Clone(q)
	var items []T
	var prevFirst K
	for page := 1; ; page++ {
		q.Set("limit", strconv.Itoa(pageSize))
		q.Set("page", strconv.Itoa(page))
		var batch []T
		if err := get(path+"?"+q.Encode(), &batch); err != nil {
			return nil, err
		}
		if len(batch) == 0 || (page > 1 && key(batch[0]) == prevFirst) {
			return items, nil
		}
		prevFirst = key(batch[0])
		items = append(items, batch...)
	}
}

// restPull is the forge's PR JSON; replace it with your forge's shape.
type restPull struct {
	Number         int     `json:"number"`
	HTMLURL        string  `json:"html_url"`
	State          string  `json:"state"`
	Merged         bool    `json:"merged"`
	Draft          bool    `json:"draft"`
	Mergeable      bool    `json:"mergeable"`
	MergeCommitSHA *string `json:"merge_commit_sha"`
	Head           struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (p restPull) toPR() forge.PR {
	out := forge.PR{
		Number:    strconv.Itoa(p.Number),
		URL:       p.HTMLURL,
		State:     p.State,
		Merged:    p.Merged,
		Draft:     p.Draft,
		Head:      p.Head.Ref,
		HeadSHA:   p.Head.SHA,
		Mergeable: "CONFLICTING",
	}
	if p.MergeCommitSHA != nil {
		out.MergeCommit = *p.MergeCommitSHA
	}
	switch {
	case p.Merged || p.State == "closed":
		out.Mergeable = "UNKNOWN"
	case p.Mergeable:
		out.Mergeable = "MERGEABLE"
	}
	return out
}

func getPull(t forge.Target, number string) (restPull, error) {
	var p restPull
	u, err := pullURL(t, number)
	if err != nil {
		return p, err
	}
	err = get(u, &p)
	return p, err
}

func prsForBranch(t forge.Target, branch string) ([]forge.PR, error) {
	all, err := pageUntilEmpty(repoURL(t)+"/pulls", url.Values{"state": {"all"}},
		func(p restPull) int { return p.Number })
	if err != nil {
		return nil, err
	}
	var out []forge.PR
	for _, p := range all {
		if p.Head.Ref == branch {
			out = append(out, p.toPR())
		}
	}
	return out, nil
}

func pr(t forge.Target, number string) (forge.PR, error) {
	p, err := getPull(t, number)
	return p.toPR(), err
}

// statuses is the combined commit status of a PR's head commit.
type statuses struct {
	State    string `json:"state"`
	Statuses []struct {
		ID          int    `json:"id"`
		Context     string `json:"context"`
		State       string `json:"state"`
		Description string `json:"description"`
	} `json:"statuses"`
}

func checks(t forge.Target, number string) (forge.Rollup, error) {
	p, err := getPull(t, number)
	if err != nil {
		return forge.Rollup{}, err
	}
	var s statuses
	if err := get(repoURL(t)+"/commits/"+url.PathEscape(p.Head.SHA)+"/status", &s); err != nil {
		return forge.Rollup{}, err
	}
	out := forge.Rollup{State: strings.ToUpper(s.State)}
	for _, c := range s.Statuses {
		check := forge.Check{
			Kind:       "StatusContext",
			Name:       c.Context,
			Conclusion: strings.ToUpper(c.State),
			Summary:    c.Description,
		}
		if c.ID != 0 {
			check.JobID = strconv.Itoa(c.ID)
		}
		out.Checks = append(out.Checks, check)
	}
	return out, nil
}

func jobLog(t forge.Target, jobID string) (string, error) {
	u, err := jobLogURL(t, jobID)
	if err != nil {
		return "", err
	}
	body, err := call(http.MethodGet, u, nil)
	return string(body), err
}

// restRepo is the repository JSON.
type restRepo struct {
	AllowRebase bool   `json:"allow_rebase"`
	FullName    string `json:"full_name"`
}

func getRepo(t forge.Target) (restRepo, error) {
	var r restRepo
	err := get(repoURL(t), &r)
	return r, err
}

// branchPath escapes each segment of a branch name for a URL path; the host
// refuses `.`/`..` segments, so they are rejected here with a clear error.
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
	bp, err := branchPath(branch)
	if err != nil {
		return false, err
	}
	resp, err := send(http.MethodGet, repoURL(t)+"/branches/"+bp, nil)
	if err != nil {
		return false, err
	}
	switch resp.Status {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	}
	return false, statusErr(resp.Status)
}

func deleteBranch(t forge.Target, branch string) error {
	bp, err := branchPath(branch)
	if err != nil {
		return err
	}
	_, err = call(http.MethodDelete, repoURL(t)+"/branches/"+bp, nil)
	return err
}

func behind(t forge.Target, number string) (int, error) {
	p, err := getPull(t, number)
	if err != nil {
		return 0, err
	}
	head, err := branchPath(p.Head.Ref)
	if err != nil {
		return 0, err
	}
	base, err := branchPath(p.Base.Ref)
	if err != nil {
		return 0, err
	}
	var cmp struct {
		TotalCommits int `json:"total_commits"`
	}
	err = get(repoURL(t)+"/compare/"+head+"..."+base, &cmp)
	return cmp.TotalCommits, err
}

func files(t forge.Target, number string) ([]string, error) {
	u, err := pullURL(t, number)
	if err != nil {
		return nil, err
	}
	type file struct {
		Filename string `json:"filename"`
	}
	fs, err := pageUntilEmpty(u+"/files", url.Values{},
		func(f file) string { return f.Filename })
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range fs {
		out = append(out, f.Filename)
	}
	return out, nil
}

func setDraft(t forge.Target, number string, draft bool) error {
	u, err := pullURL(t, number)
	if err != nil {
		return err
	}
	_, err = call(http.MethodPatch, u, map[string]bool{"draft": draft})
	return err
}

func comment(t forge.Target, number, body string) error {
	u, err := issueURL(t, number)
	if err != nil {
		return err
	}
	_, err = call(http.MethodPost, u+"/comments", map[string]string{"body": body})
	return err
}

// postMerge always rebases: the protocol carries no method. A 405 or 409 is the
// forge refusing an unmergeable PR, reported as NotMergeable so core re-reads
// the PR to tell why; core retries only a Transient failure. enqueueAutoMerge
// shares it, so a refused enqueue reports NotMergeable too.
func postMerge(t forge.Target, number string, extra map[string]any) error {
	u, err := pullURL(t, number)
	if err != nil {
		return err
	}
	body := map[string]any{"Do": "rebase"}
	for k, v := range extra {
		body[k] = v
	}
	resp, err := send(http.MethodPost, u+"/merge", body)
	if err != nil {
		return err
	}
	if resp.Status/100 == 2 {
		return nil
	}
	err = statusErr(resp.Status)
	if resp.Status == http.StatusMethodNotAllowed || resp.Status == http.StatusConflict {
		return connector.Errorf(connector.NotMergeable, "%v", err)
	}
	return err
}

func merge(t forge.Target, number string) error { return postMerge(t, number, nil) }

func probe(t forge.Target) (string, error) {
	r, err := getRepo(t)
	return r.FullName, err
}

func autoMergeAllowed(t forge.Target) (bool, error) {
	r, err := getRepo(t)
	return r.AllowRebase, err
}

func enqueueAutoMerge(t forge.Target, number string) error {
	return postMerge(t, number, map[string]any{"merge_when_checks_succeed": true})
}

// A reactor's exports are all it does; main is never called.
func main() {}
