// Command forgepr is the fixture PR-capable forge Connector: it registers
// forge.Register, forge.RegisterBranches and forge.RegisterPRs and translates
// them to a forgejo-shaped REST backend over http_request, with Target.API as
// the base URL (the scripted backend's port is dynamic). Link-time mode
// (-ldflags=-X=main.mode=<mode>) selects a variant: "" sets every optional
// primitive; "minimal" sets none of the optional PR primitives;
// "undeclared-merge" merges through a path outside the repo subtree the Route
// table declares, which the host refuses unsent.
package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/forge"
)

var mode string

func init() {
	forge.Register(forge.Reader{
		PRsForBranch: prsForBranch,
		PR:           pr,
		Checks:       checks,
		JobLog:       jobLog,
	})
	forge.RegisterBranches(forge.Branches{
		Remote:       remote,
		BranchExists: branchExists,
		DeleteBranch: deleteBranch,
	})
	p := forge.PRs{
		Open:     open,
		Behind:   behind,
		Files:    files,
		SetDraft: setDraft,
		Comment:  comment,
		Merge:    merge,
	}
	if mode != "minimal" {
		p.AutoMergeAllowed = autoMergeAllowed
		p.EnqueueAutoMerge = enqueueAutoMerge
		p.CIRun = ciRun
		p.BranchProtected = branchProtected
		p.Probe = probe
	}
	forge.RegisterPRs(p)
}

// call sends one request to the backend and returns its body. A transport
// failure is Transient, so core may retry it; a non-2xx status is classified
// by status.
func call(method, url string, body any) ([]byte, error) {
	req := connector.Request{Method: method, URL: url}
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		req.Body = string(b)
		req.Headers = map[string]string{"Content-Type": "application/json"}
	}
	resp, err := connector.HTTPRequest(req)
	if err != nil {
		return nil, connector.Errorf(connector.Transient, "forge backend unreachable: %w", err)
	}
	if resp.Status/100 != 2 {
		return nil, statusErr(resp.Status)
	}
	return resp.Body, nil
}

func statusErr(status int) error {
	return connector.Errorf(connector.ClassForStatus(status), "forge backend answered status %d", status)
}

// get decodes a 2xx JSON answer into v.
func get(url string, v any) error {
	body, err := call(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

func repoURL(t forge.Target) string { return t.API + "/repos/" + t.Repo }

type pull struct {
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

func (p pull) forgePR() forge.PR {
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

func getPull(t forge.Target, number string) (pull, error) {
	var p pull
	err := get(repoURL(t)+"/pulls/"+number, &p)
	return p, err
}

type repoInfo struct {
	CloneURL    string `json:"clone_url"`
	HTMLURL     string `json:"html_url"`
	FullName    string `json:"full_name"`
	AllowRebase bool   `json:"allow_rebase"`
}

func getRepo(t forge.Target) (repoInfo, error) {
	var r repoInfo
	err := get(repoURL(t), &r)
	return r, err
}

func remote(t forge.Target) (forge.Remote, error) {
	r, err := getRepo(t)
	return forge.Remote{CloneURL: r.CloneURL, WebURL: r.HTMLURL}, err
}

func probe(t forge.Target) (string, error) {
	r, err := getRepo(t)
	return r.FullName, err
}

func autoMergeAllowed(t forge.Target) (bool, error) {
	r, err := getRepo(t)
	return r.AllowRebase, err
}

// The branch rides in the path raw: the host refuses an escaped path.
func branchExists(t forge.Target, branch string) (bool, error) {
	resp, err := connector.HTTPRequest(connector.Request{Method: http.MethodGet, URL: repoURL(t) + "/branches/" + branch})
	if err != nil {
		return false, connector.Errorf(connector.Transient, "forge backend unreachable: %w", err)
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
	_, err := call(http.MethodDelete, repoURL(t)+"/branches/"+branch, nil)
	return err
}

func branchProtected(t forge.Target, branch string) (bool, error) {
	var b struct {
		Protected bool `json:"protected"`
	}
	err := get(repoURL(t)+"/branches/"+branch, &b)
	return b.Protected, err
}

func prsForBranch(t forge.Target, branch string) ([]forge.PR, error) {
	var all []pull
	if err := get(repoURL(t)+"/pulls?state=all", &all); err != nil {
		return nil, err
	}
	var out []forge.PR
	for _, p := range all {
		if p.Head.Ref == branch {
			out = append(out, p.forgePR())
		}
	}
	return out, nil
}

func pr(t forge.Target, number string) (forge.PR, error) {
	p, err := getPull(t, number)
	return p.forgePR(), err
}

func open(t forge.Target, p forge.NewPR) (forge.PR, error) {
	body, err := call(http.MethodPost, repoURL(t)+"/pulls", p)
	if err != nil {
		return forge.PR{}, err
	}
	var created pull
	if err := json.Unmarshal(body, &created); err != nil {
		return forge.PR{}, err
	}
	return created.forgePR(), nil
}

func behind(t forge.Target, number string) (int, error) {
	p, err := getPull(t, number)
	if err != nil {
		return 0, err
	}
	var cmp struct {
		TotalCommits int `json:"total_commits"`
	}
	err = get(repoURL(t)+"/compare/"+p.Head.Ref+"..."+p.Base.Ref, &cmp)
	return cmp.TotalCommits, err
}

func files(t forge.Target, number string) ([]string, error) {
	var fs []struct {
		Filename string `json:"filename"`
	}
	if err := get(repoURL(t)+"/pulls/"+number+"/files", &fs); err != nil {
		return nil, err
	}
	var out []string
	for _, f := range fs {
		out = append(out, f.Filename)
	}
	return out, nil
}

func setDraft(t forge.Target, number string, draft bool) error {
	_, err := call(http.MethodPatch, repoURL(t)+"/pulls/"+number, map[string]bool{"draft": draft})
	return err
}

func comment(t forge.Target, number, body string) error {
	_, err := call(http.MethodPost, repoURL(t)+"/issues/"+number+"/comments", map[string]string{"body": body})
	return err
}

func mergeURL(t forge.Target, number string) string {
	if mode == "undeclared-merge" {
		return t.API + "/repos/elsewhere/r/pulls/" + number + "/merge"
	}
	return repoURL(t) + "/pulls/" + number + "/merge"
}

func merge(t forge.Target, number string) error {
	_, err := call(http.MethodPost, mergeURL(t, number), map[string]any{"Do": "rebase"})
	return err
}

func enqueueAutoMerge(t forge.Target, number string) error {
	_, err := call(http.MethodPost, mergeURL(t, number), map[string]any{"Do": "rebase", "merge_when_checks_succeed": true})
	return err
}

type combinedStatus struct {
	State    string `json:"state"`
	Statuses []struct {
		ID          int    `json:"id"`
		Context     string `json:"context"`
		State       string `json:"state"`
		Description string `json:"description"`
		TargetURL   string `json:"target_url"`
	} `json:"statuses"`
}

func status(t forge.Target, number string) (combinedStatus, error) {
	var s combinedStatus
	p, err := getPull(t, number)
	if err != nil {
		return s, err
	}
	err = get(repoURL(t)+"/commits/"+p.Head.SHA+"/status", &s)
	return s, err
}

func checks(t forge.Target, number string) (forge.Rollup, error) {
	s, err := status(t, number)
	if err != nil {
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

func ciRun(t forge.Target, number string) (string, error) {
	s, err := status(t, number)
	if err != nil {
		return "", err
	}
	for _, c := range s.Statuses {
		if c.TargetURL != "" {
			return c.TargetURL, nil
		}
	}
	return "", nil
}

func jobLog(t forge.Target, jobID string) (string, error) {
	body, err := call(http.MethodGet, repoURL(t)+"/actions/jobs/"+jobID+"/logs", nil)
	return string(body), err
}

func main() {}
