package main

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"spindrift.dev/sdk/forge"
)

// rollupQuery reads checks through the GraphQL rollup: REST check-runs
// answer 403 under a fine-grained PAT, the rollup does not.
const rollupQuery = `query($owner:String!,$repo:String!,$number:Int!){repository(owner:$owner,name:$repo){pullRequest(number:$number){url commits(last:1){nodes{commit{statusCheckRollup{state contexts(first:50){nodes{__typename ... on CheckRun{name conclusion status summary detailsUrl} ... on StatusContext{context state description}}}}}}}}}}`

type rawPR struct {
	Number   int     `json:"number"`
	URL      string  `json:"html_url"`
	State    string  `json:"state"`
	MergedAt *string `json:"merged_at"`
	Draft    bool    `json:"draft"`
	// NodeID is the GraphQL id the draft-flip mutations take.
	NodeID string `json:"node_id"`
	Head   struct {
		Ref  string     `json:"ref"`
		SHA  string     `json:"sha"`
		Repo *rawPRRepo `json:"repo"`
	} `json:"head"`
	// Base.Ref is the branch the PR targets.
	Base struct {
		Ref  string     `json:"ref"`
		Repo *rawPRRepo `json:"repo"`
	} `json:"base"`
	// Mergeable is null while GitHub is still computing it.
	Mergeable      *bool  `json:"mergeable"`
	MergeCommitSHA string `json:"merge_commit_sha"`
}

type rawPRRepo struct {
	FullName string `json:"full_name"`
}

// crossRepo reports a head in another repository. A null head.repo is a
// deleted fork; with no base.repo to compare, nothing is flagged.
func (r rawPR) crossRepo() bool {
	if r.Base.Repo == nil {
		return false
	}
	return r.Head.Repo == nil || !strings.EqualFold(r.Head.Repo.FullName, r.Base.Repo.FullName)
}

func (r rawPR) pr() forge.PR {
	p := forge.PR{
		Number:    strconv.Itoa(r.Number),
		URL:       r.URL,
		State:     r.State,
		Merged:    r.MergedAt != nil,
		Draft:     r.Draft,
		Head:      r.Head.Ref,
		HeadSHA:   r.Head.SHA,
		CrossRepo: r.crossRepo(),
		Mergeable: "UNKNOWN",
	}
	if r.Mergeable != nil {
		p.Mergeable = "CONFLICTING"
		if *r.Mergeable {
			p.Mergeable = "MERGEABLE"
		}
	}
	// An open PR's merge_commit_sha is a test-merge commit, not a landing.
	if p.Merged {
		p.MergeCommit = r.MergeCommitSHA
	}
	return p
}

// forgePath is a REST path under the target repository; rest starts with "/".
func forgePath(t forge.Target, rest string) string { return "/repos/" + t.Repo + rest }

func parseNumber(number string) (int, error) {
	n, err := strconv.Atoi(number)
	if err != nil || n < 1 || strconv.Itoa(n) != number {
		return 0, fmt.Errorf("number %q is not a positive integer", number)
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
// It matches same-repo heads only (head=owner:branch): a fork PR of the same
// branch name is never ours to land.
func prsForBranch(t forge.Target, branch string) ([]forge.PR, error) {
	owner, _, err := splitRepo(t.Repo)
	if err != nil {
		return nil, err
	}
	var raw []rawPR
	path := forgePath(t, "/pulls?head="+url.QueryEscape(owner+":"+branch)+
		"&state=all&sort=created&direction=desc&per_page="+strconv.Itoa(pageSize))
	if _, err := send("GET", t.API, path, nil, &raw); err != nil {
		return nil, err
	}
	prs := make([]forge.PR, 0, len(raw))
	for _, r := range raw {
		prs = append(prs, r.pr())
	}
	return prs, nil
}

// readPR is the raw REST pull request, for the primitives that need its node
// id or refs rather than core's view of it.
func readPR(t forge.Target, number string) (rawPR, error) {
	var raw rawPR
	if _, err := parseNumber(number); err != nil {
		return raw, err
	}
	_, err := send("GET", t.API, forgePath(t, "/pulls/"+number), nil, &raw)
	return raw, err
}

func pr(t forge.Target, number string) (forge.PR, error) {
	raw, err := readPR(t, number)
	if err != nil {
		return forge.PR{}, err
	}
	return raw.pr(), nil
}

type rawContext struct {
	Typename    string  `json:"__typename"`
	Name        string  `json:"name"`
	Conclusion  string  `json:"conclusion"`
	Status      string  `json:"status"`
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

// rollup is the head commit's statusCheckRollup.
type rollup struct {
	State    string `json:"state"`
	Contexts struct {
		Nodes []rawContext `json:"nodes"`
	} `json:"contexts"`
}

// readRollup reads the PR's head-commit rollup, nil when it reports no checks,
// and the PR's web URL (reported either way).
func readRollup(t forge.Target, number string) (*rollup, string, error) {
	n, err := parseNumber(number)
	if err != nil {
		return nil, "", err
	}
	owner, name, err := splitRepo(t.Repo)
	if err != nil {
		return nil, "", err
	}
	var data struct {
		Repository *struct {
			PullRequest *struct {
				URL     string `json:"url"`
				Commits struct {
					Nodes []struct {
						Commit struct {
							StatusCheckRollup *rollup `json:"statusCheckRollup"`
						} `json:"commit"`
					} `json:"nodes"`
				} `json:"commits"`
			} `json:"pullRequest"`
		} `json:"repository"`
	}
	vars := map[string]any{"owner": owner, "repo": name, "number": n}
	if err := graphql(t.API, rollupQuery, vars, &data); err != nil {
		return nil, "", err
	}
	repo := data.Repository
	if repo == nil || repo.PullRequest == nil {
		return nil, "", fmt.Errorf("POST /graphql: no pull request %s#%s", t.Repo, number)
	}
	nodes := repo.PullRequest.Commits.Nodes
	if len(nodes) == 0 {
		return nil, repo.PullRequest.URL, nil
	}
	return nodes[0].Commit.StatusCheckRollup, repo.PullRequest.URL, nil
}

func checks(t forge.Target, number string) (forge.Rollup, error) {
	roll, _, err := readRollup(t, number)
	if err != nil {
		return forge.Rollup{}, err
	}
	out := forge.Rollup{Checks: []forge.Check{}}
	if roll == nil {
		return out, nil
	}
	out.State = roll.State
	for _, c := range roll.Contexts.Nodes {
		out.Checks = append(out.Checks, c.check(t.Repo))
	}
	return out, nil
}

// runURLRE matches a whole Actions run URL, with the /job/<id> segment a
// CheckRun's detailsUrl carries optional. It is anchored over the raw string,
// so a query, fragment, userinfo or upper-case scheme is refused rather than
// normalised away. Group 1 is the host, group 2 owner/repo, group 3 the run
// path.
var runURLRE = regexp.MustCompile(`^https://([^/]+)/([^/]+/[^/]+)(/actions/runs/\d+)(?:/job/\d+)?/?$`)

// runMatch is the runURLRE match for a CheckRun of the target repo on the PR's
// own host, nil for any other context: a third-party App's check, a run of
// another repo, or one on another host.
func runMatch(c rawContext, host, repo string) []string {
	if c.Typename != "CheckRun" {
		return nil
	}
	m := runURLRE.FindStringSubmatch(deref(c.DetailsURL))
	if m == nil || !strings.EqualFold(m[1], host) || !strings.EqualFold(m[2], repo) {
		return nil
	}
	return m
}

// hostOf is the host of a PR URL, or "" when it does not parse.
func hostOf(prURL string) string {
	if p, err := url.Parse(prURL); err == nil {
		return p.Host
	}
	return ""
}

// ciRun is the URL of the head commit's Actions run: the first run with a
// CheckRun still not COMPLETED, else the first run, so the link follows live
// work rather than a finished sibling workflow. Only runs of the target repo on
// the PR's own host count; "" when there is none, or when the PR reports no
// URL. The /job/<id> segment is dropped so the URL stays stable as a run's jobs
// register.
func ciRun(t forge.Target, number string) (string, error) {
	roll, prURL, err := readRollup(t, number)
	if err != nil || roll == nil {
		return "", err
	}
	host := hostOf(prURL)
	var first string
	for _, c := range roll.Contexts.Nodes {
		m := runMatch(c, host, t.Repo)
		if m == nil {
			continue
		}
		run := "https://" + m[1] + "/" + m[2] + m[3]
		if c.Status != "COMPLETED" {
			return run, nil
		}
		if first == "" {
			first = run
		}
	}
	return first, nil
}

// rerunConclusions are the CheckRun conclusions a re-run can cure.
// ACTION_REQUIRED is left out: it waits on an approval a re-run does not give.
var rerunConclusions = map[string]bool{
	"FAILURE": true, "TIMED_OUT": true, "CANCELLED": true, "STARTUP_FAILURE": true,
}

// rerunFailedCI re-runs the failed jobs of every Actions run behind a failed
// CheckRun on the head commit, once per run however many of its jobs failed.
// With no such run it errors, so core treats the attempt as not made. A failed
// POST does not stop the rest, and any one that succeeds counts as a re-run:
// it errors on a POST only when none succeeded, returning the first failure.
func rerunFailedCI(t forge.Target, number string) error {
	roll, prURL, err := readRollup(t, number)
	if err != nil {
		return err
	}
	var ids []string
	if roll != nil {
		host := hostOf(prURL)
		for _, c := range roll.Contexts.Nodes {
			m := runMatch(c, host, t.Repo)
			if m == nil || !rerunConclusions[c.Conclusion] {
				continue
			}
			if id := path.Base(m[3]); !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		return fmt.Errorf("no failed Actions run to re-run on the head commit of %s#%s", t.Repo, number)
	}
	var first error
	posted := false
	for _, id := range ids {
		if _, err := send("POST", t.API, forgePath(t, "/actions/runs/"+id+"/rerun-failed-jobs"), nil, nil); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		posted = true
	}
	if posted {
		return nil
	}
	return first
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
	resp, err := send("GET", t.API, forgePath(t, "/actions/jobs/"+id+"/logs"), nil, nil)
	if err != nil {
		return "", err
	}
	return string(resp.Body), nil
}
