package forge

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

const issueQueryLimit = 100

func (e *execClient) ListIssues(state DispatchState) ([]Issue, error) {
	label := e.labels.Label(state)
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
		State:  IssueState(raw.State),
	}
	for _, l := range raw.Labels {
		iss.Labels = append(iss.Labels, l.Name)
	}
	return iss, nil
}

// TransitionState swaps the from-state label for the to-state label on issue
// num. It emits exactly one --add-label and one --remove-label, matching the
// prior SwapLabel(add, remove) call contract with typed state identifiers.
func (e *execClient) TransitionState(num string, from, to DispatchState) error {
	add := e.labels.Label(to)
	args := []string{"issue", "edit", num, "--repo", e.repo, "--add-label", add}
	if remove := e.labels.Label(from); remove != "" {
		args = append(args, "--remove-label", remove)
	}
	cmd := exec.Command("gh", args...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh issue edit %s: %w", num, err)
	}
	return nil
}

// DepsOf returns the canonical dependency IDs for issue num by fetching its
// body and parsing GitHub-format blocker references.
func (e *execClient) DepsOf(num string) ([]string, error) {
	iss, err := e.Issue(num)
	if err != nil {
		return nil, err
	}
	return ParseBlockerRefs(iss.Body), nil
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
