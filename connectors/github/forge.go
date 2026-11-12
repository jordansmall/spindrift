package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"spindrift.dev/sdk/forge"
)

// rollupQuery is the one GraphQL document the Connector sends: REST check-runs
// answer 403 under a fine-grained PAT, the rollup does not.
const rollupQuery = `query($owner:String!,$repo:String!,$number:Int!){repository(owner:$owner,name:$repo){pullRequest(number:$number){commits(last:1){nodes{commit{statusCheckRollup{state contexts(first:50){nodes{__typename ... on CheckRun{name conclusion summary detailsUrl} ... on StatusContext{context state description}}}}}}}}}}`

type rawPR struct {
	Number   int     `json:"number"`
	URL      string  `json:"html_url"`
	State    string  `json:"state"`
	MergedAt *string `json:"merged_at"`
}

func (r rawPR) pr() forge.PR {
	return forge.PR{
		Number: strconv.Itoa(r.Number),
		URL:    r.URL,
		State:  r.State,
		Merged: r.MergedAt != nil,
	}
}

func parseNumber(number string) (int, error) {
	n, err := strconv.Atoi(number)
	if err != nil || n < 1 || strconv.Itoa(n) != number {
		return 0, fmt.Errorf("PR number %q is not a positive integer", number)
	}
	return n, nil
}

func splitRepo(repo string) (owner, name string, err error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return "", "", fmt.Errorf("repo %q is not owner/name", repo)
	}
	return owner, name, nil
}

// prsForBranch reads one page: newest first, 100 PRs is far past any branch's history.
// It matches same-repo heads only (head=owner:branch), unlike the native adapter's
// `gh pr list --head`, which also returns a fork PR of the same branch name; a fork
// PR is never ours to land, so shadow comparison should expect that difference.
func prsForBranch(t forge.Target, branch string) ([]forge.PR, error) {
	owner, _, err := splitRepo(t.Repo)
	if err != nil {
		return nil, err
	}
	var raw []rawPR
	path := "/repos/" + t.Repo + "/pulls?head=" + url.QueryEscape(owner+":"+branch) +
		"&state=all&sort=created&direction=desc&per_page=" + strconv.Itoa(pageSize)
	if err := call("GET", t.API, path, nil, &raw); err != nil {
		return nil, err
	}
	prs := make([]forge.PR, 0, len(raw))
	for _, r := range raw {
		prs = append(prs, r.pr())
	}
	return prs, nil
}

func pr(t forge.Target, number string) (forge.PR, error) {
	if _, err := parseNumber(number); err != nil {
		return forge.PR{}, err
	}
	var raw rawPR
	if err := call("GET", t.API, "/repos/"+t.Repo+"/pulls/"+number, nil, &raw); err != nil {
		return forge.PR{}, err
	}
	return raw.pr(), nil
}

type rawContext struct {
	Typename    string  `json:"__typename"`
	Name        string  `json:"name"`
	Conclusion  string  `json:"conclusion"`
	Summary     *string `json:"summary"`
	DetailsURL  *string `json:"detailsUrl"`
	Context     string  `json:"context"`
	State       string  `json:"state"`
	Description *string `json:"description"`
}

func (c rawContext) check(repo string) forge.Check {
	switch c.Typename {
	case "CheckRun":
		return forge.Check{Kind: c.Typename, Name: c.Name, Conclusion: c.Conclusion, Summary: deref(c.Summary), JobID: jobID(repo, deref(c.DetailsURL))}
	case "StatusContext":
		return forge.Check{Kind: c.Typename, Name: c.Context, Conclusion: c.State, Summary: deref(c.Description)}
	}
	// Core decides what an unknown kind means; pass it through by kind alone.
	return forge.Check{Kind: c.Typename}
}

func checks(t forge.Target, number string) (forge.Rollup, error) {
	n, err := parseNumber(number)
	if err != nil {
		return forge.Rollup{}, err
	}
	owner, name, err := splitRepo(t.Repo)
	if err != nil {
		return forge.Rollup{}, err
	}
	body, err := json.Marshal(map[string]any{
		"query":     rollupQuery,
		"variables": map[string]any{"owner": owner, "repo": name, "number": n},
	})
	if err != nil {
		return forge.Rollup{}, err
	}
	var resp struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
		Data struct {
			Repository *struct {
				PullRequest *struct {
					Commits struct {
						Nodes []struct {
							Commit struct {
								StatusCheckRollup *struct {
									State    string `json:"state"`
									Contexts struct {
										Nodes []rawContext `json:"nodes"`
									} `json:"contexts"`
								} `json:"statusCheckRollup"`
							} `json:"commit"`
						} `json:"nodes"`
					} `json:"commits"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := call("POST", t.API, "/graphql", body, &resp); err != nil {
		return forge.Rollup{}, err
	}
	if len(resp.Errors) > 0 {
		return forge.Rollup{}, fmt.Errorf("POST /graphql: %s", resp.Errors[0].Message)
	}
	repo := resp.Data.Repository
	if repo == nil || repo.PullRequest == nil {
		return forge.Rollup{}, fmt.Errorf("POST /graphql: no pull request %s#%s", t.Repo, number)
	}
	out := forge.Rollup{Checks: []forge.Check{}}
	nodes := repo.PullRequest.Commits.Nodes
	if len(nodes) == 0 || nodes[0].Commit.StatusCheckRollup == nil {
		return out, nil
	}
	roll := nodes[0].Commit.StatusCheckRollup
	out.State = roll.State
	for _, c := range roll.Contexts.Nodes {
		out.Checks = append(out.Checks, c.check(t.Repo))
	}
	return out, nil
}

// jobPathRE matches the path of a workflow job's web page, the detailsUrl of a
// GitHub Actions CheckRun. Any other CheckRun (a third-party App's) has no job
// log to read.
var jobPathRE = regexp.MustCompile(`^/([^/]+/[^/]+)/actions/runs/\d+/job/(\d+)/?$`)

// jobID is the job a detailsUrl names, or "" unless it is a job of repo: the
// log read is authorised for the target repo alone, so another repo's job URL
// is not followed.
func jobID(repo, detailsURL string) string {
	u, err := url.Parse(detailsURL)
	if err != nil {
		return ""
	}
	if m := jobPathRE.FindStringSubmatch(u.Path); m != nil && strings.EqualFold(m[1], repo) {
		return m[2]
	}
	return ""
}

// jobLog reads a job's log. The route answers 302 to a blob the host follows; a
// log too large to return whole comes back as its tail, which is what core
// excerpts. A failed read is an error the caller drops.
func jobLog(t forge.Target, id string) (string, error) {
	if _, err := strconv.ParseUint(id, 10, 64); err != nil {
		return "", fmt.Errorf("job id %q is not a number", id)
	}
	if _, _, err := splitRepo(t.Repo); err != nil {
		return "", err
	}
	b, err := fetch("GET", t.API, "/repos/"+t.Repo+"/actions/jobs/"+id+"/logs", nil)
	return string(b), err
}
