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
	// Sidebar is the open live-tail sidebar, if any — nil when the operator
	// is looking at the backlog/queue alone, docked beside the still-visible
	// list on a wide enough terminal, fullscreen otherwise (ADR 0030,
	// #1501). Replaces the retired fullscreen-only DrillIn pane (#1500).
	Sidebar *SidebarState
	// Focus is which pane keyboard input drives while Sidebar is open — the
	// list or the sidebar itself, moved with h/l (and left/right). Meaningless
	// while Sidebar is nil, where every key targets the list as it always has
	// (ADR 0030, #1501).
	Focus Focus
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
	// SidebarPositions retains each Dispatch's live-tail scroll offset and
	// Follow state across selection changes, keyed by Dispatch number — ADR
	// 0030's "per-issue scroll and follow state are retained across
	// selections" (issue #1502). saveSidebarPosition populates it from
	// whichever Sidebar was open right before SidebarLoadedMsg replaces it
	// or SidebarCloseMsg clears it; SidebarLoadedMsg consults it for the
	// Dispatch it's about to open, so a Dispatch selected before starts
	// again wherever it was left instead of always at the top with Follow
	// re-armed.
	SidebarPositions map[string]SidebarPosition
	// SidebarZoom is whether the operator forced the sidebar to render
	// fullscreen with "z", regardless of sidebarFits' own width check — the
	// "deep reading" zoom ADR 0030 calls for, orthogonal to the
	// narrow-terminal fallback View already applies (issue #1502). Reset to
	// false on SidebarCloseMsg so a later sidebar open on a wide terminal
	// starts docked rather than still forced fullscreen from a prior
	// session.
	SidebarZoom bool
}

// SidebarPosition is one Dispatch's retained live-tail position — the
// SidebarPositions map's value (issue #1502, ADR 0030).
type SidebarPosition struct {
	Offset int
	Follow bool
}

// Focus names which pane keyboard input drives while a sidebar is open — the
// section-switched list's single-list body, or the live-tail sidebar beside
// it (ADR 0030). FocusList, the zero value, matches a fresh Console with no
// sidebar open.
type Focus int

const (
	FocusList Focus = iota
	FocusSidebar
)

// SidebarState is one Dispatch's loaded live-tail sidebar content: its
// condensed Activity feed (ActivityFeed's derivation) and its whole
// Driver-rendered Transcript plus byte-exact raw form, loaded together so the
// Activity/Transcript and rendered/raw toggles both need no further I/O
// (#1501, the sidebar's analogue of the retired DrillInState).
type SidebarState struct {
	Number string
	// Activity is the condensed feed ActivityFeed derived from the
	// Dispatch's most-recent pass log — the sidebar's default view.
	Activity []ActivityLine
	// TranscriptRendered and TranscriptRaw are DrillIn's own two forms of the
	// same Dispatch's whole transcript, shown instead of Activity once
	// ShowTranscript is set.
	TranscriptRendered, TranscriptRaw string
	// ShowTranscript is whether the sidebar shows the Transcript instead of
	// the Activity feed — "t" advances it, and ShowRaw within it, around a
	// three-step cycle (Activity -> Transcript rendered -> Transcript raw ->
	// Activity), preserving the byte-exact raw form's reachability without a
	// second key (#1501).
	ShowTranscript bool
	// ShowRaw is whether the Transcript view shows TranscriptRaw instead of
	// TranscriptRendered — meaningless while ShowTranscript is false.
	ShowRaw bool
	// Err is set when nothing could load at all (e.g. no Driver configured)
	// — shown regardless of ShowTranscript, since neither view has anything
	// to fall back to.
	Err error
	// TranscriptErr is set when only the Transcript's own load failed
	// (DrillIn's error) while Activity loaded independently — shown only
	// while ShowTranscript is true, so a Transcript-only failure never
	// blanks out an otherwise-good Activity feed (#1501 review finding).
	TranscriptErr error
	// Offset is the index of the first visible line in the currently active
	// form — the scroll position, DrillInState.Offset's sidebar analogue.
	Offset int
	// Follow is whether the sidebar auto-scrolls to the newest line as the
	// Activity feed advances — true by default the moment a feed opens;
	// scrolling up detaches it so the operator can review frozen history,
	// and G/End re-attaches it at the bottom (issue #1502, ADR 0030).
	Follow bool
	// Lines is the currently active form (Activity formatted one line per
	// entry, or TranscriptRendered/TranscriptRaw per ShowTranscript/ShowRaw)
	// pre-split on "\n". Update recomputes it only when the content or the
	// toggle state actually changes (SidebarLoadedMsg, SidebarToggleMsg, a
	// grown SidebarActivityMsg), so clampSidebarOffset and the render
	// functions never re-split a
	// multi-megabyte transcript on every keystroke (issue #722's fix,
	// inherited from DrillInState.Lines — see BenchmarkUpdate_DrillInScroll
	// for the recorded before/after).
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
			Activity:           msg.Activity,
			TranscriptRendered: msg.Rendered,
			TranscriptRaw:      msg.Raw,
			ShowTranscript:     showTranscript,
			ShowRaw:            showRaw,
			Err:                msg.Err,
			TranscriptErr:      msg.TranscriptErr,
			Offset:             offset,
			Follow:             follow,
		}
		m.Sidebar.Lines = sidebarLines(m.Sidebar)
		if follow {
			// ADR 0030: the feed "follows the newest line by default" —
			// true of any opened feed, not only a reopen after a close, so
			// this applies whether follow came from a retained position or
			// today's true default. A retained Offset from before a close
			// would otherwise read as "following" while showing stale,
			// non-bottom lines if the Dispatch kept working while the
			// sidebar was shut (review finding on issue #1502). Overshoot;
			// the clamp below pulls it back to the true last page — for
			// content that already fits the viewport, that's still 0
			// (clampSidebarOffset's short-content case), so a short feed's
			// fresh open looks unchanged from before this fix.
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
				// Cycling back to the Activity feed while still following
				// must land on today's bottom, not wherever the Transcript
				// view's own Offset happened to sit — that Offset belongs
				// to a different form with a different line count, and
				// leaving it in place would read as "following" while
				// showing arbitrary, likely non-bottom content.
				m.Sidebar.Offset = len(m.Sidebar.Lines)
			}
		}
	case SidebarActivityMsg:
		if m.Sidebar != nil && m.Sidebar.Number == msg.Number {
			// changed, not "grew": a length-only growth check misses a
			// Dispatch rolling onto a new pass (LogPaths/ActivityFeed key
			// on only the latest pass log, so a fresh fix/conflict-resolve
			// pass's feed can be shorter than the finished pass it follows)
			// — content equality catches that pass rollover the same as an
			// ordinary append, while still skipping syncQueue's frequent
			// no-op refreshes (most calls, between actual writes) that
			// would otherwise re-snap a Follow-ing operator's manual
			// downward scroll (pgdown, which moves Offset without
			// detaching Follow) back to the bottom on every keystroke.
			changed := !activityEqual(msg.Activity, m.Sidebar.Activity)
			m.Sidebar.Activity = msg.Activity
			if changed && !m.Sidebar.ShowTranscript {
				// The #722 Lines cache exists precisely so a re-split only
				// happens on an actual content change — recomputing it on
				// every no-op refresh (most calls, between actual writes)
				// would re-split against every keystroke/tick while an
				// Activity sidebar is open on a running Dispatch.
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
	case FocusListMsg:
		m.Focus = FocusList
	case FocusSidebarMsg:
		if m.Sidebar != nil {
			m.Focus = FocusSidebar
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
	if m.Sidebar != nil {
		// Docked (sidebarFits and not zoomed), the sidebar's actual
		// viewport is the same row budget the list body renders into, not
		// the whole terminal height — renderSidebarDocked subtracts
		// headerFooterLines from bodyBudget(m), same as this clamp must,
		// or the "last page fills the viewport" cap (issue #829) target a
		// taller page than the docked render actually has room to show
		// (#1501 review finding). SidebarZoom forces renderSidebarFullscreen
		// regardless of sidebarFits (View's own decision, mirrored here),
		// so it must also use the whole terminal height, not bodyBudget —
		// otherwise the clamp targets the docked view the operator zoomed
		// away from (review finding on issue #1502).
		height := m.Height
		if sidebarFits(m) && !m.SidebarZoom {
			height = bodyBudget(m)
		}
		clampSidebarOffset(m.Sidebar, height)
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
// different list (issue #1500). It also closes any open Sidebar, the same
// end state as SidebarCloseMsg (including saving its position via
// saveSidebarPosition), since a Sidebar left open survives the switch
// pinned to the old Dispatch under a different Section's list — nonsensical
// over SectionBacklog, whose rows have no Sidebar at all (issue #1581).
// Jumping to the Section that's already active is a no-op on Cursor/Offset
// and leaves an open Sidebar alone, so a repeated "1" or an "H"/"L" that
// wraps back onto the same Section (only possible with a single Section,
// which sectionCount > 1 rules out today) never resets scroll position or
// closes a Sidebar the operator just opened.
func switchSection(m Model, s Section) Model {
	if s != m.ActiveSection {
		m.Cursor = 0
		m.Offset = 0
		m = closeSidebar(m)
	}
	m.ActiveSection = s
	return m
}

// closeSidebar clears m.Sidebar, Focus, and SidebarZoom back to their no-
// sidebar-open state, saving the closed Sidebar's scroll/follow position
// first so a later reopen restores it — the shared close sequence behind
// both SidebarCloseMsg and switchSection (issue #1581).
func closeSidebar(m Model) Model {
	m = saveSidebarPosition(m)
	m.Sidebar = nil
	m.Focus = FocusList
	m.SidebarZoom = false
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

// saveSidebarPosition records m.Sidebar's current Offset/Follow into
// SidebarPositions, keyed by its Number, before SidebarLoadedMsg replaces it
// or SidebarCloseMsg clears it — the write side of per-Dispatch position
// retention (issue #1502, ADR 0030). A nil Sidebar is a no-op, matching
// clampSidebarOffset's own nil convention.
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

// clampSidebarOffset pulls s.Offset into [0, lines-1], further capped so the
// last page fills the viewport instead of leaving it mostly blank (issue
// #829's fix, inherited from clampDrillInOffset) — the sidebar analogue of
// clampCursor, so a scroll commanded past either end of the active form
// (Activity, or Transcript rendered/raw) never leaves an Offset the render
// functions can't slice with, and a toggle whose other form has fewer lines
// still lands somewhere valid (issue #786). A nil s is a no-op — Update calls
// this unconditionally, matching the cursor clamp. Content that already fits
// the viewport (budget >= len(Lines), the short-content case) falls all the
// way back to 0 rather than to the last line. When height is too small to
// fit a page at all (budget == 0), pageMax never undercuts maxOffset, so an
// out-of-range Offset instead lands at the last line, len(Lines)-1.
func clampSidebarOffset(s *SidebarState, height int) {
	if s == nil {
		return
	}
	budget := height - headerFooterLines
	if budget < 0 {
		budget = 0
	}
	maxOffset := len(s.Lines) - 1
	if pageMax := len(s.Lines) - budget; pageMax < maxOffset {
		maxOffset = pageMax
	}
	if maxOffset < 0 {
		maxOffset = 0
	}
	switch {
	case s.Offset < 0:
		s.Offset = 0
	case s.Offset > maxOffset:
		s.Offset = maxOffset
	}
}

// sidebarLines computes s's currently active form, pre-split on "\n" — the
// Activity feed formatted one line per entry when ShowTranscript is false,
// otherwise TranscriptRendered or TranscriptRaw per ShowRaw. Called only when
// the loaded content or the toggle state changes (SidebarLoadedMsg,
// SidebarToggleMsg), matching DrillInState.Lines' recompute-on-change caching.
func sidebarLines(s *SidebarState) []string {
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

// formatActivityLine renders one ActivityLine as the sidebar's Activity feed
// shows it: just the emitted status text, with no timestamp — the only clock
// ActivityFeed has to attach is the pass log's on-disk mtime, which advances
// to ~now on every refresh rather than reflecting when the record actually
// happened, so a precise-looking HH:MM:SS prefix would be misleading (#1584).
func formatActivityLine(a ActivityLine) string {
	return a.Text
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
