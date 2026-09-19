package console

import (
	tea "github.com/charmbracelet/bubbletea"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/forge"
)

// openDetailModalCmd fetches number's body separately because ListOpenIssues
// never carries Body, then resolves Blocked-by from DepsOf and Blocks from
// BlocksOf, which only forge.BlockersLister trackers have (not local, which
// has no reverse edge to query). Reading one issue's edges keeps first-open
// latency independent of backlog size (issue #1744, replacing #1632/#1746).
func openDetailModalCmd(tracker forge.IssueTracker, all []forge.Issue, number string) tea.Cmd {
	return func() tea.Msg {
		issue, err := tracker.Issue(number)
		if err != nil {
			return DetailModalLoadedMsg{Number: number, Err: err}
		}
		blockedBy := resolveEdgeRefs(tracker, all, tracker.DepsOf, number)
		var blocks []BlockerRef
		// Resolve capabilities rather than asserting
		// tracker.(forge.BlockersLister) directly (issue #2946). CodeForge is
		// nil because BlockersLister lives on the tracker side only.
		caps := forge.ResolveCapabilities(nil, tracker, backend.Descriptor{}, backend.Descriptor{})
		if caps.BlockersLister != nil {
			blocks = resolveEdgeRefs(tracker, all, caps.BlockersLister.BlocksOf, number)
		}
		return DetailModalLoadedMsg{Number: number, Body: issue.Body, BlockedBy: blockedBy, Blocks: blocks}
	}
}

// resolveEdgeRefs resolves one direction of number's dependency edge into
// BlockerRefs. A fetch failure yields no refs rather than failing the whole
// modal load (issue #1744).
func resolveEdgeRefs(tracker forge.IssueTracker, all []forge.Issue, fetch func(string) ([]forge.Dependency, error), number string) []BlockerRef {
	deps, err := fetch(number)
	if err != nil {
		return nil
	}
	ids := make([]string, len(deps))
	sourceOf := make(map[string]forge.DepSource, len(deps))
	for i, d := range deps {
		ids[i] = d.ID
		sourceOf[d.ID] = d.Source
	}
	return resolveBlockerRefs(tracker, all, ids, sourceOf)
}

// resolveBlockerRefs resolves ids into BlockerRefs tagged with their DepSource
// (issue #1632).
func resolveBlockerRefs(tracker forge.IssueTracker, all []forge.Issue, ids []string, sourceOf map[string]forge.DepSource) []BlockerRef {
	refs := make([]BlockerRef, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, resolveBlockerRef(tracker, all, id, sourceOf[id]))
	}
	return refs
}

// resolveBlockerRef skips the fetch when the issue is already in the backlog
// list; a closed blocker, or one past the backlog's open-only listing, costs
// its own Issue call. A failed resolution leaves Title empty rather than
// failing the whole modal load (issue #1632).
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
