// Package console is the Elm-architecture core of the `console` subcommand
// (issue #645): a pure Model/Update/View, fed by a thin adapter that turns
// IssueTracker results into Msg values. The dependency arrow is one-way:
// engine packages (forge, waves, dispatch, settle, runner) never import
// console.
package console

import (
	"strings"

	"spindrift.dev/launcher/internal/forge"
)

// Model is the console's whole state. Update is the only function that
// produces a new Model; View is the only one that renders one.
type Model struct {
	All      []forge.Issue
	Filter   string
	Quitting bool
	// Err is the last refresh error. A failed refresh leaves All untouched, so
	// Err shows alongside the stale list rather than an empty one.
	Err error
	// DogfoodLive is whether a live dogfood pid-file was found at startup.
	// Informational only, never gated on.
	DogfoodLive bool
	// Picks is the session's operator queue, in pick order.
	Picks []Pick
	// Sidebar is the open live-tail sidebar, nil when none is open. It docks
	// beside the still-visible list on a wide enough terminal and goes
	// fullscreen otherwise (ADR 0030, #1500, #1501).
	Sidebar *SidebarState
	// Focus is which pane keyboard input drives while Sidebar is open, moved
	// with h/l (and left/right). Meaningless while Sidebar is nil, where every
	// key targets the list (ADR 0030, #1501).
	Focus Focus
	// Mode is which modal state exclusively owns the keyboard outside of
	// Sidebar, whose ownership is derived from Sidebar/Focus/SidebarZoom
	// instead. Folding six independently settable fields into one makes two of
	// them being true at once unrepresentable (issue #1543).
	Mode Mode
	// TerminateConfirm is the pending "X" terminate's payload. Its Number is
	// meaningful only while Mode is ModeTerminateConfirm (ADR 0024, issue
	// #649, folded into Mode by issue #1543).
	TerminateConfirm TerminateConfirmState
	// Cap and Live are the session's live parallelism cap and current live
	// count (issue #653, ADR 0023), both zero in a launch-less session since
	// refreshPickDecorations sends no CapMsg without a Launcher.
	Cap, Live int
	// RecoverableCount is how many open issues carry the tracker's Recoverable
	// dispatch-state label: state stranded by a prior run, distinct from the
	// Picks-derived counts of this session's own launches (issue #2255, ADR
	// 0039 slice S4).
	RecoverableCount int
	// RebuildStatus is the launcher's live image-freshness state. New launches
	// hold while Stale is true; a running Box is unaffected (issue #652,
	// #1541).
	RebuildStatus RebuildStatus
	// OrphanRecoveryErr is the explicit adopt gesture's last failure, "" when
	// nothing has been adopted yet or the last adopt succeeded (issue #1218,
	// #1619).
	OrphanRecoveryErr string
	// OrphanNums is the issue numbers startup detection reported running with
	// no live goroutine in this process. They are adopted only through the
	// explicit gesture IsOrphan gates (issue #1619).
	OrphanNums []string
	// OrphanHeartbeats is each orphan-flagged issue's last-parsed status line,
	// keyed by number (issue #1621). Absent ("") for a number with no complete
	// heartbeat line yet.
	OrphanHeartbeats map[string]string
	// AdoptingOrphans is the issue numbers with an adopt gesture's RecoverFn
	// call in flight. IsAdoptingOrphan gates a second "A" for that whole
	// window: the orphan flag alone does not clear until completion, so gating
	// on it let a second RecoverFn race the first over the same PR (issue
	// #1619 review finding).
	AdoptingOrphans []string
	// RebuildOutputOffset is the index of the rebuild-output pane's first
	// visible line (issue #1128). Meaningful only while Mode is
	// ModeRebuildOutput.
	RebuildOutputOffset int
	// PendingG is whether a lone "g" is waiting on the "gg" leader window
	// (issue #1628). It lives on Model rather than Mode so it stays armed
	// across ModeList, ModeSidebar, and ModeRebuildOutput alike. A non-"g" key
	// cancels without consuming that key.
	PendingG bool
	// QueueEnterNotice is a one-shot message rendered after Enter is a no-op on
	// a work-queue row lacking a Transcript. It clears on the operator's next
	// keypress rather than a timer, and stays off Mode because it overlays
	// ModeList rather than claiming the keyboard (issue #998).
	QueueEnterNotice string
	// Toast is a one-shot message rendered after a queued pick transitions to
	// running, settled, failed, or held (issue #1830). QueueSnapshotMsg's
	// handler diffs the snapshot against the outgoing m.Picks, since that
	// snapshot is Update's only signal of a Queue-side change. It clears on a
	// generation-pinned tick, so a stale timer cannot clear a newer toast.
	Toast string
	// Cursor indexes the highlighted row within the active Section's own row
	// list (ADR 0030, issue #784). Always clamped into [0, len(rows)-1], 0
	// when that Section is empty. A Section switch resets it to 0 rather than
	// remembering a position per Section (issue #1500).
	Cursor int
	// preEditFilter is Filter's value from just before FilterEditStartMsg,
	// restored verbatim by FilterEditCancelMsg. Update-internal, not rendered.
	preEditFilter string
	// Width and Height are the terminal's current size, floored at
	// minTerminalDimension by Update's unconditional clamp before the first
	// size event arrives (issue #842). View derives the header height and the
	// body's row budget from Height on every render (issue #1035).
	Width, Height int
	// Offset is the index of the active Section's first rendered row, clamped
	// into [0, len(rows)-1] (issue #1036). CursorMoveMsg advances it with
	// Cursor so the highlighted row never scrolls off; ScrollMsg moves it
	// directly. Reset to 0 on a Section switch, matching Cursor (issue #1500).
	Offset int
	// ActiveSection is which of the five Sections the body renders (ADR 0030).
	// SectionBacklog, the zero value, opens a fresh Console on the pick source.
	ActiveSection Section
	// SidebarPositions retains each Dispatch's live-tail scroll offset and
	// Follow state across selection changes, keyed by Dispatch number (ADR
	// 0030, issue #1502), so a Dispatch selected again resumes where it was
	// left instead of at the top with Follow re-armed.
	SidebarPositions map[string]SidebarPosition
	// SidebarZoom is whether the operator forced the sidebar fullscreen with
	// "z", regardless of sidebarFits' width check (ADR 0030, issue #1502).
	// SidebarCloseMsg resets it so a later open on a wide terminal starts
	// docked.
	SidebarZoom bool
	// DetailModal is the open Backlog row's ticket detail modal, nil when none
	// is open. It floats as a bordered box over the still-rendered list (issue
	// #1758), falling back to fullscreen on a terminal too small for a legible
	// box (issue #1759).
	DetailModal *DetailModalState
	// DetailCache holds every detail modal's loaded content this session,
	// keyed by issue number, so reopening applies it synchronously with no
	// fetch. "r" (DetailCacheInvalidatedMsg) is the only thing that clears it
	// (issue #1632).
	DetailCache map[string]DetailModalCache
}

// DetailModalCache is one ticket's loaded detail modal content, retained on
// Model.DetailCache across a close and reopen. A failed load is never cached
// (issue #1632).
type DetailModalCache struct {
	Body      string
	BlockedBy []BlockerRef
	Blocks    []BlockerRef
}

// DetailModalState is one Backlog issue's open ticket detail modal. The row
// supplies Number, Title, and Labels the instant Enter opens it; Body and the
// Blocked-by/Blocks lists arrive from a background fetch, with Loading true in
// between (issue #1632).
type DetailModalState struct {
	Number, Title string
	Labels        []string
	Loading       bool
	Body          string
	BlockedBy     []BlockerRef
	Blocks        []BlockerRef
	// Err is the async body/blocker fetch's failure. Body, BlockedBy, and
	// Blocks are all meaningless while it is set.
	Err error
	// Offset is the index of the first visible line in Lines.
	Offset int
	// Lines is Body word-wrapped to the modal's width, followed by the
	// formatted Blocked-by/Blocks sections. It is computed once when
	// DetailModalLoadedMsg lands rather than re-wrapped on every keystroke
	// (the same caching as SidebarState.Lines, issue #722).
	Lines []string
}

// BlockerRef is one resolved entry in a ticket detail modal's Blocked-by or
// Blocks section. Source records whether the dependency edge came from a
// native relationship or from body-text parsing. Static text, with no
// drill-down into the referenced issue (issue #1632).
type BlockerRef struct {
	Number string
	Source forge.DepSource
	State  forge.IssueState
	Title  string
}

// SidebarPosition is one Dispatch's retained live-tail position (issue #1502,
// ADR 0030).
type SidebarPosition struct {
	Offset int
	Follow bool
}

// Focus names which pane keyboard input drives while a sidebar is open (ADR
// 0030). FocusList, the zero value, matches a Console with no sidebar open.
type Focus int

const (
	FocusList Focus = iota
	FocusSidebar
)

// Mode names which modal state exclusively owns the keyboard: ActiveMode
// returns the first Mode in modePrecedence whose modeActive check passes,
// with ModeList the always-active last resort (issue #1543). ModeSidebar is
// derived from Sidebar/Focus/SidebarZoom (ADR 0030), so a stale Mode can sit
// alongside a live Sidebar; modePrecedence's Sidebar-first check stops that.
type Mode int

const (
	ModeList Mode = iota
	// ModeSidebar is a focused, fullscreen-fallback, or zoomed live-tail
	// sidebar (ADR 0030). modeActive derives it from Sidebar/Focus/SidebarZoom
	// rather than a stored flag, so routing can never disagree with the layout
	// those same fields drive (#1501).
	ModeSidebar
	// ModeRebuildOutput is the rebuild-output pane open over
	// RebuildStatus.Output (issue #1128). RebuildOutputOpenMsg enters it only
	// while Output is non-empty, and a StaleStatusMsg that empties Output
	// leaves it rather than rendering blank (issue #1543).
	ModeRebuildOutput
	// ModeHelp is the "?" help overlay listing every key the tea layer binds
	// (issue #784).
	ModeHelp
	// ModeFilterEdit is "/" pressed and not yet confirmed or cancelled. The
	// tea layer routes typed runes into FilterChangedMsg instead of navigation
	// keys while it is active (issue #784).
	ModeFilterEdit
	// ModeTerminateConfirm is the pending "X" terminate awaiting a y/N answer.
	// TerminateConfirm.Number names the issue (ADR 0024, issue #649).
	ModeTerminateConfirm
	// ModeQuitConfirm is the armed quit confirm awaiting the operator's
	// drain/terminate-all/stay answer, entered only when live Dispatches exist
	// at quit time (issue #651, ADR 0023).
	ModeQuitConfirm
	// ModeDetailModal is the ticket detail modal a Backlog row's Enter opens.
	// modeActive derives it from DetailModal alone, since nil vs non-nil is
	// already the source of truth for View's own routing (issue #1632).
	ModeDetailModal
)

// modePrecedence is the keypress dispatch order as data: ActiveMode returns
// the first Mode here whose modeActive check passes, so a new mode joins by
// appending here plus one modeActive case (issue #1543). ModeDetailModal
// precedes ModeSidebar so that nothing depends on the two never both being
// open (issue #1632).
var modePrecedence = []Mode{
	ModeDetailModal,
	ModeSidebar,
	ModeRebuildOutput,
	ModeHelp,
	ModeFilterEdit,
	ModeTerminateConfirm,
	ModeQuitConfirm,
	ModeList,
}

// modeActive reports whether mode is the one currently owning the keyboard.
// Every case but ModeDetailModal, ModeSidebar, and ModeList reduces to m.Mode;
// those three derive ownership from their own fields instead.
func (m Model) modeActive(mode Mode) bool {
	switch mode {
	case ModeDetailModal:
		return m.DetailModal != nil
	case ModeSidebar:
		return m.Sidebar != nil && (m.Focus == FocusSidebar || !sidebarDocked(m))
	case ModeList:
		return true
	default:
		return m.Mode == mode
	}
}

// ActiveMode returns whichever Mode currently owns the keyboard, per
// modePrecedence (issue #1543). It must resolve that from Model's fields
// alone and never consult resolveLayout: mode authority is an input to the
// layout resolver, and a dependency the other way closes an ActiveMode ->
// resolveLayout -> bodyBudget -> renderHeader cycle (issue #3017).
func (m Model) ActiveMode() Mode {
	for _, mode := range modePrecedence {
		if m.modeActive(mode) {
			return mode
		}
	}
	return ModeList
}

// TerminateConfirmState is ModeTerminateConfirm's payload: the issue number
// "X" armed a pending y/N confirm for (ADR 0024, issue #649).
type TerminateConfirmState struct {
	Number string
}

// SidebarState is one Dispatch's loaded live-tail sidebar content: its
// condensed Activity feed and its whole Driver-rendered Transcript plus
// byte-exact raw form, loaded together so the Activity/Transcript and
// rendered/raw toggles need no further I/O (#1501).
type SidebarState struct {
	Number, Title string
	// Activity is the condensed feed ActivityFeed derived from the Dispatch's
	// most-recent pass log, the sidebar's default view.
	Activity []ActivityLine
	// TranscriptRendered and TranscriptRaw are the two forms of the Dispatch's
	// whole transcript, shown instead of Activity once ShowTranscript is set.
	TranscriptRendered, TranscriptRaw string
	// ShowTranscript is whether the sidebar shows the Transcript instead of
	// the Activity feed. "t" advances it, and ShowRaw within it, around a
	// three-step cycle (Activity, Transcript rendered, Transcript raw), so the
	// byte-exact raw form stays reachable without a second key (#1501).
	ShowTranscript bool
	// ShowRaw is whether the Transcript view shows TranscriptRaw instead of
	// TranscriptRendered. Meaningless while ShowTranscript is false.
	ShowRaw bool
	// Err is set when nothing could load at all (for example no Driver
	// configured). It shows regardless of ShowTranscript, since neither view
	// has anything to fall back to.
	Err error
	// TranscriptErr is set when only the Transcript's load failed while
	// Activity loaded. It shows only while ShowTranscript is true, so a
	// Transcript-only failure never blanks an otherwise-good Activity feed
	// (#1501 review finding).
	TranscriptErr error
	// Notice is a non-error explanation shown in place of an empty pane,
	// currently only "no local logs for this dispatch" for an orphan-flagged
	// Dispatch with nothing on disk yet (issue #1621). sidebarLines shows it
	// only while nothing else has loaded; live Activity clears it.
	Notice string
	// Offset is the index of the first visible line in the active form.
	Offset int
	// Follow is whether the sidebar auto-scrolls to the newest line as the
	// Activity feed advances, true the moment a feed opens. Scrolling up
	// detaches it so the operator can review frozen history, and G/End
	// re-attaches it at the bottom (issue #1502, ADR 0030).
	Follow bool
	// Lines is the currently active form, pre-split on "\n". Update recomputes
	// it only when the content or the toggle state changes, so Update's tail
	// and the render functions never re-split a multi-megabyte transcript on
	// every keystroke (issue #722).
	Lines []string
}

// NewModel returns the zero-value console state.
func NewModel() Model {
	return Model{}
}

// Visible returns the rows View renders: All narrowed by Filter. An empty
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

// IsOrphan reports whether num is a running agent-issue-<N> sandbox this
// process has no live goroutine for (issue #1619). It gates the explicit adopt
// gesture and flags the row in the Backlog.
func (m Model) IsOrphan(num string) bool {
	for _, n := range m.OrphanNums {
		if n == num {
			return true
		}
	}
	return false
}

// IsAdoptingOrphan reports whether num's adopt gesture has a RecoverFn call
// still in flight, gating a second "A" on the same row for the whole window
// until OrphanAdoptedMsg/OrphanRecoveryMsg lands (issue #1619).
func (m Model) IsAdoptingOrphan(num string) bool {
	for _, n := range m.AdoptingOrphans {
		if n == num {
			return true
		}
	}
	return false
}

// Update applies msg to m and returns the resulting Model. It is pure: the
// adapter and the tea layer do the I/O and translate results into a Msg first.
// It discards the layout updateLayout resolves along the way; callers needing
// that layout call updateLayout directly instead (issue #3018).
func Update(m Model, msg Msg) Model {
	m, _ = updateLayout(m, msg)
	return m
}

// updateLayout is Update's real body. It resolves the layout exactly once per
// message, at the tail, after every mutation resolveLayout's inputs can
// undergo, so the returned layout is always a valid snapshot of the returned
// Model (issue #3018). Branches that need DetailModal.Lines re-wrapped set
// rewrapDetail so the tail handles it instead of resolving a second time.
func updateLayout(m Model, msg Msg) (Model, layout) {
	rewrapDetail := false
	switch msg := msg.(type) {
	case IssuesLoadedMsg:
		m.Err = msg.Err
		if msg.Err == nil {
			m.All = msg.Issues
			m.RecoverableCount = msg.RecoverableCount
		}
	case FilterChangedMsg:
		m.Filter = msg.Filter
	case QuitRequestedMsg:
		m.Mode = ModeQuitConfirm
	case QuitCancelledMsg:
		m.Mode = ModeList
	case GPendingMsg:
		m.PendingG = true
	case GResolvedMsg:
		m.PendingG = false
	case QueueEnterNoticedMsg:
		m.QueueEnterNotice = "no transcript to show"
	case QueueEnterNoticeClearedMsg:
		m.QueueEnterNotice = ""
	case ToastDismissedMsg:
		m.Toast = ""
	case QuitMsg:
		m.Mode = ModeList
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
		if toast := pickTransitionToast(m.Picks, msg.Picks); toast != "" {
			m.Toast = toast
		}
		m.Picks = msg.Picks
	case SidebarLoadedMsg:
		showTranscript := false
		showRaw := false
		sameNumber := m.Sidebar != nil && m.Sidebar.Number == msg.Number
		if sameNumber {
			showTranscript = m.Sidebar.ShowTranscript
			showRaw = m.Sidebar.ShowRaw
		}
		m = saveSidebarPosition(m)
		pos, retained := m.SidebarPositions[msg.Number]
		offset, follow := 0, true
		if retained {
			offset, follow = pos.Offset, pos.Follow
		}
		m.Sidebar = &SidebarState{
			Number:             msg.Number,
			Title:              msg.Title,
			Activity:           msg.Activity,
			TranscriptRendered: msg.Rendered,
			TranscriptRaw:      msg.Raw,
			ShowTranscript:     showTranscript,
			ShowRaw:            showRaw,
			Err:                msg.Err,
			TranscriptErr:      msg.TranscriptErr,
			Notice:             msg.Notice,
			Offset:             offset,
			Follow:             follow,
		}
		m.Sidebar.Lines = sidebarLines(m.Sidebar)
		if follow {
			// Any opened feed follows the newest line (ADR 0030), a retained
			// position included: the Dispatch may have kept working while the
			// sidebar was shut, so a retained Offset would read as following
			// while showing stale lines (issue #1502). The tail clamps it back.
			m.Sidebar.Offset = len(m.Sidebar.Lines)
		}
		if !sameNumber {
			m.Focus = FocusSidebar
		}
	case SidebarToggleMsg:
		if m.Sidebar != nil {
			switch {
			case !m.Sidebar.ShowTranscript:
				m.Sidebar.ShowTranscript = true
			case !m.Sidebar.ShowRaw:
				m.Sidebar.ShowRaw = true
			default:
				m.Sidebar.ShowTranscript = false
				m.Sidebar.ShowRaw = false
			}
			m.Sidebar.Lines = sidebarLines(m.Sidebar)
			if !m.Sidebar.ShowTranscript && m.Sidebar.Follow {
				// The Transcript view's Offset belongs to a form with a
				// different line count, so cycling back to Activity while
				// following must re-seek the bottom rather than keep it.
				m.Sidebar.Offset = len(m.Sidebar.Lines)
			}
		}
	case SidebarActivityMsg:
		if m.Sidebar != nil && m.Sidebar.Number == msg.Number {
			// Changed, not grew: ActivityFeed keys on the latest pass log alone,
			// so a fresh pass's feed can be shorter than the one it follows and
			// a length check would miss the rollover. Equality also skips the
			// no-op refreshes that would re-snap a following operator's scroll.
			changed := !activityEqual(msg.Activity, m.Sidebar.Activity)
			m.Sidebar.Activity = msg.Activity
			if len(msg.Activity) > 0 {
				// The Notice applies only while there is nothing else to show
				// (issue #1621).
				m.Sidebar.Notice = ""
			}
			if changed && !m.Sidebar.ShowTranscript {
				// The #722 Lines cache exists so a re-split happens only on a
				// real content change, not on every keystroke or tick while
				// an Activity sidebar is open on a running Dispatch.
				m.Sidebar.Lines = sidebarLines(m.Sidebar)
				if m.Sidebar.Follow {
					m.Sidebar.Offset = len(m.Sidebar.Lines)
				}
			}
		}
	case SidebarTranscriptMsg:
		if m.Sidebar != nil && m.Sidebar.Number == msg.Number {
			// String equality, not activityEqual: the Transcript render is one
			// blob, not a parsed sequence of entries. It skips the same no-op
			// refreshes SidebarActivityMsg does (issues #1502, #1736).
			changed := msg.Rendered != m.Sidebar.TranscriptRendered || msg.Raw != m.Sidebar.TranscriptRaw
			m.Sidebar.TranscriptRendered = msg.Rendered
			m.Sidebar.TranscriptRaw = msg.Raw
			if changed && m.Sidebar.ShowTranscript {
				// Only while ShowTranscript is active: recomputing Lines under
				// the Activity feed would re-split a form the operator cannot
				// see, the waste the #722 cache exists to avoid.
				m.Sidebar.Lines = sidebarLines(m.Sidebar)
				if m.Sidebar.Follow {
					m.Sidebar.Offset = len(m.Sidebar.Lines)
				}
			}
		}
	case SidebarZoomToggleMsg:
		m.SidebarZoom = !m.SidebarZoom
	case SidebarCloseMsg:
		m = closeSidebar(m)
	case SidebarScrollMsg:
		if m.Sidebar != nil {
			m.Sidebar.Offset += msg.Delta
			if msg.Delta < 0 {
				m.Sidebar.Follow = false
			}
		}
	case SidebarJumpToEndMsg:
		if m.Sidebar != nil {
			m.Sidebar.Follow = true
			m.Sidebar.Offset = len(m.Sidebar.Lines)
		}
	case SidebarJumpToBeginningMsg:
		if m.Sidebar != nil {
			m.Sidebar.Follow = false
			m.Sidebar.Offset = 0
		}
	case FocusListMsg:
		m.Focus = FocusList
	case FocusSidebarMsg:
		if m.Sidebar != nil {
			m.Focus = FocusSidebar
		}
	case ScrollMsg:
		// clampCursor below clamps by total row count alone, not by how many
		// rows the viewport shows, so a pgdown on content that already fits
		// scrolls to the last row and hides the rows above. Current behaviour,
		// not a decision (issue #1060; the viewport-aware fix is #1053).
		m.Offset += msg.Delta
	case TerminateRequestedMsg:
		m.Mode = ModeTerminateConfirm
		m.TerminateConfirm = TerminateConfirmState{Number: msg.Number}
	case TerminateConfirmedMsg:
		m.Mode = ModeList
		m.TerminateConfirm = TerminateConfirmState{}
	case TerminateCancelledMsg:
		m.Mode = ModeList
		m.TerminateConfirm = TerminateConfirmState{}
	case CapMsg:
		m.Cap = msg.Cap
		m.Live = msg.Live
	case StaleStatusMsg:
		m.RebuildStatus = msg.RebuildStatus
		if m.Mode == ModeRebuildOutput && m.RebuildStatus.Output == "" {
			// A rebuild-output pane whose Output empties out has nothing left
			// to show, so close it rather than render blank (issue #1543).
			m.Mode = ModeList
		}
	case OrphanRecoveryMsg:
		m.OrphanRecoveryErr = msg.Err
		m.AdoptingOrphans = removeOrphan(m.AdoptingOrphans, msg.Number)
	case OrphanDetectedMsg:
		m.OrphanNums = msg.Numbers
	case OrphanHeartbeatsMsg:
		m.OrphanHeartbeats = msg.Heartbeats
	case AdoptOrphanStartedMsg:
		if !m.IsAdoptingOrphan(msg.Number) {
			m.AdoptingOrphans = append(m.AdoptingOrphans, msg.Number)
		}
	case OrphanAdoptedMsg:
		m.OrphanNums = removeOrphan(m.OrphanNums, msg.Number)
		m.AdoptingOrphans = removeOrphan(m.AdoptingOrphans, msg.Number)
		// The banner states only the last attempt's outcome, so a later success
		// must not leave an earlier failure's banner up (review finding).
		m.OrphanRecoveryErr = ""
	case RebuildOutputOpenMsg:
		if m.RebuildStatus.Output != "" {
			m.Mode = ModeRebuildOutput
		}
	case RebuildOutputCloseMsg:
		m.Mode = ModeList
	case RebuildOutputScrollMsg:
		if m.Mode == ModeRebuildOutput {
			m.RebuildOutputOffset += msg.Delta
		}
	case RebuildOutputJumpToFirstMsg:
		if m.Mode == ModeRebuildOutput {
			m.RebuildOutputOffset = 0
		}
	case RebuildOutputJumpToLastMsg:
		// Set past the last valid offset; the ModeRebuildOutput clamp below
		// pulls it back to the last page. The pane is cursorless, so there is
		// no cursor-follow to drag Offset into view and that clamp is the jump.
		if m.Mode == ModeRebuildOutput {
			m.RebuildOutputOffset = strings.Count(m.RebuildStatus.Output, "\n") + 1
		}
	case CursorMoveMsg:
		m.Cursor += msg.Delta
	case CursorJumpToFirstMsg:
		m.Cursor = 0
		m.Offset = 0
	case CursorJumpToLastMsg:
		// -1 on an empty Section is safe: clampCursor's n==0 check runs before
		// its cursor<0 check, so it lands on 0 either way.
		m.Cursor = sectionRowCount(m, m.ActiveSection) - 1
	case HelpToggleMsg:
		if m.Mode == ModeHelp {
			m.Mode = ModeList
		} else {
			m.Mode = ModeHelp
		}
	case FilterEditStartMsg:
		m.Mode = ModeFilterEdit
		m.preEditFilter = m.Filter
	case FilterEditConfirmMsg:
		m.Mode = ModeList
	case FilterEditCancelMsg:
		m.Mode = ModeList
		m.Filter = m.preEditFilter
	case SizeChangedMsg:
		m.Width = clampSize(msg.Width)
		m.Height = clampSize(msg.Height)
		if m.DetailModal != nil && !m.DetailModal.Loading && m.DetailModal.Err == nil {
			// Lines is width-dependent, unlike SidebarState.Lines, so a resize
			// must re-wrap it or the modal keeps line breaks sized for a width
			// it no longer has (issue #1632). The tail wraps it against
			// whichever width the render path will use (issues #1758, #1759).
			rewrapDetail = true
		}
	case SectionPrevMsg:
		m = switchSection(m, (m.ActiveSection-1+sectionCount)%sectionCount)
	case SectionNextMsg:
		m = switchSection(m, (m.ActiveSection+1)%sectionCount)
	case SectionJumpMsg:
		m = switchSection(m, msg.Section)
	case DetailModalOpenMsg:
		m.DetailModal = &DetailModalState{Number: msg.Number, Title: msg.Title, Labels: msg.Labels, Loading: true}
	case DetailModalCloseMsg:
		m.DetailModal = nil
	case DetailModalScrollMsg:
		if m.DetailModal != nil {
			m.DetailModal.Offset += msg.Delta
		}
	case DetailModalJumpToFirstMsg:
		if m.DetailModal != nil {
			m.DetailModal.Offset = 0
		}
	case DetailModalJumpToLastMsg:
		// Set past the last valid offset; the DetailModal clamp below pulls it
		// back to the last page that fills the box's scroll budget, as
		// RebuildOutputJumpToLastMsg does (issue #1795).
		if m.DetailModal != nil {
			m.DetailModal.Offset = len(m.DetailModal.Lines)
		}
	case DetailModalLoadedMsg:
		if m.DetailModal != nil && m.DetailModal.Number == msg.Number {
			m.DetailModal.Loading = false
			m.DetailModal.Body = msg.Body
			m.DetailModal.BlockedBy = msg.BlockedBy
			m.DetailModal.Blocks = msg.Blocks
			m.DetailModal.Err = msg.Err
			// Re-wrapped below, off the tail layout (issue #3018).
			rewrapDetail = true
		}
		if msg.Err == nil {
			if m.DetailCache == nil {
				m.DetailCache = make(map[string]DetailModalCache)
			}
			m.DetailCache[msg.Number] = DetailModalCache{Body: msg.Body, BlockedBy: msg.BlockedBy, Blocks: msg.Blocks}
		}
	case DetailCacheInvalidatedMsg:
		m.DetailCache = nil
	}
	m.Width = clampSize(m.Width)
	m.Height = clampSize(m.Height)

	// resolveLayout is the single source of truth for the docked, modal, or
	// fullscreen sidebar decision, the detail modal's scroll budget, and the
	// list's cursor-follow viewport height below (issue #2922).
	l := resolveLayout(m)

	if rewrapDetail && m.DetailModal != nil {
		m.DetailModal.Lines = detailModalLines(l.detailWrapWidth, *m.DetailModal)
	}

	total := sectionRowCount(m, m.ActiveSection)
	m.Cursor = clampCursor(m.Cursor, total)
	m.Offset = clampCursor(m.Offset, total)
	switch msg.(type) {
	case CursorMoveMsg, CursorJumpToLastMsg:
		// height is set directly rather than through SetHeight: pgup/pgdown
		// leaves Offset non-page-capped (issue #1060, tracked as #1053), and
		// SetHeight's clamp-on-shrink would re-cap an Offset a prior ScrollMsg
		// left past the fold the moment the cursor next moves. "G" shares this
		// follow; "gg" does not, setting Offset to 0 directly (issue #1628).
		vp := Viewport{cursor: m.Cursor, offset: m.Offset, height: queueItemBudget(l.compact, l.listContentBudget)}
		vp.MoveCursor(0, total)
		m.Offset = vp.offset
	}

	if m.Sidebar != nil {
		vp := Viewport{offset: m.Sidebar.Offset}
		vp.Scroll(0, len(m.Sidebar.Lines))
		vp.SetHeight(l.sidebarHeight)
		m.Sidebar.Offset = vp.offset
	}

	if m.Mode == ModeRebuildOutput {
		lines := strings.Count(m.RebuildStatus.Output, "\n") + 1
		vp := Viewport{offset: m.RebuildOutputOffset}
		vp.Scroll(0, lines)
		vp.SetHeight(m.Height - headerFooterLines - trailingNewlineRow)
		m.RebuildOutputOffset = vp.offset
	}

	if m.DetailModal != nil {
		// Clamped against whichever row budget the render path will use (issues
		// #1758, #1759). The budget counts wrapped label rows dynamically
		// (issue #1772), because a clamp that disagrees with
		// renderDetailModalContent targets rows the render cannot show.
		vp := Viewport{offset: m.DetailModal.Offset}
		vp.Scroll(0, len(m.DetailModal.Lines))
		vp.SetHeight(l.detailScrollBudget)
		m.DetailModal.Offset = vp.offset
	}

	return m, l
}

// switchSection moves m to Section s, resetting Cursor and Offset to 0 when s
// differs from the active Section so a fresh Section starts at its top row
// (issue #1500). It also closes any open Sidebar, since one left open stays
// pinned to the old Dispatch under a different Section's list (issue #1581).
func switchSection(m Model, s Section) Model {
	if s != m.ActiveSection {
		m.Cursor = 0
		m.Offset = 0
		m = closeSidebar(m)
	}
	m.ActiveSection = s
	return m
}

// closeSidebar clears m.Sidebar, Focus, and SidebarZoom, saving the closed
// Sidebar's scroll and follow position first so a later reopen restores it.
// Shared by SidebarCloseMsg and switchSection (issue #1581).
func closeSidebar(m Model) Model {
	m = saveSidebarPosition(m)
	m.Sidebar = nil
	m.Focus = FocusList
	m.SidebarZoom = false
	return m
}

// sectionRowCount returns the row count of Section s's own list. Cursor and
// Offset clamp against this rather than the whole backlog or Picks slice,
// since only one Section renders at a time (ADR 0030).
func sectionRowCount(m Model, s Section) int {
	if s == SectionBacklog {
		return len(m.Visible())
	}
	return len(sectionPicks(m, s))
}

// sectionPicks returns m.Picks narrowed to the ones pickSection maps onto s,
// in pick order (ADR 0030). Meaningless for SectionBacklog, whose rows are
// Visible() instead.
func sectionPicks(m Model, s Section) []Pick {
	var out []Pick
	for _, p := range m.Picks {
		if pickSection(p.State) == s {
			out = append(out, p)
		}
	}
	return out
}

// clampCursor pulls cursor into [0, n-1], or 0 when n is zero. Every Update
// case shares this, so a list that shrinks under a filter or a refresh never
// leaves the cursor pointing past the end.
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

// saveSidebarPosition records m.Sidebar's Offset and Follow into
// SidebarPositions, keyed by its Number, before SidebarLoadedMsg replaces it
// or SidebarCloseMsg clears it (issue #1502, ADR 0030). A nil Sidebar is a
// no-op.
func saveSidebarPosition(m Model) Model {
	if m.Sidebar == nil {
		return m
	}
	if m.SidebarPositions == nil {
		m.SidebarPositions = make(map[string]SidebarPosition)
	}
	m.SidebarPositions[m.Sidebar.Number] = SidebarPosition{Offset: m.Sidebar.Offset, Follow: m.Sidebar.Follow}
	return m
}

// sidebarLines computes s's currently active form, pre-split on "\n". Called
// Callers invoke it only when the loaded content or the toggle state changes,
// keeping the Lines cache from re-splitting on every keystroke (issue #722).
func sidebarLines(s *SidebarState) []string {
	// Notice only ever accompanies an open with nothing else to show, so this
	// one check covers every toggle state (issue #1621).
	if s.Notice != "" {
		return []string{s.Notice}
	}
	if !s.ShowTranscript {
		lines := make([]string, len(s.Activity))
		for i, a := range s.Activity {
			lines[i] = formatActivityLine(a)
		}
		return lines
	}
	content := s.TranscriptRendered
	if s.ShowRaw {
		content = s.TranscriptRaw
	}
	return strings.Split(content, "\n")
}

// formatActivityLine renders one ActivityLine as the Activity feed shows it:
// the status text with no timestamp. The only clock available is the pass
// log's mtime, which advances to now on every refresh rather than recording
// when the line happened, so an HH:MM:SS prefix would mislead (#1584).
func formatActivityLine(a ActivityLine) string {
	return a.Text
}

// minTerminalDimension is the floor Width and Height clamp to, so a zero or
// negative size from a malformed WindowSizeMsg never leaves Model claiming a
// terminal too small to lay anything out in (issue #842).
const minTerminalDimension = 1

// clampSize pulls a terminal dimension up to minTerminalDimension, so Update
// stays total over any resize input.
func clampSize(dim int) int {
	if dim < minTerminalDimension {
		return minTerminalDimension
	}
	return dim
}

// removePick drops the pick numbered num only while it sits at PickQueued or
// PickHeld. A pick already claiming, running, or settled is left alone.
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

// removeOrphan drops num out of orphans, so an adopted issue stops reading as
// an orphan (issue #1619).
func removeOrphan(orphans []string, num string) []string {
	var out []string
	for _, n := range orphans {
		if n == num {
			continue
		}
		out = append(out, n)
	}
	return out
}

// issueHasLabelContaining reports whether any of iss's labels contains substr,
// case-insensitively. Filter uses substring matching so the list narrows as
// the operator types rather than requiring an exact label.
func issueHasLabelContaining(iss forge.Issue, substr string) bool {
	substr = strings.ToLower(substr)
	for _, l := range iss.Labels {
		if strings.Contains(strings.ToLower(l), substr) {
			return true
		}
	}
	return false
}
