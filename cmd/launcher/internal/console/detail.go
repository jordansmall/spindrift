// Package console: detail.go carries the ticket detail modal's async
// fetch — its body and its Blocked-by/Blocks lists — the seam through which
// tea.go's openDetailModal reaches the forge.IssueTracker. tea.go stays the
// thin key-routing adapter; this file holds the actual data-resolution
// logic, mirroring activity.go/transcript.go's own split from tea.go for
// the live-tail sidebar.
package console

import (
	tea "github.com/charmbracelet/bubbletea"

	"spindrift.dev/launcher/internal/forge"
)

// openDetailModalCmd loads number's body — a separate Issue fetch, since
// ListOpenIssues never carries Body — plus its Blocked-by and Blocks lists,
// each resolved directly from number's own dependency edge rather than a
// whole-backlog readiness graph (issue #1744, replacing the
// waves.NewReadiness sweep issue #1632 originally used here, and the
// keep-it-warm-across-refresh mitigation issue #1746 layered onto that
// sweep in turn — both moot once nothing here ever builds the whole graph
// to begin with): Blocked-by is a single DepsOf call, and Blocks is a
// single BlocksOf call on trackers that implement forge.BlockersLister
// (github, jira, whose blocked/blocking relationship is genuinely native
// and bidirectional) — nil on trackers that don't (local, whose only
// blocker concept is one-directional body-text parsing with no reverse to
// query short of scanning every issue file). This keeps first-open latency
// independent of backlog size, on every open, not just a warm one.
func openDetailModalCmd(tracker forge.IssueTracker, all []forge.Issue, number string) tea.Cmd {
	return func() tea.Msg {
		issue, err := tracker.Issue(number)
		if err != nil {
			return DetailModalLoadedMsg{Number: number, Err: err}
		}
		blockedBy := resolveEdgeRefs(tracker, all, tracker.DepsOf, number)
		var blocks []BlockerRef
		if bl, ok := tracker.(forge.BlockersLister); ok {
			blocks = resolveEdgeRefs(tracker, all, bl.BlocksOf, number)
		}
		return DetailModalLoadedMsg{Number: number, Body: issue.Body, BlockedBy: blockedBy, Blocks: blocks}
	}
}

// resolveEdgeRefs resolves number's dependency edge in one direction —
// fetch is tracker.DepsOf for Blocked-by, or a BlockersLister's BlocksOf for
// Blocks — into BlockerRefs. A fetch failure resolves to no refs rather
// than failing the whole modal load, matching resolveBlockerRef's own
// per-ref tolerance below (issue #1744).
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
