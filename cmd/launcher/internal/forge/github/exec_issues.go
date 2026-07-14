package github

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"spindrift.dev/launcher/internal/forge"
)

func (e *execClient) ListIssues(state forge.DispatchState) ([]forge.Issue, error) {
	label := e.labels.Label(state)
	cmd := exec.Command("gh", "issue", "list",
		"--repo", e.repo,
		"--state", "open",
		"--label", label,
		"--limit", strconv.Itoa(forge.ResultPageLimit),
		"--search", "sort:created-asc",
		"--json", "number,title",
		"--jq", "sort_by(.number) | .[] | [.number, .title] | @tsv",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh issue list: %w", err)
	}
	var issues []forge.Issue
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
		issues = append(issues, forge.Issue{Number: parts[0], Title: parts[1]})
	}
	forge.WarnPageMayTruncateBacklog("gh issue list", len(issues))
	return issues, nil
}

// ListOpenIssues returns every open issue, in canonical order (ascending
// issue number), regardless of dispatch state — unlike ListIssues, which
// scopes to one dispatch state's label, this carries no --label filter, so
// untriaged issues (no dispatch label yet) are included too.
func (e *execClient) ListOpenIssues() ([]forge.Issue, error) {
	cmd := exec.Command("gh", "issue", "list",
		"--repo", e.repo,
		"--state", "open",
		"--limit", strconv.Itoa(forge.ResultPageLimit),
		"--search", "sort:created-asc",
		"--json", "number,title,labels",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh issue list: %w", err)
	}
	var raw []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse gh issue list: %w", err)
	}
	issues := make([]forge.Issue, len(raw))
	for i, r := range raw {
		iss := forge.Issue{Number: strconv.Itoa(r.Number), Title: r.Title}
		for _, l := range r.Labels {
			iss.Labels = append(iss.Labels, l.Name)
		}
		issues[i] = iss
	}
	sort.Slice(issues, func(i, j int) bool {
		ni, _ := strconv.Atoi(issues[i].Number)
		nj, _ := strconv.Atoi(issues[j].Number)
		return ni < nj
	})
	forge.WarnPageMayTruncateBacklog("gh issue list", len(issues))
	return issues, nil
}

func (e *execClient) Issue(num string) (forge.Issue, error) {
	cmd := exec.Command("gh", "issue", "view", num,
		"--repo", e.repo,
		"--json", "number,title,body,state,labels",
	)
	out, err := cmd.Output()
	if err != nil {
		return forge.Issue{}, fmt.Errorf("gh issue view %s: %w", num, err)
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
		return forge.Issue{}, fmt.Errorf("parse issue %s: %w", num, err)
	}
	iss := forge.Issue{
		Number: strconv.Itoa(raw.Number),
		Title:  raw.Title,
		Body:   raw.Body,
		State:  forge.IssueState(raw.State),
	}
	for _, l := range raw.Labels {
		iss.Labels = append(iss.Labels, l.Name)
	}
	return iss, nil
}

// TransitionState swaps the from-state label for the to-state label on issue
// num. It emits exactly one --add-label and one --remove-label, matching the
// prior SwapLabel(add, remove) call contract with typed state identifiers.
func (e *execClient) TransitionState(num string, from, to forge.DispatchState) error {
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

// CompleteVerdict swaps the InProgress label for verdict's terminal label on
// issue num, emitting exactly one --add-label and one --remove-label —
// TransitionState's contract, with the to-label resolved from verdictLabels
// instead of DispatchLabels.Complete.
func (e *execClient) CompleteVerdict(num string, verdict forge.Verdict) error {
	add := e.verdictLabels.Label(verdict)
	if add == "" {
		return fmt.Errorf("gh issue edit %s: no label configured for verdict %v", num, verdict)
	}
	args := []string{"issue", "edit", num, "--repo", e.repo, "--add-label", add}
	if remove := e.labels.Label(forge.InProgress); remove != "" {
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
func (e *execClient) DepsOf(num string) ([]forge.Dependency, error) {
	deps, err := e.nativeDepsOf(num)
	if err == nil && len(deps) > 0 {
		return forge.WithSource(deps, forge.DepSourceNative), nil
	}
	if err != nil {
		fmt.Printf("WARNING: native dependency lookup for issue %s failed (%v); falling back to body parsing\n", num, err)
	}
	iss, err := e.Issue(num)
	if err != nil {
		return nil, err
	}
	return forge.WithSource(forge.ParseBlockerRefs(iss.Body), forge.DepSourceBody), nil
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

// TouchesOf returns the declared touch-set parsed from issue num's body —
// the shared body-grammar default (forge.ParseTouchPaths); this adapter has
// no native touch-set concept to prefer over it.
func (e *execClient) TouchesOf(num string) ([]string, error) {
	iss, err := e.Issue(num)
	if err != nil {
		return nil, err
	}
	return forge.ParseTouchPaths(iss.Body), nil
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
