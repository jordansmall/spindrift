package forge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// execClient is the gh-exec adapter. It satisfies Client using the gh CLI.
// GH_TOKEN is read from the ambient environment; the repo slug is fixed at
// construction time.
type execClient struct {
	repo string // owner/repo slug
}

// NewExecClient returns a Client backed by the gh CLI for the given repo slug.
func NewExecClient(repo string) Client {
	return &execClient{repo: repo}
}

const issueQueryLimit = 100

func (e *execClient) ListIssues(label string) ([]Issue, error) {
	cmd := exec.Command("gh", "issue", "list",
		"--repo", e.repo,
		"--state", "open",
		"--label", label,
		"--limit", strconv.Itoa(issueQueryLimit),
		"--search", "sort:created-asc",
		"--json", "number,title",
		"--jq", "sort_by(.number) | .[] | [.number, .title] | @tsv",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh issue list: %w", err)
	}
	var issues []Issue
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) < 2 {
			continue
		}
		issues = append(issues, Issue{Number: parts[0], Title: parts[1]})
	}
	if len(issues) >= issueQueryLimit {
		fmt.Printf("WARNING: issue list returned %d issues (limit %d); backlog may be larger — rerun to drain\n",
			len(issues), issueQueryLimit)
	}
	return issues, nil
}

func (e *execClient) Issue(num string) (Issue, error) {
	cmd := exec.Command("gh", "issue", "view", num,
		"--repo", e.repo,
		"--json", "number,title,body,state,labels",
	)
	out, err := cmd.Output()
	if err != nil {
		return Issue{}, fmt.Errorf("gh issue view %s: %w", num, err)
	}
	var raw struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		State  string `json:"state"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return Issue{}, fmt.Errorf("parse issue %s: %w", num, err)
	}
	iss := Issue{
		Number: strconv.Itoa(raw.Number),
		Title:  raw.Title,
		Body:   raw.Body,
		State:  raw.State,
	}
	for _, l := range raw.Labels {
		iss.Labels = append(iss.Labels, l.Name)
	}
	return iss, nil
}

func (e *execClient) SwapLabel(num, add, remove string) error {
	cmd := exec.Command("gh", "issue", "edit", num,
		"--repo", e.repo,
		"--add-label", add,
		"--remove-label", remove,
	)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh issue edit %s: %w", num, err)
	}
	return nil
}

func (e *execClient) Comment(num, body string) error {
	cmd := exec.Command("gh", "issue", "comment", num,
		"--repo", e.repo,
		"--body", body,
	)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh issue comment %s: %w", num, err)
	}
	return nil
}

func (e *execClient) OpenPRForBranch(branch string) (PR, bool, error) {
	cmd := exec.Command("gh", "pr", "list",
		"--repo", e.repo,
		"--head", branch,
		"--state", "open",
		"--json", "url",
		"--jq", `.[0].url // ""`,
	)
	out, err := cmd.Output()
	if err != nil {
		return PR{}, false, fmt.Errorf("gh pr list: %w", err)
	}
	url := strings.TrimSpace(string(out))
	if url == "" {
		return PR{}, false, nil
	}
	viewCmd := exec.Command("gh", "pr", "view", url, "--json", "isDraft", "--jq", ".isDraft")
	out, err = viewCmd.Output()
	if err != nil {
		// Cannot determine draft status — do not adopt.
		return PR{}, false, fmt.Errorf("gh pr view %s isDraft: %w", url, err)
	}
	isDraft := strings.TrimSpace(string(out)) == "true"
	return PR{URL: url, IsDraft: isDraft}, true, nil
}

func (e *execClient) PRState(url string) (string, error) {
	cmd := exec.Command("gh", "pr", "view", url, "--json", "state", "--jq", ".state")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("gh pr view %s state: %w", url, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// CheckState queries the aggregate statusCheckRollup state of the PR's head
// commit via GraphQL and returns the result as a RollupState. Returns StateNone
// when no checks are registered or the rollup is absent.
func (e *execClient) CheckState(url string) (RollupState, error) {
	// Parse https://github.com/OWNER/REPO/pull/NUMBER
	parts := strings.Split(url, "/")
	if len(parts) < 7 {
		return StateNone, fmt.Errorf("invalid PR URL: %s", url)
	}
	owner, repo, number := parts[3], parts[4], parts[6]
	const gql = `query($owner:String!,$repo:String!,$number:Int!){repository(owner:$owner,name:$repo){pullRequest(number:$number){commits(last:1){nodes{commit{statusCheckRollup{state}}}}}}}`
	cmd := exec.Command("gh", "api", "graphql",
		"-f", "query="+gql,
		"-f", "owner="+owner,
		"-f", "repo="+repo,
		"-F", "number="+number,
		"--jq", `.data.repository.pullRequest.commits.nodes[0].commit.statusCheckRollup.state // ""`,
	)
	out, err := cmd.Output()
	if err != nil {
		return StateNone, fmt.Errorf("gh api graphql: %w", err)
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return StateNone, nil
	}
	return RollupState(s), nil
}

func (e *execClient) Merge(url string) error {
	var stderr bytes.Buffer
	cmd := exec.Command("gh", "pr", "merge", url, "--rebase", "--delete-branch")
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if isMergeConflict(stderr.String()) {
			return ErrMergeConflict
		}
		return fmt.Errorf("gh pr merge %s: %w", url, err)
	}
	return nil
}

// isMergeConflict returns true when gh's stderr indicates a merge-conflict
// failure rather than a permissions error, network failure, or other cause.
func isMergeConflict(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "merge conflict") ||
		strings.Contains(s, "not mergeable")
}

// Rebase checks out the PR's head branch into a temporary clone of the target
// repository, rebases it onto origin/<base>, and force-pushes the result.
// Returns ErrMergeConflict if the rebase cannot be completed automatically.
func (e *execClient) Rebase(prURL string) error {
	out, err := exec.Command("gh", "pr", "view", prURL,
		"--json", "headRefName,baseRefName",
		"--jq", "[.headRefName,.baseRefName]|@tsv",
	).Output()
	if err != nil {
		return fmt.Errorf("gh pr view %s: %w", prURL, err)
	}
	fields := strings.SplitN(strings.TrimSpace(string(out)), "\t", 2)
	if len(fields) != 2 {
		return fmt.Errorf("gh pr view: unexpected output %q", string(out))
	}
	head, base := fields[0], fields[1]

	dir, err := os.MkdirTemp("", "spindrift-rebase-*")
	if err != nil {
		return fmt.Errorf("mkdtemp: %w", err)
	}
	defer os.RemoveAll(dir)

	if err := exec.Command("gh", "repo", "clone", e.repo, dir,
		"--", "--no-single-branch").Run(); err != nil {
		return fmt.Errorf("gh repo clone: %w", err)
	}

	gitIn := func(args ...string) *exec.Cmd {
		return exec.Command("git", append([]string{"-C", dir}, args...)...)
	}

	if err := gitIn("checkout", head).Run(); err != nil {
		return fmt.Errorf("git checkout %s: %w", head, err)
	}
	if err := gitIn("rebase", "origin/"+base).Run(); err != nil {
		_ = gitIn("rebase", "--abort").Run()
		return ErrMergeConflict
	}
	if err := gitIn("push", "--force-with-lease").Run(); err != nil {
		return fmt.Errorf("git push --force-with-lease: %w", err)
	}
	return nil
}
