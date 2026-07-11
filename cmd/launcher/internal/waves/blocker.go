package waves

import (
	"fmt"

	"spindrift.dev/launcher/internal/forge"
)

// BuildEdges returns the dependency graph for the given batch of issues by
// calling the IssueTracker's DepsOf for each. Non-fatal per-issue errors are
// skipped, matching the original best-effort behaviour. Callers pass the
// result as Input.Edges to NewPlan.
func BuildEdges(fc forge.Client, issues []Issue) (map[string][]string, error) {
	edges := map[string][]string{}
	for _, iss := range issues {
		deps, err := fc.DepsOf(iss.Number)
		if err != nil {
			// Non-fatal: skip issues whose data cannot be fetched.
			continue
		}
		if len(deps) > 0 {
			edges[iss.Number] = deps
		}
	}
	return edges, nil
}

// detectCycle runs Kahn's algorithm on the in-batch portion of the dependency
// graph. Only edges where both endpoints appear in nums are considered; external
// blockers (not in the batch) are ignored. Returns a cycle-member issue number
// and true when a cycle exists; returns "" and false for an acyclic graph.
func detectCycle(edges map[string][]string, nums []string) (string, bool) {
	inBatch := make(map[string]bool, len(nums))
	for _, n := range nums {
		inBatch[n] = true
	}

	indegree := make(map[string]int, len(nums))
	adj := map[string][]string{}
	for _, n := range nums {
		indegree[n] = 0
	}
	for child, blockers := range edges {
		if !inBatch[child] {
			continue
		}
		for _, blocker := range blockers {
			if !inBatch[blocker] {
				continue
			}
			indegree[child]++
			adj[blocker] = append(adj[blocker], child)
		}
	}

	queue := make([]string, 0, len(nums))
	for _, n := range nums {
		if indegree[n] == 0 {
			queue = append(queue, n)
		}
	}
	done := 0
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		done++
		for _, dep := range adj[node] {
			indegree[dep]--
			if indegree[dep] == 0 {
				queue = append(queue, dep)
			}
		}
	}
	if done < len(nums) {
		for _, n := range nums {
			if indegree[n] > 0 {
				return n, true
			}
		}
	}
	return "", false
}

// BlockerReady returns true when the blocker's PR is merged, or when the
// blocker issue is closed with no discoverable PR (human-handled work).
// Exported for callers outside the wave engine that need a single-blocker
// readiness check ahead of a Plan — e.g. the selective `dispatch <nums>`
// path's external-blocker eviction pass.
func BlockerReady(fc forge.Client, dep string) bool {
	branch := fc.AgentBranch(dep)
	prURL, ok, err := fc.PRForBranch(branch)
	if err == nil && ok {
		state, stateErr := fc.PRState(prURL)
		if stateErr == nil {
			return state == forge.PRMerged
		}
		return false
	}
	fi, err := fc.Issue(dep)
	if err != nil {
		return false
	}
	if fi.State == forge.IssueClosed {
		fmt.Printf("    .. blocker #%s is closed (no discoverable PR); treating as satisfied\n", dep)
		return true
	}
	return false
}

// issueIsReady returns true when all of num's declared blockers are ready.
func issueIsReady(fc forge.Client, num string, edges map[string][]string) bool {
	return len(unreadyBlockers(fc, num, edges)) == 0
}

// containsLabel reports whether labels contains target.
func containsLabel(labels []string, target string) bool {
	for _, l := range labels {
		if l == target {
			return true
		}
	}
	return false
}

// hasFailedInBatchBlocker returns true when any of num's in-batch declared
// blockers carry failedLabel, meaning the dependent can never proceed.
func hasFailedInBatchBlocker(cfg Config, fc forge.Client, num string, edges map[string][]string) bool {
	for _, dep := range edges[num] {
		fi, err := fc.Issue(dep)
		if err != nil {
			continue
		}
		if containsLabel(fi.Labels, cfg.FailedLabel) {
			return true
		}
	}
	return false
}

// unreadyBlockers returns num's declared blockers that are not yet satisfied,
// in edge order. Empty means the issue is ready to dispatch.
func unreadyBlockers(fc forge.Client, num string, edges map[string][]string) []string {
	var out []string
	for _, dep := range edges[num] {
		if !BlockerReady(fc, dep) {
			out = append(out, dep)
		}
	}
	return out
}
