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
	// PendingQuit is whether a quit confirm is armed, awaiting the
	// operator's drain/terminate-all/stay answer — only when live
	// Dispatches exist at quit time (issue #651, ADR 0023).
	PendingQuit bool
	// PendingPick is whether "p" is waiting on the "pa" leader window,
	// awaiting a trailing "a" (pick-all-ready) before the 200ms
	// pickChordTimeout resolves it to a single-issue pick instead — rendered
	// as a visible hint so the wait isn't silent (issue #835).
	PendingPick bool
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
	// #722).
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
	case QuitMsg:
		m.PendingQuit = false
		m.Quitting = true
	case DogfoodNoticeMsg:
		m.DogfoodLive = msg.Live
	case PickQueuedMsg:
		m.Picks = append(m.Picks, Pick{Number: msg.Number, Title: msg.Title, Kind: msg.Kind, State: PickQueued})
	case PickFailedMsg:
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
		if m.Focus == FocusQueue {
			m.QueueOffset += msg.Delta
		} else {
			m.BacklogOffset += msg.Delta
		}
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
	case CursorMoveMsg:
		if m.Focus == FocusQueue {
			m.QueueCursor += msg.Delta
		} else {
			m.Cursor += msg.Delta
		}
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
	clampDrillInOffset(m.DrillIn, m.Height)
	m.BacklogOffset = clampCursor(m.BacklogOffset, len(m.Visible()))
	m.QueueOffset = clampCursor(m.QueueOffset, len(m.Picks))
	if _, ok := msg.(CursorMoveMsg); ok {
		backlogBudget, queueBudget := bodyColumnBudgets(m)
		if m.Focus == FocusQueue {
			m.QueueOffset = followViewport(m.QueueOffset, m.QueueCursor, len(m.Picks), columnItemBudget(queueBudget))
		} else {
			m.BacklogOffset = followViewport(m.BacklogOffset, m.Cursor, len(m.Visible()), columnItemBudget(backlogBudget))
		}
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
