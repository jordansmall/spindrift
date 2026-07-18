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
//
// The inner map is keyed per-blocker, but no current adapter ever mixes
// sources within one issue: DepsOf resolves and tags a whole issue's
// blockers in a single call via forge.WithSource (see the GitHub adapter's
// DepsOf, which tries the native API first and only falls back to body
// parsing for the entire issue if that errors or is empty), so every entry
// under a given issue number is guaranteed to share one DepSource. The
// per-blocker keying is future-proofing for an adapter that could someday
// resolve a mix of native and body-sourced blockers for the same issue —
// it is not a reflection of current behaviour.
type Sources map[string]map[string]forge.DepSource

// BuildEdges returns the dependency graph for the given batch of issues by
// calling the IssueTracker's DepsOf for each, plus the source each blocker
// ref was resolved from. Non-fatal per-issue errors are skipped, matching
// the original best-effort behaviour, but named in the returned failed set
// so a caller can tell a transient DepsOf failure apart from a confirmed
// zero-blocker issue (#752) — the two look identical in edges alone, since
// both simply omit the issue's key. Callers pass the edges result as
// Input.Edges and the sources result as Input.Sources to NewPlan.
func BuildEdges(it forge.IssueTracker, issues []Issue) (edges map[string][]string, sources Sources, failed map[string]bool, err error) {
	edges = map[string][]string{}
	sources = Sources{}
	failed = map[string]bool{}
	for _, iss := range issues {
		deps, depsErr := it.DepsOf(iss.Number)
		if depsErr != nil {
			// Non-fatal: skip issues whose data cannot be fetched.
			failed[iss.Number] = true
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
	return edges, sources, failed, nil
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

// containsLabel reports whether labels contains target.
func containsLabel(labels []string, target string) bool {
	for _, l := range labels {
		if l == target {
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

// BlockerStatus reports num's blocker readiness against edges without
// transitioning any tracker state — the seam the Console (#650) reuses to
// hold a pick rather than the headless engine's own cascade-to-Failed (which
// moves the dependent issue itself to Failed when a blocker's failed set is
// non-empty). ready is true when every declared blocker is satisfied
// (BlockerReady) and none carries cfg.FailedLabel; unready names every
// blocker not yet satisfied, in edge order. failed scans all of edges[num],
// not just unready — a blocker can be closed (so BlockerReady's fallback
// calls it satisfied) and still carry cfg.FailedLabel, which must never be
// satisfiable regardless of readiness.
// failed is reported separately from unready rather than folded into it:
// unready drives the console's BlockedBy badge and failed drives Reason
// (queue.go's setHeld), and collapsing the two would reintroduce the
// redundant rendering #755 removes.
func BlockerStatus(cfg Config, it forge.IssueTracker, cf forge.CodeForge, num string, edges map[string][]string) (ready bool, failed, unready []string) {
	unready = unreadyBlockers(it, cf, num, edges)
	for _, dep := range edges[num] {
		fi, err := it.Issue(dep)
		if err != nil {
			continue
		}
		if containsLabel(fi.Labels, cfg.FailedLabel) {
			failed = append(failed, dep)
		}
	}
	return len(unready) == 0 && len(failed) == 0, failed, unready
}
