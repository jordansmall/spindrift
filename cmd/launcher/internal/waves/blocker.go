package waves

import (
	"fmt"

	"spindrift.dev/launcher/internal/forge"
)

// Sources maps an issue number to the source (native relationship vs
// body-text parsing) DepsOf resolved each of its blockers from, mirroring
// the keys of the edges map BuildEdges returns alongside it. Carrying source
// as data keyed off the same edges — rather than switching on adapter type
// at render time — is what lets Jira (always native) and the local tracker
// (always body) render correctly without display-layer special cases.
type Sources map[string]map[string]forge.DepSource

// BuildEdges returns the dependency graph for the given batch of issues by
// calling the IssueTracker's DepsOf for each, plus the source each blocker
// ref was resolved from. Non-fatal per-issue errors are skipped, matching
// the original best-effort behaviour. Callers pass the edges result as
// Input.Edges and the sources result as Input.Sources to NewPlan.
func BuildEdges(it forge.IssueTracker, issues []Issue) (map[string][]string, Sources, error) {
	edges := map[string][]string{}
	sources := Sources{}
	for _, iss := range issues {
		deps, err := it.DepsOf(iss.Number)
		if err != nil {
			// Non-fatal: skip issues whose data cannot be fetched.
			continue
		}
		if len(deps) == 0 {
			continue
		}
		ids := make([]string, len(deps))
		srcs := make(map[string]forge.DepSource, len(deps))
		for i, d := range deps {
			ids[i] = d.ID
			srcs[d.ID] = d.Source
		}
		edges[iss.Number] = ids
		sources[iss.Number] = srcs
	}
	return edges, sources, nil
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
// path's external-blocker eviction pass. cf's PR surface is optional: a
// push-only Code Forge (no PRForge) has no PR to discover, so readiness
// falls straight to the issue-closed check.
func BlockerReady(it forge.IssueTracker, cf forge.CodeForge, dep string) bool {
	if pr, ok := cf.(forge.PRForge); ok {
		branch := cf.AgentBranch(dep)
		prURL, found, err := pr.PRForBranch(branch)
		if err == nil && found {
			state, stateErr := pr.PRState(prURL)
			if stateErr == nil {
				return state == forge.PRMerged
			}
			return false
		}
	}
	fi, err := it.Issue(dep)
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
func issueIsReady(it forge.IssueTracker, cf forge.CodeForge, num string, edges map[string][]string) bool {
	return len(unreadyBlockers(it, cf, num, edges)) == 0
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
func hasFailedInBatchBlocker(cfg Config, it forge.IssueTracker, num string, edges map[string][]string) bool {
	for _, dep := range edges[num] {
		fi, err := it.Issue(dep)
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
func unreadyBlockers(it forge.IssueTracker, cf forge.CodeForge, num string, edges map[string][]string) []string {
	var out []string
	for _, dep := range edges[num] {
		if !BlockerReady(it, cf, dep) {
			out = append(out, dep)
		}
	}
	return out
}
