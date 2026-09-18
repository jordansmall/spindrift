package waves

import (
	"fmt"
	"sync"

	"spindrift.dev/launcher/internal/forge"
)

// depsOfConcurrency bounds how many DepsOf calls NewReadiness has in flight at
// once (#1745). It is a small fixed pool, not cfg.MaxParallel, because this
// fans out per-issue lookups within one wave, not whole-dispatch parallelism.
const depsOfConcurrency = 8

// Sources maps an issue number to the source DepsOf resolved each of its
// blockers from, keyed like the edges map a Readiness carries. Source as data
// is what lets Jira (always native) and the local tracker (always body) render
// without display-layer special cases. No current adapter mixes sources within
// one issue; the per-blocker keying allows for an adapter that someday might.
type Sources map[string]map[string]forge.DepSource

// Readiness answers "may this issue dispatch now, and if not, why" ahead of a
// Plan (#1547): the dependency graph for a batch of issues, the source each
// blocker ref was resolved from, and the issues whose own DepsOf call failed.
type Readiness struct {
	Edges   map[string][]string
	Sources Sources
	Failed  map[string]bool
}

// NewReadiness resolves the dependency graph for a batch of issues by calling
// DepsOf for each. A per-issue error is non-fatal but names the issue in Failed
// so a caller can tell a transient DepsOf failure from a confirmed zero-blocker
// issue (#752); Edges alone cannot, because both omit the issue's key.
func NewReadiness(it forge.IssueTracker, issues []Issue) (Readiness, error) {
	type depsResult struct {
		deps []forge.Dependency
		err  error
	}
	results := make([]depsResult, len(issues))

	limiter := NewLimiter(depsOfConcurrency)
	var wg sync.WaitGroup
	for i, iss := range issues {
		wg.Add(1)
		go func() {
			defer wg.Done()
			limiter.Acquire()
			defer limiter.Release()
			deps, depsErr := it.DepsOf(iss.Number)
			results[i] = depsResult{deps: deps, err: depsErr}
		}()
	}
	wg.Wait()

	edges := map[string][]string{}
	sources := Sources{}
	failed := map[string]bool{}
	for i, iss := range issues {
		deps, depsErr := results[i].deps, results[i].err
		if depsErr != nil {
			failed[iss.Number] = true
			continue
		}
		if len(deps) == 0 {
			continue
		}
		ids := make([]string, len(deps))
		srcs := make(map[string]forge.DepSource, len(deps))
		for j, d := range deps {
			ids[j] = d.ID
			srcs[d.ID] = d.Source
		}
		edges[iss.Number] = ids
		sources[iss.Number] = srcs
	}
	return Readiness{Edges: edges, Sources: sources, Failed: failed}, nil
}

// Status reports num's blocker readiness against r.Edges without transitioning
// tracker state: the Console (#650) and the engine hold a pick rather than
// cascade the dependent to Failed (#1984). failed scans every r.Edges[num]
// entry, since a closed blocker counts as satisfied yet can still carry
// cfg.FailedLabel; failed drives Reason, unready drives BlockedBy (#755).
func (r Readiness) Status(cfg Config, it forge.IssueTracker, cf forge.CodeForge, caps forge.Capabilities, num string) (ready bool, failed, unready []string) {
	return blockerStatus(cfg, it, cf, caps, num, r.Edges)
}

// Ready reports whether a single blocker ref is satisfied. The ref need not be
// one of r's Edges entries: the selective dispatch path checks blockers outside
// the batch r was resolved from. A nil caps.PRForge falls straight to the
// issue-closed check; a zero scope (no parent) skips the containment check, so
// an IntegrationRef-landed blocker stays unready until it closes (#2130).
func (r Readiness) Ready(it forge.IssueTracker, cf forge.CodeForge, caps forge.Capabilities, dep string, scope forge.SeedScope) bool {
	ready, _ := blockerReady(it, cf, caps, dep, scope)
	return ready
}

// detectCycle runs Kahn's algorithm over the in-batch portion of the graph. An
// edge counts only when both endpoints appear in nums, so a blocker outside the
// batch cannot register as a cycle.
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

// blockerReady is Readiness.Ready's logic, plus the forge.Issue it fetched. fi
// is nil when a merged-PR lookup settled readiness without calling it.Issue, so
// blockerStatus can tell "no fetch happened" from "fetched and still open".
// Both optional handles come from caps, not a type assertion on cf (#2946); a
// zero scope (no parent) keeps an IntegrationRef-landed blocker unready (#2130).
func blockerReady(it forge.IssueTracker, cf forge.CodeForge, caps forge.Capabilities, dep string, scope forge.SeedScope) (ready bool, fi *forge.Issue) {
	if pr := caps.PRForge; pr != nil {
		branch := cf.AgentBranch(dep)
		prURL, found, err := pr.PRForBranch(branch)
		if err == nil && found {
			state, stateErr := pr.PRState(prURL)
			if stateErr == nil {
				return state == forge.PRMerged, nil
			}
			return false, nil
		}
	}
	issue, err := it.Issue(dep)
	if err != nil {
		fmt.Printf("    .. blocker #%s could not be fetched: %v; holding\n", dep, err)
		return false, nil
	}
	switch issue.State {
	case forge.IssueClosed:
		fmt.Printf("    .. blocker #%s is closed (no discoverable PR); treating as satisfied\n", dep)
		return true, &issue
	case forge.IssueMerged:
		fmt.Printf("    .. blocker #%s is a merged PR (no discoverable agent branch); treating as satisfied\n", dep)
		return true, &issue
	}
	if issue.Landing != "" {
		if landing, perr := forge.ParseLanding(issue.Landing); perr == nil && landing.Kind == forge.LandingIntegrationRef {
			if q := caps.LandingContainmentQuery; q != nil && scope.Parent() != "" {
				contained, cerr := q.LandingContained(landing, scope)
				if cerr != nil {
					fmt.Printf("    .. blocker #%s seed-branch containment check failed: %v; holding\n", dep, cerr)
				} else if contained {
					fmt.Printf("    .. blocker #%s landing present on %s (this seam's own integration branch); treating as satisfied\n", dep, scope)
					return true, &issue
				} else {
					fmt.Printf("    .. blocker #%s landed but not yet on %s (this seam's own integration branch); holding\n", dep, scope)
				}
			}
		}
	}
	return false, &issue
}

func containsLabel(labels []string, target string) bool {
	for _, l := range labels {
		if l == target {
			return true
		}
	}
	return false
}

// unreadyBlockers returns num's declared blockers that are not yet satisfied,
// in edge order. A nil scopeOf yields a zero scope, which skips the seed-branch
// containment check, so an IntegrationRef-landed blocker stays unready (#2130).
func unreadyBlockers(it forge.IssueTracker, cf forge.CodeForge, caps forge.Capabilities, num string, edges map[string][]string, scopeOf func(num string) forge.SeedScope) []string {
	var scope forge.SeedScope
	if scopeOf != nil {
		scope = scopeOf(num)
	}
	var out []string
	for _, dep := range edges[num] {
		if ready, _ := blockerReady(it, cf, caps, dep, scope); !ready {
			out = append(out, dep)
		}
	}
	return out
}

// blockerStatus is Readiness.Status's logic against an arbitrary edges map, so
// the engine's internal callers (drainMaxJobs, nextReady) can reuse it against
// a Plan's edges without a Readiness value.
func blockerStatus(cfg Config, it forge.IssueTracker, cf forge.CodeForge, caps forge.Capabilities, num string, edges map[string][]string) (ready bool, failed, unready []string) {
	var scope forge.SeedScope
	if cfg.SeedScopeOf != nil {
		scope = cfg.SeedScopeOf(num)
	}
	for _, dep := range edges[num] {
		depReady, fi := blockerReady(it, cf, caps, dep, scope)
		if !depReady {
			unready = append(unready, dep)
		}
		if fi == nil {
			issue, err := it.Issue(dep)
			if err != nil {
				fmt.Printf("    .. blocker #%s could not be fetched: %v; skipping failed-label check\n", dep, err)
				continue
			}
			fi = &issue
		}
		if containsLabel(fi.Labels, cfg.FailedLabel) {
			failed = append(failed, dep)
		}
	}
	return len(unready) == 0 && len(failed) == 0, failed, unready
}
