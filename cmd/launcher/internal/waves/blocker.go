package waves

import (
	"fmt"
	"sync"

	"spindrift.dev/launcher/internal/forge"
)

// depsOfConcurrency bounds how many DepsOf subprocess calls NewReadiness
// ever has in flight at once (#1745) — a small fixed pool, not
// cfg.MaxParallel, since this fans out per-issue lookups within a single
// wave rather than whole-dispatch parallelism.
const depsOfConcurrency = 8

// Sources maps an issue number to the source (native relationship vs
// body-text parsing) DepsOf resolved each of its blockers from, mirroring
// the keys of the edges map a Readiness carries alongside it. Carrying
// source as data keyed off the same edges — rather than switching on
// adapter type at render time — is what lets Jira (always native) and the
// local tracker (always body) render correctly without display-layer
// special cases.
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

// Readiness is the query seam answering "may this issue dispatch now, and
// if not, why" ahead of a Plan (CONTEXT.md's Readiness entry, #1547): the
// dependency graph for a batch of issues (edges child -> blockers, the
// source each blocker ref was resolved from, and the set of issues whose
// own DepsOf call failed), plus the Status/Ready queries answered against
// it. It replaces the package's former exported blocker primitives
// (BuildEdges, BlockerReady, BlockerStatus), now package-internal; the
// pre-dispatch consumers naming it in CONTEXT.md — the Console's held picks
// (#650) and preview's blocker annotations — use it instead of reaching
// into the wave engine's own internal gate.
type Readiness struct {
	Edges   map[string][]string
	Sources Sources
	Failed  map[string]bool
}

// NewReadiness resolves the dependency graph for the given batch of issues
// by calling the IssueTracker's DepsOf for each, plus the source each
// blocker ref was resolved from. Non-fatal per-issue errors are skipped,
// matching the original best-effort behaviour, but named in the returned
// Failed set so a caller can tell a transient DepsOf failure apart from a
// confirmed zero-blocker issue (#752) — the two look identical in Edges
// alone, since both simply omit the issue's key. Callers pass the result's
// Edges as Input.Edges and Sources as Input.Sources to NewPlan.
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
			// Non-fatal: skip issues whose data cannot be fetched.
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

// Status reports num's blocker readiness against r.Edges without
// transitioning any tracker state — the seam the Console (#650) reuses to
// hold a pick rather than the headless engine's own cascade-to-Failed
// (which moves the dependent issue itself to Failed when a blocker's failed
// set is non-empty). ready is true when every declared blocker is
// satisfied (Ready) and none carries cfg.FailedLabel; unready names every
// blocker not yet satisfied, in edge order. failed scans all of
// r.Edges[num], not just unready — a blocker can be closed (so Ready's
// fallback calls it satisfied) and still carry cfg.FailedLabel, which must
// never be satisfiable regardless of readiness.
// failed is reported separately from unready rather than folded into it:
// unready drives the console's BlockedBy badge and failed drives Reason
// (queue.go's setHeld), and collapsing the two would reintroduce the
// redundant rendering #755 removes.
func (r Readiness) Status(cfg Config, it forge.IssueTracker, cf forge.CodeForge, num string) (ready bool, failed, unready []string) {
	return blockerStatus(cfg, it, cf, num, r.Edges)
}

// Ready reports whether a single blocker ref — not necessarily one of r's
// own Edges entries, e.g. the selective `dispatch <nums>` path's
// external-blocker eviction pass, which checks blockers outside the batch
// r was resolved from — is satisfied: the blocker's PR is merged, or it
// resolves (via the issue-fallback path) to an issue that is closed or a PR
// that is merged, with no discoverable agent branch (human-handled work, or
// a blocker ref that names a PR number directly). cf's PR surface is
// optional: a push-only Code Forge (no PRForge) has no PR to discover, so
// readiness falls straight to the issue-closed check.
func (r Readiness) Ready(it forge.IssueTracker, cf forge.CodeForge, dep string) bool {
	ready, _ := blockerReady(it, cf, dep)
	return ready
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

// blockerReady is Readiness.Ready's logic, plus the fetched forge.Issue when
// the readiness check needed one. fi is nil when a merged-PR lookup resolved
// readiness without ever calling it.Issue, letting blockerStatus tell "no
// fetch happened" apart from "fetched and still open" without a second call.
func blockerReady(it forge.IssueTracker, cf forge.CodeForge, dep string) (ready bool, fi *forge.Issue) {
	if pr, ok := cf.(forge.PRForge); ok {
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
	return false, &issue
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
		if ready, _ := blockerReady(it, cf, dep); !ready {
			out = append(out, dep)
		}
	}
	return out
}

// blockerStatus is Readiness.Status's logic, generalized to an arbitrary
// edges map so the engine's own internal callers (drainMaxJobs, nextReady)
// can reuse it against a Plan's edges without going through a Readiness
// value.
func blockerStatus(cfg Config, it forge.IssueTracker, cf forge.CodeForge, num string, edges map[string][]string) (ready bool, failed, unready []string) {
	for _, dep := range edges[num] {
		depReady, fi := blockerReady(it, cf, dep)
		if !depReady {
			unready = append(unready, dep)
		}
		if fi == nil {
			issue, err := it.Issue(dep)
			if err != nil {
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
