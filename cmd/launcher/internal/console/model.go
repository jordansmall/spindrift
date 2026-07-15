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
	// PendingQuit is whether a quit confirm is armed, awaiting the
	// operator's drain/terminate-all/stay answer — only when live
	// Dispatches exist at quit time (issue #651, ADR 0023).
	PendingQuit bool
	// Cursor indexes the highlighted row in Visible() — the tea layer's j/k
	// and arrow-key navigation target (issue #784). Always clamped into
	// [0, len(Visible())-1], 0 when Visible() is empty.
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
		m.DrillIn = &DrillInState{Number: msg.Number, Rendered: msg.Rendered, Raw: msg.Raw, Err: msg.Err, ShowRaw: showRaw, Offset: offset}
	case DrillInToggleMsg:
		if m.DrillIn != nil {
			m.DrillIn.ShowRaw = !m.DrillIn.ShowRaw
		}
	case DrillInCloseMsg:
		m.DrillIn = nil
	case DrillInScrollMsg:
		if m.DrillIn != nil {
			m.DrillIn.Offset += msg.Delta
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
	}
	m.Cursor = clampCursor(m.Cursor, len(m.Visible()))
	clampDrillInOffset(m.DrillIn)
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

// clampDrillInOffset pulls d.Offset into [0, lines-1] — the drill-in
// analogue of clampCursor, so a scroll commanded past either end of the
// active form (Rendered, or Raw while ShowRaw) never leaves an Offset
// renderDrillIn can't slice with, and a raw/rendered toggle whose other form
// has fewer lines still lands somewhere valid (issue #786). A nil d is a
// no-op — Update calls this unconditionally, matching the cursor clamp.
func clampDrillInOffset(d *DrillInState) {
	if d == nil {
		return
	}
	content := d.Rendered
	if d.ShowRaw {
		content = d.Raw
	}
	maxOffset := len(strings.Split(content, "\n")) - 1
	switch {
	case d.Offset < 0:
		d.Offset = 0
	case d.Offset > maxOffset:
		d.Offset = maxOffset
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
