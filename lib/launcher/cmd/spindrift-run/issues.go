package main

import (
	"bufio"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Issue is a single open ready-for-agent issue.
type Issue struct {
	Number int
	Title  string
}

// queryIssues fetches open issues with cfg.Label, sorted oldest-first.
func queryIssues(cfg *Config) ([]Issue, error) {
	fmt.Printf("==> querying open '%s' issues in %s\n", cfg.Label, cfg.Repo)

	out, err := exec.Command("gh", "issue", "list",
		"--repo", cfg.Repo,
		"--state", "open",
		"--label", cfg.Label,
		"--limit", "100",
		"--json", "number,title",
		"--jq", "sort_by(.number) | .[] | [.number, .title] | @tsv",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("querying issues: %w", err)
	}

	var issues []Issue
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		num, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		issues = append(issues, Issue{Number: num, Title: parts[1]})
	}
	return issues, nil
}

// issueNums returns the issue numbers from a slice of issues.
func issueNums(issues []Issue) []int {
	nums := make([]int, len(issues))
	for i, iss := range issues {
		nums[i] = iss.Number
	}
	return nums
}
