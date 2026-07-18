// Package console is the Elm-architecture core of the `console` subcommand
// (issue #645): a pure Model/Update/View, fed by a thin adapter that turns
// IssueTracker results into Msg values. The dependency arrow is one-way —
// engine packages (forge, waves, dispatch, settle, runner) never import
// console.
package console

import (
	"strings"

	"spindrift.dev/launcher/internal/forge"
)

// Model is the console's whole state: the unfiltered backlog, the active
// label filter, and whether the operator has asked to quit. Update is the
// only function that produces a new Model; View is the only function that
// renders one.
type Model struct {
	All      []forge.Issue
	Filter   string
	Quitting bool
	// Err is the last refresh error, if any. A failed refresh leaves All
	// untouched — Err surfaces alongside the stale list rather than
	// replacing it with an empty one.
	Err error
	// DogfoodLive is whether a live dogfood pid-file was found at startup —
	// informational only, set once and never gated on.
	DogfoodLive bool
	// Picks is the session's operator queue, in pick order.
	Picks []Pick
	// DrillIn is the open transcript view, if any — nil when the operator is
	// looking at the backlog/queue instead.
	DrillIn *DrillInState
	// PendingTerminate is the issue number awaiting an explicit y/N confirm
	// after "X" — empty when no terminate is pending (ADR 0024, issue #649).
	PendingTerminate string
	// Cap and Live are the session's live parallelism cap and current live
	// count (issue #653, ADR 0023) — zero in a launch-less session, since
	// syncQueue never sends a CapMsg when there is no Launcher to read them
	// from.
	Cap, Live int
	// Stale is whether the freshness probe found the loaded image would be
	// rebuilt against the current base-branch tip — new launches hold while
	// true; a running Box rides it out (issue #652).
	Stale bool
	// StaleMessage is the probe's human-readable explanation, shown
	// alongside Stale in the banner.
	StaleMessage string
	// Rebuilding is whether an operator-triggered in-session rebuild is in
	// flight.
	Rebuilding bool
	// RebuildErr is the last rebuild's failure, if any — "" on success or
	// when no rebuild has run yet.
	RebuildErr string
	// OrphanRecoveryErr is startup orphan recovery's last failure, if any —
	// "" when detection and every adopt succeeded, or recovery hasn't run
	// yet (issue #1218).
	OrphanRecoveryErr string
	// RebuildOutput is the last rebuild's captured nix output (issue #765)
	// — stdout/stderr merged, in build order — never streamed to the
	// Console's own stdout/stderr while the rebuild ran. "" when no rebuild
	// has run yet.
	RebuildOutput string
	// BranchSwitchNotice is the last rebuild's branch-switch notice, if any
	// — "" when pwd's checkout didn't move off the branch it was on (issue
	// #1141), shown alongside the other rebuild alert lines in the header.
	BranchSwitchNotice string
	// ShowRebuildOutput is whether the rebuild-output pane is open, showing
	// RebuildOutput in full — RebuildOutput's only consumer (issue #1128).
	// RebuildOutputOpenMsg only ever sets it while RebuildOutput is
	// non-empty, but a later StaleStatusMsg can still empty RebuildOutput
	// out from under an already-open pane — the pane just renders blank
	// rather than closing itself.
	ShowRebuildOutput bool
	// RebuildOutputOffset is the rebuild-output pane's scroll position — the
	// index of its first visible line, the pane's analogue of DrillInState's
	// Offset (issue #1128).
	RebuildOutputOffset int
	// PendingQuit is whether a quit confirm is armed, awaiting the
	// operator's drain/terminate-all/stay answer — only when live
	// Dispatches exist at quit time (issue #651, ADR 0023).
	PendingQuit bool
	// PendingPick is whether "p" is waiting on the "pa" leader window,
	// awaiting a trailing "a" (pick-all-ready) before the 200ms
	// pickChordTimeout resolves it to a single-issue pick instead — rendered
	// as a visible hint so the wait isn't silent (issue #835).
	PendingPick bool
	// QueueEnterNotice is a one-shot, human-readable message rendered after
	// Enter is a no-op on a focused work-queue row lacking a Transcript
	// (queued/claiming/held/dissolved, per hasTranscript) — empty otherwise.
	// It clears on the operator's next keypress, mirroring PendingPick's
	// resolve-on-any-key precedent rather than a timer (issue #998).
	QueueEnterNotice string
	// Cursor indexes the highlighted row within the active Section's own row
	// list — Visible() for SectionBacklog, sectionPicks(m, ActiveSection) for
	// a work Section (ADR 0030) — the tea layer's j/down and up/arrow
	// navigation target (issue #784; "k" moved to Terminate in #785, then to
	// "X" in #1500). Always clamped into [0, len(rows)-1], 0 when the active
	// Section is empty. A Section switch resets it to 0 (issue #1500) — AC
	// only requires clamping per Section, not remembering position across
	// switches, so this stays the single shared field #784 introduced rather
	// than growing a cursor per Section.
	Cursor int
	// ShowHelp is whether the "?" help overlay is open, listing every key
	// the tea layer binds (issue #784).
	ShowHelp bool
	// FilterEditing is whether "/" has been pressed and not yet confirmed
	// (Enter) or cancelled (Esc) — while true, the tea layer routes typed
	// runes into FilterChangedMsg instead of navigation keys (issue #784).
	FilterEditing bool
	// preEditFilter is Filter's value from just before FilterEditStartMsg,
	// restored verbatim by FilterEditCancelMsg — Update-internal, not
	// rendered.
	preEditFilter string
	// Width and Height are the terminal's current size, set by the tea
	// layer's translation of Bubble Tea's WindowSizeMsg (issue #842).
	// Update's unconditional clamp floors both at minTerminalDimension
	// before the first size event arrives. View derives the header height
	// and the body's row budget from Height on every render (issue #1035).
	Width, Height int
	// Offset is the active Section's scroll offset — the index of its first
	// rendered row, clamped into [0, len(rows)-1] the same way DrillIn.Offset
	// is (issue #1036). CursorMoveMsg keeps it advancing with Cursor so the
	// highlighted row never scrolls off; ScrollMsg moves it directly. Reset
	// to 0 on a Section switch, matching Cursor (issue #1500).
	Offset int
	// ActiveSection is which of the five Sections the body renders — the
	// section-switched list's single-list analogue of the retired
	// FocusedColumn (ADR 0030). SectionBacklog, the zero value, matches a
	// fresh Console opening on the pick source.
	ActiveSection Section
}

// DrillInState is one Dispatch's loaded transcript: both the Driver-rendered
// form and the byte-exact raw form, loaded together so ShowRaw toggles with
// no further I/O.
type DrillInState struct {
	Number        string
	Rendered, Raw string
	ShowRaw       bool
	Err           error
	// Offset is the index of the first visible line in the currently active
	// form (Rendered, or Raw while ShowRaw) — the scroll position (#786).
	Offset int
	// Lines is the active form (Rendered, or Raw while ShowRaw) pre-split on
	// "\n". Update recomputes it only when the content or ShowRaw changes
	// (DrillInMsg, DrillInToggleMsg), so clampDrillInOffset and the render
	// functions never re-split the full transcript on every keystroke (issue
	// #722). As recorded when #722 landed, a scroll keystroke against a
	// 10MB+ transcript (BenchmarkUpdate_DrillInScroll_LargeTranscript, issue
	// #1016) went from 1.59ms/op, 2.5MB/op, 1 alloc/op (pre-cache,
	// re-splitting the transcript every call) to 51.5ns/op, 0B/op, 0
	// allocs/op (cached) — the alloc counts are the invariant; absolute
	// ns/op and B/op vary by machine and Go version. Reproduce with `go
	// test ./internal/console/... -run '^$' -bench
	// BenchmarkUpdate_DrillInScroll -benchmem` from cmd/launcher.
	Lines []string
}

// NewModel returns the zero-value console state: no issues loaded yet, no
// filter, not quitting.
func NewModel() Model {
	return Model{}
}

// Visible returns All narrowed by Filter — the list View renders. An empty
// Filter returns All unchanged.
func (m Model) Visible() []forge.Issue {
	if m.Filter == "" {
		return m.All
	}
	var out []forge.Issue
	for _, iss := range m.All {
		if issueHasLabelContaining(iss, m.Filter) {
			out = append(out, iss)
		}
	}
	return out
}

// HasHighlighted reports whether Visible has a row at Cursor for the
// operator to act on — the PendingPick hint's gate, since Pick only ever
// targets a Backlog row (ADR 0030's pick source) regardless of which
// Section is active when "p" is pressed. Cursor is clamped to [0,
// len(Visible())-1] and to 0 when Visible is empty, so this is exactly
// len(m.Visible()) > 0.
func (m Model) HasHighlighted() bool {
	return len(m.Visible()) > 0
}

// Update applies msg to m and returns the resulting Model. It is pure: no
// I/O, no network — the adapter and the tea layer are the only callers that
// touch either, translating their results into a Msg before calling Update.
func Update(m Model, msg Msg) Model {
	switch msg := msg.(type) {
	case IssuesLoadedMsg:
		m.Err = msg.Err
		if msg.Err == nil {
			m.All = msg.Issues
		}
	case FilterChangedMsg:
		m.Filter = msg.Filter
	case QuitRequestedMsg:
		m.PendingQuit = true
	case QuitCancelledMsg:
		m.PendingQuit = false
	case PickPendingMsg:
		m.PendingPick = true
	case PickResolvedMsg:
		m.PendingPick = false
	case QueueEnterNoticedMsg:
		m.QueueEnterNotice = "no transcript to show"
	case QueueEnterNoticeClearedMsg:
		m.QueueEnterNotice = ""
	case QuitMsg:
		m.PendingQuit = false
		m.Quitting = true
	case DogfoodNoticeMsg:
		m.DogfoodLive = msg.Live
	case PickQueuedMsg:
		m.Picks = append(m.Picks, Pick{Number: msg.Number, Title: msg.Title, Kind: msg.Kind, State: PickQueued})
	case PickDissolvedMsg:
		m.Picks = append(m.Picks, Pick{Number: msg.Number, Title: msg.Title, State: PickDissolved, Reason: msg.Reason})
	case UnpickMsg:
		m.Picks = removePick(m.Picks, msg.Number)
	case QueueSnapshotMsg:
		m.Picks = msg.Picks
	case DrillInMsg:
		showRaw := false
		offset := 0
		if m.DrillIn != nil && m.DrillIn.Number == msg.Number {
			showRaw = m.DrillIn.ShowRaw
			offset = m.DrillIn.Offset
		}
		content := msg.Rendered
		if showRaw {
			content = msg.Raw
		}
		m.DrillIn = &DrillInState{Number: msg.Number, Rendered: msg.Rendered, Raw: msg.Raw, Err: msg.Err, ShowRaw: showRaw, Offset: offset, Lines: strings.Split(content, "\n")}
	case DrillInToggleMsg:
		if m.DrillIn != nil {
			m.DrillIn.ShowRaw = !m.DrillIn.ShowRaw
			content := m.DrillIn.Rendered
			if m.DrillIn.ShowRaw {
				content = m.DrillIn.Raw
			}
			m.DrillIn.Lines = strings.Split(content, "\n")
		}
	case DrillInCloseMsg:
		m.DrillIn = nil
	case DrillInScrollMsg:
		if m.DrillIn != nil {
			m.DrillIn.Offset += msg.Delta
		}
	case ScrollMsg:
		// Adds Delta unconditionally, then clampCursor below still clamps
		// into [0, len-1] by total row count alone — not by how many rows
		// the viewport can actually show. This is current, undocumented
		// behavior rather than a deliberate design choice: a pgdown on a
		// column whose content already fits on screen still scrolls to the
		// last row instead of no-op'ing, hiding the earlier, already-visible
		// rows (issue #1060; a viewport-aware fix, if wanted, is #1053).
		m.Offset += msg.Delta
	case TerminateRequestedMsg:
		m.PendingTerminate = msg.Number
	case TerminateConfirmedMsg:
		m.PendingTerminate = ""
	case TerminateCancelledMsg:
		m.PendingTerminate = ""
	case CapMsg:
		m.Cap = msg.Cap
		m.Live = msg.Live
	case StaleStatusMsg:
		m.Stale = msg.Stale
		m.StaleMessage = msg.Message
		m.Rebuilding = msg.Rebuilding
		m.RebuildErr = msg.RebuildErr
		m.RebuildOutput = msg.RebuildOutput
		m.BranchSwitchNotice = msg.BranchSwitchNotice
	case OrphanRecoveryMsg:
		m.OrphanRecoveryErr = msg.Err
	case RebuildOutputOpenMsg:
		if m.RebuildOutput != "" {
			m.ShowRebuildOutput = true
		}
	case RebuildOutputCloseMsg:
		m.ShowRebuildOutput = false
	case RebuildOutputScrollMsg:
		if m.ShowRebuildOutput {
			m.RebuildOutputOffset += msg.Delta
		}
	case CursorMoveMsg:
		m.Cursor += msg.Delta
	case HelpToggleMsg:
		m.ShowHelp = !m.ShowHelp
	case FilterEditStartMsg:
		m.FilterEditing = true
		m.preEditFilter = m.Filter
	case FilterEditConfirmMsg:
		m.FilterEditing = false
	case FilterEditCancelMsg:
		m.FilterEditing = false
		m.Filter = m.preEditFilter
	case SizeChangedMsg:
		m.Width = msg.Width
		m.Height = msg.Height
	case SectionPrevMsg:
		m = switchSection(m, (m.ActiveSection-1+sectionCount)%sectionCount)
	case SectionNextMsg:
		m = switchSection(m, (m.ActiveSection+1)%sectionCount)
	case SectionJumpMsg:
		m = switchSection(m, msg.Section)
	}
	m.Width = clampSize(m.Width)
	m.Height = clampSize(m.Height)
	m.Cursor = clampCursor(m.Cursor, sectionRowCount(m, m.ActiveSection))
	if m.DrillIn != nil {
		clampDrillInOffset(m.DrillIn, m.Height)
	}
	clampRebuildOutputOffset(&m)
	m.Offset = clampCursor(m.Offset, sectionRowCount(m, m.ActiveSection))
	if _, ok := msg.(CursorMoveMsg); ok {
		m.Offset = followViewport(m.Offset, m.Cursor, sectionRowCount(m, m.ActiveSection), columnItemBudget(bodyBudget(m)))
	}
	return m
}

// switchSection moves m to Section s, resetting Cursor and Offset to 0 when
// s differs from the currently active Section — a fresh Section starts at
// its top row rather than carrying over a position that belonged to a
// different list (issue #1500). Jumping to the Section that's already
// active is a no-op on Cursor/Offset, so a repeated "1" or an "H"/"L" that
// wraps back onto the same Section (only possible with a single Section,
// which sectionCount > 1 rules out today) never resets scroll position for
// nothing.
func switchSection(m Model, s Section) Model {
	if s != m.ActiveSection {
		m.Cursor = 0
		m.Offset = 0
	}
	m.ActiveSection = s
	return m
}

// sectionRowCount returns the row count of Section s's own list — Visible()
// for SectionBacklog, len(sectionPicks(m, s)) for a work Section — the
// figure Cursor and Offset clamp against instead of the whole backlog or the
// whole Picks slice, now that only one Section renders at a time (ADR 0030).
func sectionRowCount(m Model, s Section) int {
	if s == SectionBacklog {
		return len(m.Visible())
	}
	return len(sectionPicks(m, s))
}

// sectionPicks returns m.Picks narrowed to the ones pickSection maps onto s,
// in pick order — the work Sections' own row list (ADR 0030). Meaningless
// for SectionBacklog, whose rows are Visible() instead; callers branch on
// the Section before reaching for either.
func sectionPicks(m Model, s Section) []Pick {
	var out []Pick
	for _, p := range m.Picks {
		if pickSection(p.State) == s {
			out = append(out, p)
		}
	}
	return out
}

// clampCursor pulls cursor into [0, n-1], or 0 when n is zero — the single
// invariant every Update case shares, so a list that shrinks (a filter, a
// refresh) never leaves the cursor pointing past the end.
func clampCursor(cursor, n int) int {
	if n == 0 {
		return 0
	}
	if cursor < 0 {
		return 0
	}
	if cursor >= n {
		return n - 1
	}
	return cursor
}

// clampDrillInOffset pulls d.Offset into [0, lines-1], further capped so the
// last page fills the fullscreen viewport instead of leaving it mostly blank
// (issue #829) — the drill-in analogue of clampCursor, so a scroll commanded
// past either end of the active form (Rendered, or Raw while ShowRaw) never
// leaves an Offset renderDrillIn can't slice with, and a raw/rendered toggle
// whose other form has fewer lines still lands somewhere valid (issue #786).
// A nil d is a no-op — Update calls this unconditionally, matching the
// cursor clamp. Content that already fits the viewport (budget >=
// len(Lines), the short-content case) falls all the way back to 0 rather
// than to the last line. When height is too small to fit a page at all
// (budget == 0), pageMax never undercuts maxOffset, so an out-of-range
// Offset instead lands at the last line, len(Lines)-1.
func clampDrillInOffset(d *DrillInState, height int) {
	if d == nil {
		return
	}
	budget := height - headerFooterLines
	if budget < 0 {
		budget = 0
	}
	maxOffset := len(d.Lines) - 1
	if pageMax := len(d.Lines) - budget; pageMax < maxOffset {
		maxOffset = pageMax
	}
	if maxOffset < 0 {
		maxOffset = 0
	}
	switch {
	case d.Offset < 0:
		d.Offset = 0
	case d.Offset > maxOffset:
		d.Offset = maxOffset
	}
}

// clampRebuildOutputOffset pulls m.RebuildOutputOffset into [0, maxOffset],
// the rebuild-output pane's analogue of clampDrillInOffset — skipped
// entirely while the pane is closed so a large RebuildOutput never costs a
// strings.Count on every keystroke it isn't visible for (issue #1128).
func clampRebuildOutputOffset(m *Model) {
	if !m.ShowRebuildOutput {
		return
	}
	lines := strings.Count(m.RebuildOutput, "\n") + 1
	budget := m.Height - headerFooterLines
	if budget < 0 {
		budget = 0
	}
	maxOffset := lines - 1
	if pageMax := lines - budget; pageMax < maxOffset {
		maxOffset = pageMax
	}
	if maxOffset < 0 {
		maxOffset = 0
	}
	switch {
	case m.RebuildOutputOffset < 0:
		m.RebuildOutputOffset = 0
	case m.RebuildOutputOffset > maxOffset:
		m.RebuildOutputOffset = maxOffset
	}
}

// minTerminalDimension is the safe floor Width and Height clamp to — a
// non-sensical size (zero, or negative from a malformed WindowSizeMsg) never
// leaves Model claiming a terminal too small to lay anything out in (issue
// #842).
const minTerminalDimension = 1

// clampSize pulls a terminal dimension up to minTerminalDimension — the
// Width/Height analogue of clampCursor, so Update stays total over any
// resize input.
func clampSize(dim int) int {
	if dim < minTerminalDimension {
		return minTerminalDimension
	}
	return dim
}

// removePick drops the queued or held pick numbered num, if any — Unpick
// only ever removes a pick still holding at PickQueued or PickHeld; a pick
// already claiming, running, or settled is left alone.
func removePick(picks []Pick, num string) []Pick {
	var out []Pick
	for _, p := range picks {
		if p.Number == num && (p.State == PickQueued || p.State == PickHeld) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// issueHasLabelContaining reports whether any of iss's labels contains
// substr, case-insensitively — the match rule behind Filter, chosen so the
// filter narrows as the operator types rather than requiring an exact label.
func issueHasLabelContaining(iss forge.Issue, substr string) bool {
	substr = strings.ToLower(substr)
	for _, l := range iss.Labels {
		if strings.Contains(strings.ToLower(l), substr) {
			return true
		}
	}
	return false
}
