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
	// Mode is which modal state exclusively owns the keyboard outside of
	// Sidebar (whose own ownership is the separate Sidebar/Focus/SidebarZoom
	// condition above) — ModeList, the zero value, is a fresh Console's
	// default. Collapses what used to be six independently settable bool/
	// string fields (ShowRebuildOutput, PendingQuit, PendingPick, ShowHelp,
	// FilterEditing, PendingTerminate's non-empty check) into one, so two of
	// them being true at once is no longer representable (issue #1543).
	Mode Mode
	// TerminateConfirm is the pending "X" terminate's own payload — its
	// Number is meaningful only while Mode is ModeTerminateConfirm, the same
	// convention Focus's own doc comment already uses for Sidebar (ADR 0024,
	// issue #649, folded into Mode by issue #1543).
	TerminateConfirm TerminateConfirmState
	// Cap and Live are the session's live parallelism cap and current live
	// count (issue #653, ADR 0023) — zero in a launch-less session, since
	// refreshPickDecorations never sends a CapMsg when there is no Launcher
	// to read them from.
	Cap, Live int
	// RebuildStatus is the launcher's live image-freshness/rebuild state —
	// new launches hold while Stale is true; a running Box rides it out
	// (issue #652). One value replaces the six scalar fields this used to
	// be spread across (issue #1541).
	RebuildStatus RebuildStatus
	// OrphanRecoveryErr is the explicit adopt gesture's last failure, if
	// any — "" when nothing has been adopted yet, or the last adopt
	// succeeded (issue #1218, demoted from a startup-only signal to the
	// gesture's own by issue #1619).
	OrphanRecoveryErr string
	// OrphanNums is the issue numbers startup detection reported running
	// with no live goroutine in this process to account for them — flagged
	// in the backlog so the operator can tell them apart from a Dispatch
	// this session launched, and adopted only through the explicit gesture
	// IsOrphan gates (issue #1619).
	OrphanNums []string
	// OrphanHeartbeats is each orphan-flagged issue's last-parsed status
	// line, keyed by number — syncQueue's orphan analogue of Pick.Heartbeat,
	// read straight off the same on-disk pass log (issue #1621). Absent
	// (zero value "") for a number with no complete heartbeat line yet, same
	// as RunningHeartbeat's own contract.
	OrphanHeartbeats map[string]string
	// AdoptingOrphans is the issue numbers with an adopt gesture's RecoverFn
	// call currently in flight — set the instant "A" fires, before
	// RecoverFn's network round-trip starts, and cleared only once it
	// returns (OrphanAdoptedMsg or OrphanRecoveryMsg). IsAdoptingOrphan
	// gates a second "A" press on the same row for that whole window, not
	// just after RecoverFn returns — the orphan flag alone doesn't clear
	// until completion, so gating on it left the in-flight window open to a
	// second concurrent RecoverFn call racing the first over the same PR
	// (issue #1619 review finding).
	AdoptingOrphans []string
	// RebuildOutputOffset is the rebuild-output pane's scroll position — the
	// index of its first visible line, the pane's analogue of DrillInState's
	// Offset (issue #1128). Meaningful only while Mode is ModeRebuildOutput.
	RebuildOutputOffset int
	// PendingG is whether a lone "g" is waiting on the "gg" leader window,
	// awaiting a trailing "g" (jump-to-first-row) before the gChordTimeout
	// resolves it to a no-op cancel instead — the g-leader mechanism issue
	// #1628 introduces, living on Model (rather than folded into Mode) so it
	// can stay armed across List, ModeSidebar, and ModeRebuildOutput alike —
	// those three modes' own handlers each check it first, rather than it
	// competing with them for exclusive ownership the way Mode's other
	// values do (issue #1543). A non-"g" key cancels without consuming that
	// key — it still gets its own normal handling (issue #1628 AC).
	PendingG bool
	// QueueEnterNotice is a one-shot, human-readable message rendered after
	// Enter is a no-op on a focused work-queue row lacking a Transcript
	// (queued/claiming/held/dissolved, per hasTranscript) — empty otherwise.
	// It clears on the operator's next keypress rather than a timer (issue
	// #998). Stays off Mode for the same reason PendingG does: it's a
	// one-shot overlay on top of ModeList, not a rival claimant to keyboard
	// ownership.
	QueueEnterNotice string
	// Toast is a one-shot, human-readable message rendered after a queued
	// pick transitions to running, settled, failed, or held — "" otherwise
	// (issue #1830). Set by QueueSnapshotMsg's own handler, which diffs the
	// incoming snapshot against the outgoing m.Picks (pickTransitionToast,
	// toast.go) rather than reading a dedicated transition message, since the
	// snapshot is Update's only signal of a Queue-side state change. Clears
	// on the operator's next keypress or an auto-dismiss timer, whichever
	// comes first (ToastDismissedMsg) — the tea layer's generation-pinned
	// tick (mirroring sidebarActivityTickMsg, tea.go) so a stale timer from a
	// toast a newer one already replaced can never clear that newer toast.
	Toast string
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
	// DetailModal is the open Backlog row's ticket detail modal, if any —
	// nil when nothing is open. It floats as a bordered box over the
	// still-rendered list (issue #1758), falling back to a fullscreen
	// takeover only on a terminal too small for a legible box (issue #1759).
	DetailModal *DetailModalState
	// DetailCache holds every ticket detail modal's fully-loaded content
	// this session, keyed by issue number, so reopening the same ticket
	// applies the cached DetailModalLoadedMsg synchronously with no fetch
	// at all — "r" (DetailCacheInvalidatedMsg) is the only thing that clears
	// it (issue #1632).
	DetailCache map[string]DetailModalCache
}

// DetailModalCache is one ticket's fully-loaded detail modal content,
// retained on Model.DetailCache across a close/reopen — everything
// DetailModalLoadedMsg carries except Err, since a failed load is never
// worth caching (issue #1632).
type DetailModalCache struct {
	Body      string
	BlockedBy []BlockerRef
	Blocks    []BlockerRef
}

// DetailModalState is one Backlog issue's open ticket detail modal: the
// number/title/labels a Backlog row already has in hand (set the instant
// Enter opens it), plus the body and Blocked-by/Blocks lists a background
// fetch fills in once it lands — Loading is true for the gap between the
// two (issue #1632).
type DetailModalState struct {
	Number, Title string
	Labels        []string
	Loading       bool
	Body          string
	BlockedBy     []BlockerRef
	Blocks        []BlockerRef
	// Err is the async body/blocker fetch's failure, if any — Body,
	// BlockedBy, and Blocks are all meaningless while it's set.
	Err error
	// Offset is the index of the first visible line in Lines — the modal
	// body's scroll position, SidebarState.Offset's detail-modal analogue.
	Offset int
	// Lines is Body word-wrapped to the modal's width, followed by the
	// formatted Blocked-by/Blocks sections — the flat, scrollable content
	// renderDetailModal windows through one Viewport, computed once when
	// DetailModalLoadedMsg lands rather than re-wrapped on every keystroke
	// (mirrors SidebarState.Lines' #722 caching).
	Lines []string
}

// BlockerRef is one resolved entry in a ticket detail modal's Blocked-by or
// Blocks section: a blocker/blocked issue's number, the source its
// dependency edge was resolved from (native relationship vs body-text
// parsing), its open/closed state, and its title — static text this round,
// no drill-down navigation into the referenced issue's own detail (issue
// #1632).
type BlockerRef struct {
	Number string
	Source forge.DepSource
	State  forge.IssueState
	Title  string
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

// Mode names which modal state exclusively owns the keyboard — the flat
// enum ActiveMode/modePrecedence replace the tea layer's old handleKey
// if-cascade with (issue #1543): the first Mode in modePrecedence whose
// modeActive check passes is the one that owns a keypress. ModeList, the
// zero value, is a fresh Console's default and always reports active, so it
// is always the precedence table's last resort.
//
// This makes the six-plus values Mode covers (RebuildOutput, Help,
// FilterEdit, TerminateConfirm, QuitConfirm, Pick, List) mutually exclusive
// by construction — Model can hold only one at a time. ModeSidebar stays
// outside that guarantee on purpose: its ownership is a condition derived
// from Sidebar/Focus/SidebarZoom (ADR 0030, predating this issue), so a
// Model can still carry a stale Mode value alongside an active Sidebar
// (TestModel_ActiveMode_SidebarBeatsEveryOtherMode exercises exactly this).
// modePrecedence's Sidebar-first check, not the type system, is what keeps
// that combination from ever misrouting a keypress.
type Mode int

const (
	ModeList Mode = iota
	// ModeSidebar is a focused, fullscreen-fallback, or zoomed live-tail
	// sidebar (ADR 0030) — modeActive derives it from Sidebar/Focus/
	// SidebarZoom rather than a stored flag, since those fields already
	// govern View's own layout choice and must never disagree with routing
	// about which one is showing (#1501's sidebarFits precedent).
	ModeSidebar
	// ModeRebuildOutput is the rebuild-output pane open over RebuildStatus.
	// Output (issue #1128) — RebuildOutputOpenMsg only enters it while
	// Output is non-empty, and a later StaleStatusMsg that empties Output
	// back out leaves it, rather than rendering blank over nothing (issue
	// #1543).
	ModeRebuildOutput
	// ModeHelp is the "?" help overlay listing every key the tea layer binds
	// (issue #784).
	ModeHelp
	// ModeFilterEdit is "/" pressed and not yet confirmed (Enter) or
	// cancelled (Esc) — the tea layer routes typed runes into
	// FilterChangedMsg instead of navigation keys while it's active (issue
	// #784).
	ModeFilterEdit
	// ModeTerminateConfirm is the pending "X" terminate awaiting an explicit
	// y/N answer — TerminateConfirm.Number names the issue (ADR 0024, issue
	// #649).
	ModeTerminateConfirm
	// ModeQuitConfirm is the armed quit confirm awaiting the operator's
	// drain/terminate-all/stay answer — only entered when live Dispatches
	// exist at quit time (issue #651, ADR 0023).
	ModeQuitConfirm
	// ModeDetailModal is the fullscreen ticket detail modal a Backlog row's
	// Enter opens (issue #1632) — modeActive derives it from DetailModal
	// alone, the same "derived, not a stored Mode value" shape ModeSidebar
	// uses, since a nil-vs-non-nil *DetailModalState already has to be the
	// source of truth for View's own routing (issue #1632's View check
	// mirrors ModeSidebar's own precedent, predating #1543).
	ModeDetailModal
)

// modePrecedence is the tea layer's old handleKey if-cascade order, now
// data: ActiveMode returns the first Mode here whose modeActive check
// passes, so a new mode joins by appending to this slice and adding one
// modeActive case rather than inserting an if-branch at the right depth
// (issue #1543). ModeDetailModal sits ahead of ModeSidebar, matching the
// order the pre-#1543 if-cascade checked DetailModal in (issue #1632) — the
// two are not expected to ever both be open at once (DetailModal only opens
// from a Backlog row's Enter, Sidebar from a work row's or an orphan
// Backlog row's), but the ordering keeps that assumption from ever being
// load-bearing.
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
// Every case but ModeDetailModal, ModeSidebar, and ModeList reduces to
// Mode's own single stored field, since those three are the only modes
// whose ownership isn't simply "m.Mode equals this value" — DetailModal's
// and Sidebar's are each a derived condition over their own fields (see
// Mode's own doc comment), and List's is the always-true fallback.
func (m Model) modeActive(mode Mode) bool {
	switch mode {
	case ModeDetailModal:
		return m.DetailModal != nil
	case ModeSidebar:
		return m.Sidebar != nil && (m.Focus == FocusSidebar || !sidebarFits(m) || m.SidebarZoom)
	case ModeList:
		return true
	default:
		return m.Mode == mode
	}
}

// ActiveMode returns whichever Mode currently owns the keyboard, per
// modePrecedence — handleKey's whole dispatch decision (issue #1543).
func (m Model) ActiveMode() Mode {
	for _, mode := range modePrecedence {
		if m.modeActive(mode) {
			return mode
		}
	}
	return ModeList
}

// TerminateConfirmState is ModeTerminateConfirm's own payload — the issue
// number "X" armed a pending y/N confirm for (ADR 0024, issue #649).
type TerminateConfirmState struct {
	Number string
}

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
	// Notice is a graceful, non-error explanation shown in place of an
	// empty pane — currently only "no local logs for this dispatch" for an
	// orphan-flagged Dispatch with nothing on disk yet (issue #1621).
	// sidebarLines shows it only while nothing else has loaded; a live
	// SidebarActivityMsg with real content clears it.
	Notice string
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
	// grown SidebarActivityMsg), so Update's tail and the render functions
	// never re-split a multi-megabyte transcript on every keystroke (issue
	// #722's fix,
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

// IsOrphan reports whether num is one of the issues startup detection
// reported as an orphan — a running agent-issue-<N> sandbox this process
// has no live goroutine for (issue #1619). Gates the explicit adopt
// gesture, and flags the row so the Backlog can render it distinguishable
// from a Dispatch this session launched.
func (m Model) IsOrphan(num string) bool {
	for _, n := range m.OrphanNums {
		if n == num {
			return true
		}
	}
	return false
}

// IsAdoptingOrphan reports whether num's adopt gesture has a RecoverFn call
// still in flight — gates a second "A" press on the same row for the whole
// window between the keypress and OrphanAdoptedMsg/OrphanRecoveryMsg
// landing, not just after RecoverFn returns (issue #1619 review finding).
func (m Model) IsAdoptingOrphan(num string) bool {
	for _, n := range m.AdoptingOrphans {
		if n == num {
			return true
		}
	}
	return false
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
			// ADR 0030: the feed "follows the newest line by default" —
			// true of any opened feed, not only a reopen after a close, so
			// this applies whether follow came from a retained position or
			// today's true default. A retained Offset from before a close
			// would otherwise read as "following" while showing stale,
			// non-bottom lines if the Dispatch kept working while the
			// sidebar was shut (review finding on issue #1502). Overshoot;
			// Update's tail pulls it back to the true last page via
			// Viewport's own clamp — for content that already fits the
			// viewport, that's still 0 (the short-content case), so a short
			// feed's fresh open looks unchanged from before this fix.
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
			// ordinary append, while still skipping refreshPickDecorations's frequent
			// no-op refreshes (most calls, between actual writes) that
			// would otherwise re-snap a Follow-ing operator's manual
			// downward scroll (pgdown, which moves Offset without
			// detaching Follow) back to the bottom on every keystroke.
			changed := !activityEqual(msg.Activity, m.Sidebar.Activity)
			m.Sidebar.Activity = msg.Activity
			if len(msg.Activity) > 0 {
				// A graceful "no local logs" Notice only ever applies while
				// there is nothing else to show — real Activity arriving
				// live (the box's first log write landing after an orphan
				// open beat it) supersedes it (issue #1621).
				m.Sidebar.Notice = ""
			}
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
	case SidebarTranscriptMsg:
		if m.Sidebar != nil && m.Sidebar.Number == msg.Number {
			// String equality, not activityEqual's line-slice comparison —
			// the Transcript render is a single blob, not a parsed sequence
			// of distinct entries, and gating the Lines recompute below on
			// this skips the same frequent no-op refreshes (most calls,
			// between actual pass-log writes) SidebarActivityMsg already
			// skips (issue #1502's rationale, extended to Transcript by
			// #1736).
			changed := msg.Rendered != m.Sidebar.TranscriptRendered || msg.Raw != m.Sidebar.TranscriptRaw
			m.Sidebar.TranscriptRendered = msg.Rendered
			m.Sidebar.TranscriptRaw = msg.Raw
			if changed && m.Sidebar.ShowTranscript {
				// Only while ShowTranscript is active: recomputing Lines
				// while the operator is looking at the Activity feed would
				// re-split a form they can't even see, wasted work the #722
				// cache exists to avoid (mirrors SidebarActivityMsg's own
				// !ShowTranscript-gated recompute in reverse).
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
		// Adds Delta unconditionally, then clampCursor below still clamps
		// into [0, len-1] by total row count alone — not by how many rows
		// the viewport can actually show. This is current, undocumented
		// behavior rather than a deliberate design choice: a pgdown on a
		// column whose content already fits on screen still scrolls to the
		// last row instead of no-op'ing, hiding the earlier, already-visible
		// rows (issue #1060; a viewport-aware fix, if wanted, is #1053).
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
			// A rebuild-output pane open over content that then empties out
			// (a fresh StaleStatusMsg with no Output) has nothing left to
			// show — close it rather than leave it rendering blank (issue
			// #1543, retiring the rough edge ShowRebuildOutput's own doc
			// comment used to describe).
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
		// A later successful adopt (of this row or another) must not leave
		// an earlier failed adopt's banner stuck on screen forever — it
		// only ever restates the last attempt's outcome (review finding).
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
		// Set past the last valid offset — the ModeRebuildOutput clamp block
		// below (the same Viewport.SetHeight page-capped maxOffset arithmetic
		// every other Update call already runs) pulls it back to the last
		// page that fills the viewport. Unlike CursorJumpToLastMsg, which
		// drags Offset into view via MoveCursor's cursor-follow, the
		// rebuild-output pane is cursorless, so landing on the page-capped
		// maxOffset directly is the whole jump.
		if m.Mode == ModeRebuildOutput {
			m.RebuildOutputOffset = strings.Count(m.RebuildStatus.Output, "\n") + 1
		}
	case CursorMoveMsg:
		m.Cursor += msg.Delta
	case CursorJumpToFirstMsg:
		m.Cursor = 0
		m.Offset = 0
	case CursorJumpToLastMsg:
		// -1 on an empty Section is still safe: clampCursor's n==0 check
		// below runs before its cursor<0 check, so it lands on 0 either way.
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
			// Lines is width-dependent (wrapText), unlike SidebarState.Lines,
			// which is never wrapped — a resize must re-wrap it or the modal
			// keeps showing line breaks sized for a width it no longer has
			// (issue #1632 review finding). Wrapped against whichever width
			// the render path is about to show — the floating box's interior
			// width, or the fullscreen renderer's raw width below the
			// detailModalFits threshold (issue #1759) — not always the box
			// interior, which is narrower than the terminal (issue #1758).
			m.DetailModal.Lines = detailModalLines(detailModalWrapWidth(m), *m.DetailModal)
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
		// Set past the last valid offset — the unconditional DetailModal
		// clamp block below (the same Viewport.SetHeight page-capped
		// maxOffset arithmetic every other Update call already runs)
		// pulls it back to the last page that fills the box's scroll
		// budget, mirroring RebuildOutputJumpToLastMsg (issue #1795).
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
			m.DetailModal.Lines = detailModalLines(detailModalWrapWidth(m), *m.DetailModal)
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

	total := sectionRowCount(m, m.ActiveSection)
	m.Cursor = clampCursor(m.Cursor, total)
	m.Offset = clampCursor(m.Offset, total)
	switch msg.(type) {
	case CursorMoveMsg, CursorJumpToLastMsg:
		// height is set directly rather than through SetHeight: backlog/queue
		// pgup/pgdown deliberately leaves Offset non-page-capped (issue
		// #1060, tracked separately as #1053), and SetHeight's clamp-on-
		// shrink would otherwise re-cap an Offset a prior ScrollMsg left past
		// the fold the moment the cursor next moves — see renderTable's own
		// reasoning for the same thing. CursorJumpToLastMsg ("G") shares this
		// drag-into-view follow, not CursorJumpToFirstMsg ("gg"), which sets
		// Offset to 0 directly per its own AC (issue #1628).
		vp := Viewport{cursor: m.Cursor, offset: m.Offset, height: queueItemBudget(m, listContentBudget(m))}
		vp.MoveCursor(0, total)
		m.Offset = vp.offset
	}

	if m.Sidebar != nil {
		// Docked (sidebarFits and not zoomed), the sidebar's actual
		// viewport is the same row budget the list body renders into, not
		// the whole terminal height — renderSidebarDocked subtracts
		// sidebarDockedFooterLines from bodyBudget(m), same as this clamp
		// must, or the "last page fills the viewport" cap (issue #829)
		// target a taller page than the docked render actually has room to
		// show (#1501 review finding). SidebarZoom forces
		// renderSidebarFullscreen regardless of sidebarFits (View's own
		// decision, mirrored here), so it must also use the whole terminal
		// height and the wider headerFooterLines budget (its label still
		// renders as an interior row) — otherwise the clamp targets the
		// docked view the operator zoomed away from (review finding on
		// issue #1502). The fullscreen branch also reserves trailingNewlineRow
		// (issue #1841): renderSidebarFullscreen budgets its own content
		// window the same way, and bodyBudget's "-1" already covers the
		// docked branch, so only the fullscreen footerLines needs it added
		// here to keep this clamp in lockstep with what View actually renders.
		height := m.Height
		footerLines := headerFooterLines
		if sidebarFits(m) && !m.SidebarZoom {
			height = bodyBudget(m)
			footerLines = sidebarDockedFooterLines
		} else {
			footerLines += trailingNewlineRow
		}
		vp := Viewport{offset: m.Sidebar.Offset}
		vp.Scroll(0, len(m.Sidebar.Lines))
		vp.SetHeight(height - footerLines)
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
		// Clamped against whichever row budget the render path is about to
		// show — the floating box's own interior budget, or the fullscreen
		// renderer's below the detailModalFits threshold (issue #1759) — not
		// always the box interior, which is shorter than the terminal
		// (issue #1758). detailModalScrollBudget folds the labels line count
		// in dynamically rather than assuming it's always 1 row, since
		// long/many labels now wrap onto further interior rows (issue
		// #1772) — this clamp must budget the body the same way
		// renderDetailModalContent does, or it targets a body budget the
		// render doesn't actually have room to show.
		vp := Viewport{offset: m.DetailModal.Offset}
		vp.Scroll(0, len(m.DetailModal.Lines))
		vp.SetHeight(detailModalScrollBudget(m))
		m.DetailModal.Offset = vp.offset
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
// Update's tail's own nil check before it touches m.Sidebar.
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

// sidebarLines computes s's currently active form, pre-split on "\n" — the
// Activity feed formatted one line per entry when ShowTranscript is false,
// otherwise TranscriptRendered or TranscriptRaw per ShowRaw. Called only when
// the loaded content or the toggle state changes (SidebarLoadedMsg,
// SidebarToggleMsg), matching DrillInState.Lines' recompute-on-change caching.
func sidebarLines(s *SidebarState) []string {
	// Notice only ever accompanies an open with nothing else to show:
	// openSidebarCmd sets it only on its no-local-logs early return, before
	// any Transcript load is attempted, and the SidebarActivityMsg handler
	// clears it the instant real Activity arrives live — so checking Notice
	// alone is enough, covering every toggle state without an empty pane
	// reading as a hang (issue #1621).
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

// formatActivityLine renders one ActivityLine as the sidebar's Activity feed
// shows it: just the emitted status text, with no timestamp — the only clock
// ActivityFeed has to attach is the pass log's on-disk mtime, which advances
// to ~now on every refresh rather than reflecting when the record actually
// happened, so a precise-looking HH:MM:SS prefix would be misleading (#1584).
func formatActivityLine(a ActivityLine) string {
	return a.Text
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

// removeOrphan drops num out of orphans — OrphanAdoptedMsg's own removal, so
// a successfully adopted issue stops reading as an orphan (issue #1619).
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
