package forge

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

func (e *execClient) ListIssues(state DispatchState) ([]Issue, error) {
	label := e.labels.Label(state)
	cmd := exec.Command("gh", "issue", "list",
		"--repo", e.repo,
		"--state", "open",
		"--label", label,
		"--limit", strconv.Itoa(resultPageLimit),
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
	warnPageMayTruncateBacklog("gh issue list", len(issues))
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

// DepsOf returns the canonical dependencies for issue num, preferring
// GitHub's native issue-dependencies API and falling back to body-text
// parsing (inline refs / "## Blocked by" section) when the native lookup
// errors or yields no relationships.
func (e *execClient) DepsOf(num string) ([]Dependency, error) {
	deps, err := e.nativeDepsOf(num)
	if err == nil && len(deps) > 0 {
		return WithSource(deps, DepSourceNative), nil
	}
	if err != nil {
		fmt.Printf("WARNING: native dependency lookup for issue %s failed (%v); falling back to body parsing\n", num, err)
	}
	iss, err := e.Issue(num)
	if err != nil {
		return nil, err
	}
	return WithSource(ParseBlockerRefs(iss.Body), DepSourceBody), nil
}

// nativeDepsOf queries GitHub's issue-dependencies API for the issues that
// block num.
func (e *execClient) nativeDepsOf(num string) ([]string, error) {
	cmd := exec.Command("gh", "api",
		fmt.Sprintf("repos/%s/issues/%s/dependencies/blocked_by", e.repo, num),
		"--jq", ".[].number",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh api dependencies/blocked_by %s: %w", num, err)
	}
	var deps []string
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			deps = append(deps, line)
		}
	}
	return deps, nil
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
