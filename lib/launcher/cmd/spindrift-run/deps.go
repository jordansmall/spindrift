package main

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// parseBlockers extracts issue numbers referenced as blockers from a body.
//
// Two formats are recognised:
//
//  1. Inline (anywhere in the body):
//     "depends on #N" or "blocked by #N" (case-insensitive, optional colon)
//
//  2. Section header + list items — the format produced by the /to-issues
//     template, which the original shell regex missed entirely:
//
//     ## Blocked by
//     - #N (optional description)
//
//     The section continues until the next markdown heading or end-of-body.
//     "## Depends on" is treated symmetrically.
func parseBlockers(body string) []int {
	var blockers []int

	inlineRe := regexp.MustCompile(`(?i)(?:depends\s+on|blocked\s+by):?\s*#(\d+)`)
	headerRe := regexp.MustCompile(`(?i)^#+\s+(?:blocked\s+by|depends\s+on)\s*$`)
	sectionRe := regexp.MustCompile(`^#+\s`)
	listItemRe := regexp.MustCompile(`^\s*[-*]\s+#(\d+)`)

	inSection := false

	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimRight(raw, "\r")

		if headerRe.MatchString(line) {
			inSection = true
			continue
		}

		if inSection {
			if sectionRe.MatchString(line) {
				// A new heading ends the blocked-by section; fall through to
				// check for inline patterns on this line too.
				inSection = false
			} else if m := listItemRe.FindStringSubmatch(line); m != nil {
				if n, err := strconv.Atoi(m[1]); err == nil {
					blockers = append(blockers, n)
				}
				continue
			} else if strings.TrimSpace(line) == "" {
				continue
			} else {
				// Non-empty, non-list line inside the section — fall through to
				// the inline scan so embedded "depends on #N" prose is caught.
			}
		}

		// Inline scan on every line (including section-boundary lines).
		for _, m := range inlineRe.FindAllStringSubmatch(line, -1) {
			if n, err := strconv.Atoi(m[1]); err == nil {
				blockers = append(blockers, n)
			}
		}
	}

	return blockers
}

// buildDepGraph fetches each issue body, parses blockers, and returns a map
// of issueNumber → []blockerNumbers.  Returns an error if a cycle is found.
func buildDepGraph(cfg *Config, issues []Issue) (map[int][]int, error) {
	deps := make(map[int][]int)

	for _, iss := range issues {
		body, err := fetchIssueBody(cfg, iss.Number)
		if err != nil || body == "" {
			continue
		}
		bs := parseBlockers(body)
		if len(bs) > 0 {
			deps[iss.Number] = bs
		}
	}

	if cycle := detectCycle(deps, issueNums(issues)); cycle != 0 {
		return nil, fmt.Errorf("ERROR: dependency cycle detected (issue #%d is in the cycle)", cycle)
	}

	return deps, nil
}

// fetchIssueBody retrieves the body text of a single issue via gh.
func fetchIssueBody(cfg *Config, num int) (string, error) {
	out, err := exec.Command("gh", "issue", "view",
		strconv.Itoa(num),
		"--repo", cfg.Repo,
		"--json", "body",
		"--jq", ".body",
	).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// detectCycle runs Kahn's topological-sort on the intra-batch subgraph.
// Returns a cycle-member issue number (> 0) when a cycle exists; 0 otherwise.
// Only edges where both endpoints are in `nodes` are considered.
func detectCycle(deps map[int][]int, nodes []int) int {
	batch := make(map[int]bool, len(nodes))
	for _, n := range nodes {
		batch[n] = true
	}

	indegree := make(map[int]int)
	adj := make(map[int][]int) // blocker → []dependents

	for child, blockers := range deps {
		if !batch[child] {
			continue
		}
		for _, b := range blockers {
			if !batch[b] {
				continue
			}
			indegree[child]++
			adj[b] = append(adj[b], child)
			if _, ok := indegree[b]; !ok {
				indegree[b] = 0
			}
		}
	}
	for _, n := range nodes {
		if _, ok := indegree[n]; !ok {
			indegree[n] = 0
		}
	}

	queue := []int{}
	for _, n := range nodes {
		if indegree[n] == 0 {
			queue = append(queue, n)
		}
	}

	done := 0
	for len(queue) > 0 {
		node := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		done++
		for _, child := range adj[node] {
			indegree[child]--
			if indegree[child] == 0 {
				queue = append(queue, child)
			}
		}
	}

	if done < len(nodes) {
		for _, n := range nodes {
			if indegree[n] > 0 {
				return n
			}
		}
	}
	return 0
}

// hasCompleteLabel returns true if issue num carries cfg.CompleteLabel on GitHub.
func hasCompleteLabel(cfg *Config, num int) bool {
	out, err := exec.Command("gh", "issue", "view",
		strconv.Itoa(num),
		"--repo", cfg.Repo,
		"--json", "labels",
		"--jq", ".labels[].name",
	).Output()
	if err != nil {
		return false
	}
	for _, label := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if label == cfg.CompleteLabel {
			return true
		}
	}
	return false
}

// isBlocked returns true if any intra-batch blocker of num has not yet reached
// cfg.CompleteLabel on GitHub.
func isBlocked(cfg *Config, num int, deps map[int][]int) bool {
	blockers := deps[num]
	if len(blockers) == 0 {
		return false
	}
	for _, b := range blockers {
		if !hasCompleteLabel(cfg, b) {
			return true
		}
	}
	return false
}
