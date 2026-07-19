// Package console: detail.go carries the ticket detail modal's async
// fetch — its body and its Blocked-by/Blocks lists — the seam through which
// tea.go's openDetailModal reaches the forge.IssueTracker and waves.
// BuildEdges (issue #1632). tea.go stays the thin key-routing adapter;
// this file holds the actual data-resolution logic, mirroring
// activity.go/transcript.go's own split from tea.go for the live-tail
// sidebar.
package console

import (
	"sort"
	"strconv"

	tea "github.com/charmbracelet/bubbletea"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/waves"
)

// openDetailModalCmd loads number's body — a separate Issue fetch, since
// ListOpenIssues never carries Body — plus its Blocked-by and Blocks lists,
// both derived from the whole-backlog dependency edge graph (waves.
// BuildEdges) rather than a fresh per-ticket DepsOf call: Blocks is the
// graph's own reverse edges, and Blocked-by rides along in the same batch
// call for free. edges/sources are the Model's already-retained graph; nil
// means no session-scoped graph exists yet (never opened a detail modal
// since startup, or "r" just invalidated it), so this builds it once here
// and hands the built graph back on DetailModalLoadedMsg for Update to
// retain — every later modal open within the same session reuses it without
// a further BuildEdges sweep (issue #1632).
func openDetailModalCmd(tracker forge.IssueTracker, all []forge.Issue, edges map[string][]string, sources map[string]map[string]forge.DepSource, number string) tea.Cmd {
	return func() tea.Msg {
		issue, err := tracker.Issue(number)
		if err != nil {
			return DetailModalLoadedMsg{Number: number, Err: err}
		}
		builtGraph := edges == nil
		if builtGraph {
			wavesIssues := make([]waves.Issue, len(all))
			for i, iss := range all {
				wavesIssues[i] = waves.Issue{Number: iss.Number, Title: iss.Title}
			}
			result, _ := waves.BuildEdges(tracker, wavesIssues)
			edges, sources = result.Edges, result.Sources
		}
		blockedBy := resolveBlockerRefs(tracker, all, edges[number], sources[number])
		blockIDs, blockSources := invertEdges(edges, sources, number)
		blocks := resolveBlockerRefs(tracker, all, blockIDs, blockSources)
		msg := DetailModalLoadedMsg{Number: number, Body: issue.Body, BlockedBy: blockedBy, Blocks: blocks}
		if builtGraph {
			msg.Edges, msg.Sources = edges, sources
		}
		return msg
	}
}

// resolveBlockerRefs resolves each of ids into a BlockerRef, tagged with its
// DepSource from sourceOf — the Blocked-by and Blocks sections' shared
// resolution step (issue #1632).
func resolveBlockerRefs(tracker forge.IssueTracker, all []forge.Issue, ids []string, sourceOf map[string]forge.DepSource) []BlockerRef {
	refs := make([]BlockerRef, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, resolveBlockerRef(tracker, all, id, sourceOf[id]))
	}
	return refs
}

// resolveBlockerRef resolves one blocker/blocked issue's title and
// open/closed state: free (no fetch) when it's already loaded in the
// backlog list, fetched with its own Issue call otherwise — e.g. a closed
// blocker, or one dispatched past the backlog's open-only listing (issue
// #1632). A resolution failure (the issue was deleted, or the fetch erred)
// leaves Title empty rather than failing the whole modal load.
func resolveBlockerRef(tracker forge.IssueTracker, all []forge.Issue, id string, source forge.DepSource) BlockerRef {
	for _, iss := range all {
		if iss.Number == id {
			return BlockerRef{Number: id, Source: source, State: iss.State, Title: iss.Title}
		}
	}
	if iss, err := tracker.Issue(id); err == nil {
		return BlockerRef{Number: id, Source: source, State: iss.State, Title: iss.Title}
	}
	return BlockerRef{Number: id, Source: source}
}

// invertEdges returns the issue numbers that declare number as one of their
// own blockers — edges' reverse direction, the Blocks section's whole
// derivation (issue #1632) — in ascending numeric order (GitHub's own
// canonical order, ListOpenIssues' convention), since map iteration order is
// otherwise random. sourceOf mirrors each returned id to the DepSource the
// forward edge (id -> number) was resolved from, the same annotation shown
// from the other end in id's own Blocked-by section.
func invertEdges(edges map[string][]string, sources map[string]map[string]forge.DepSource, number string) (ids []string, sourceOf map[string]forge.DepSource) {
	sourceOf = make(map[string]forge.DepSource)
	for child, blockers := range edges {
		for _, b := range blockers {
			if b == number {
				sourceOf[child] = sources[child][number]
				ids = append(ids, child)
				break
			}
		}
	}
	sortIssueNumbers(ids)
	return ids, sourceOf
}

// sortIssueNumbers sorts nums ascending by numeric value where every entry
// parses as one, falling back to a lexical sort otherwise — GitHub's own
// canonical issue order (ListOpenIssues' convention), applied to
// invertEdges' otherwise map-iteration-order result.
func sortIssueNumbers(nums []string) {
	sort.Slice(nums, func(i, j int) bool {
		ni, ei := strconv.Atoi(nums[i])
		nj, ej := strconv.Atoi(nums[j])
		if ei == nil && ej == nil {
			return ni < nj
		}
		return nums[i] < nums[j]
	})
}
