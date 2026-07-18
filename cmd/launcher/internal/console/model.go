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
	// after "k"/"kill"/"terminate" <num> — empty when no terminate is
	// pending (ADR 0024, issue #649).
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
	// Cursor indexes the highlighted row in Visible() — the tea layer's j/
	// down and up/arrow navigation target (issue #784; "k" moved to
	// Terminate in #785). Always clamped into [0, len(Visible())-1], 0 when
	// Visible() is empty.
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
	// Focus is which body column the cursor keys and context-Enter act on —
	// Tab toggles it (issue #845). FocusBacklog, the zero value, matches the
	// pre-#845 Console's backlog-only cursor.
	Focus FocusedColumn
	// QueueCursor indexes the highlighted row in Picks — the work-queue
	// column's own cursor, independent of Cursor (the backlog column's) and
	// clamped into [0, len(Picks)-1] the same way (issue #845).
	QueueCursor int
	// BacklogOffset is the backlog column's scroll offset — the index of its
	// first rendered row, clamped into [0, len(Visible())-1] the same way
	// DrillIn.Offset is (issue #1036). CursorMoveMsg keeps it advancing with
	// Cursor so the highlighted row never scrolls off; ScrollMsg moves it
	// directly. Independent of QueueOffset so Tab preserves each column's
	// scroll position across a focus toggle.
	BacklogOffset int
	// QueueOffset is the work-queue column's scroll offset — QueueCursor's
	// analogue of BacklogOffset, clamped into [0, len(Picks)-1] (issue
	// #1036).
	QueueOffset int
	// PaneMode is the operator-selected layout for an open DrillIn's
	// Transcript — docked (the zero value), floating, or fullscreen — cycled
	// by a key and derived down to fullscreen at View time on a terminal too
	// narrow for three columns, regardless of this stored value (issue #846,
	// ADR 0025).
	PaneMode TranscriptPaneMode
}

// TranscriptPaneMode is the Transcript pane's layout while a DrillIn is open
// (issue #846, ADR 0025).
type TranscriptPaneMode int

const (
	// PaneDocked is the zero value — the Transcript renders as a third
	// column beside the backlog and work queue, which stay visible.
	PaneDocked TranscriptPaneMode = iota
	// PaneFloating — the Transcript renders as an overlay atop the
	// two-column body.
	PaneFloating
	// PaneFullscreen — the Transcript takes the whole body, as it did before
	// issue #846.
	PaneFullscreen
)

// FocusedColumn is which of the two body columns holds the operator's
// cursor (issue #845).
type FocusedColumn int

const (
	// FocusBacklog is the zero value — cursor keys move Model.Cursor and
	// Enter Picks the highlighted backlog row.
	FocusBacklog FocusedColumn = iota
	// FocusQueue — cursor keys move Model.QueueCursor and Enter drills into
	// the highlighted pick's Transcript.
	FocusQueue
)

// focusedCursor returns a pointer to the focused column's cursor field —
// &m.QueueCursor while the work queue has focus, &m.Cursor (the backlog's)
// otherwise — so a caller mutates the right twin without re-testing Focus
// itself (issue #1062). Takes *Model, unlike focusedTotal/focusedBudget
// below, because a caller needs to write through it; those two only ever
// read, so they stay on the cheaper value receiver.
func focusedCursor(m *Model) *int {
	if m.Focus == FocusQueue {
		return &m.QueueCursor
	}
	return &m.Cursor
}

// focusedOffset returns a pointer to the focused column's scroll offset —
// &m.QueueOffset while the work queue has focus, &m.BacklogOffset (the
// backlog's) otherwise (issue #1062).
func focusedOffset(m *Model) *int {
	if m.Focus == FocusQueue {
		return &m.QueueOffset
	}
	return &m.BacklogOffset
}

// focusedTotal returns the focused column's underlying row count —
// len(m.Picks) for the work queue, len(m.Visible()) for the backlog (issue
// #1062).
func focusedTotal(m Model) int {
	if m.Focus == FocusQueue {
		return len(m.Picks)
	}
	return len(m.Visible())
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
		} else {
			// New pick: reset the layout to docked, matching #846 AC1
			// (issue #999).
			m.PaneMode = PaneDocked
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
		*focusedOffset(&m) += msg.Delta
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
		*focusedCursor(&m) += msg.Delta
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
	case FocusToggleMsg:
		if m.Focus == FocusBacklog {
			m.Focus = FocusQueue
		} else {
			m.Focus = FocusBacklog
		}
	case PaneModeCycleMsg:
		if m.DrillIn != nil {
			m.PaneMode = nextPaneMode(m.PaneMode)
		}
	}
	m.Cursor = clampCursor(m.Cursor, len(m.Visible()))
	m.QueueCursor = clampCursor(m.QueueCursor, len(m.Picks))
	if m.DrillIn != nil {
		clampDrillInOffset(m.DrillIn, transcriptHeight(m))
	}
	clampRebuildOutputOffset(&m)
	m.BacklogOffset = clampCursor(m.BacklogOffset, len(m.Visible()))
	m.QueueOffset = clampCursor(m.QueueOffset, len(m.Picks))
	if _, ok := msg.(CursorMoveMsg); ok {
		offset := focusedOffset(&m)
		*offset = followViewport(*offset, *focusedCursor(&m), focusedTotal(m), columnItemBudget(focusedBudget(m)))
	}
	m.Width = clampSize(m.Width)
	m.Height = clampSize(m.Height)
	return m
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
// cursor clamp. Content that already fits the viewport at Offset 0 (either
// because it's short, or because height is too small to fit a page at all)
// falls all the way back to 0 rather than to the last line.
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

// nextPaneMode advances mode one step through the fixed cycle docked ->
// floating -> fullscreen -> docked — PaneModeCycleMsg's transition table
// (issue #846).
func nextPaneMode(mode TranscriptPaneMode) TranscriptPaneMode {
	switch mode {
	case PaneDocked:
		return PaneFloating
	case PaneFloating:
		return PaneFullscreen
	default:
		return PaneDocked
	}
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
