// Package console is the Elm-architecture core of the `console` subcommand: a
// pure Model/Update/View, fed by a thin adapter that turns IssueTracker results
// into Msg values. The dependency arrow is one-way — engine packages (forge,
// waves, dispatch, settle, runner) never import console.
package console

import (
	"strings"

	"spindrift.dev/launcher/internal/forge"
)

// Model is the console's whole state. Update is the only function that produces
// a new Model; View is the only function that renders one.
type Model struct {
	All      []forge.Issue
	Filter   string
	Quitting bool
	// Err is the last refresh error, if any. A failed refresh leaves All
	// untouched, so Err surfaces alongside the stale list rather than replacing
	// it with an empty one.
	Err error
	// DogfoodLive is informational only: set once at startup, never gated on.
	DogfoodLive bool
	// Picks is the session's operator queue, in pick order.
	Picks []Pick
	// Sidebar is the open live-tail sidebar, or nil. Docked beside the
	// still-visible list on a wide enough terminal, fullscreen otherwise (ADR
	// 0030).
	Sidebar *SidebarState
	// Focus is which pane keyboard input drives while Sidebar is open.
	// Meaningless while Sidebar is nil, where every key targets the list.
	Focus Focus
	// Mode is which modal state exclusively owns the keyboard outside of Sidebar
	// (whose ownership is the derived condition above). Being one field rather
	// than several flags makes two modes holding at once unrepresentable.
	Mode Mode
	// TerminateConfirm's Number is meaningful only while Mode is
	// ModeTerminateConfirm (ADR 0024).
	TerminateConfirm TerminateConfirmState
	// Cap and Live are the session's live parallelism cap and current live count
	// (ADR 0023) — both zero in a launch-less session, where there is no Launcher
	// to read them from.
	Cap, Live int
	// RecoverableCount is how many open issues carry the tracker's Recoverable
	// label: pre-existing state from a prior run, distinct from the Picks-derived
	// counts, which are this session's own launches (ADR 0039). Set from
	// IssuesLoadedMsg, never recomputed here.
	RecoverableCount int
	// RebuildStatus is the launcher's live image-freshness state — new launches
	// hold while Stale is true; a running Box rides it out.
	RebuildStatus RebuildStatus
	// OrphanRecoveryErr is the adopt gesture's last failure — "" when nothing has
	// been adopted yet, or the last adopt succeeded.
	OrphanRecoveryErr string
	// OrphanNums is the issues startup detection found running with no live
	// goroutine in this process, flagged in the backlog so the operator can tell
	// them from a Dispatch this session launched.
	OrphanNums []string
	// OrphanHeartbeats is each orphan-flagged issue's last-parsed status line,
	// keyed by number. Absent ("") for a number with no complete heartbeat line
	// yet, matching RunningHeartbeat's contract.
	OrphanHeartbeats map[string]string
	// AdoptingOrphans is the issue numbers with a RecoverFn call in flight — set
	// the instant "A" fires, before the network round-trip starts, and cleared
	// only once it returns. The orphan flag itself doesn't clear until
	// completion, so gating a second "A" on that alone left the window open to a
	// second concurrent RecoverFn racing the first over the same PR.
	AdoptingOrphans []string
	// RebuildOutputOffset is meaningful only while Mode is ModeRebuildOutput.
	RebuildOutputOffset int
	// PendingG is whether a lone "g" is waiting on the "gg" leader window. It
	// lives on Model rather than in Mode so it can stay armed across List,
	// ModeSidebar, and ModeRebuildOutput alike — those handlers each check it
	// first rather than it competing for exclusive ownership. A non-"g" key
	// cancels without consuming that key.
	PendingG bool
	// QueueEnterNotice is a one-shot message rendered after Enter is a no-op on a
	// work-queue row lacking a Transcript. It clears on the next keypress rather
	// than a timer, and stays off Mode for the same reason PendingG does: an
	// overlay on ModeList, not a rival claimant to keyboard ownership.
	QueueEnterNotice string
	// Toast is a one-shot message rendered after a queued pick transitions state.
	// QueueSnapshotMsg's handler sets it by diffing the incoming snapshot against
	// the outgoing m.Picks, since that snapshot is Update's only signal of a
	// Queue-side change. Its auto-dismiss tick is generation-pinned in the tea
	// layer so a stale timer can never clear a newer toast.
	Toast string
	// Cursor indexes the highlighted row within the active Section's row list
	// (ADR 0030), always clamped into [0, len(rows)-1] and 0 when that Section is
	// empty. A Section switch resets it — position is deliberately not remembered
	// across switches, so this stays one shared field rather than one per
	// Section.
	Cursor int
	// preEditFilter is Filter's value from just before FilterEditStartMsg,
	// restored verbatim by FilterEditCancelMsg — Update-internal, not rendered.
	preEditFilter string
	// Width and Height are the terminal's current size. Update's unconditional
	// clamp floors both at minTerminalDimension before the first size event
	// arrives.
	Width, Height int
	// Offset is the active Section's scroll offset. CursorMoveMsg keeps it
	// advancing with Cursor so the highlighted row never scrolls off; ScrollMsg
	// moves it directly. Reset to 0 on a Section switch, matching Cursor.
	Offset int
	// ActiveSection is which Section the body renders (ADR 0030). SectionBacklog,
	// the zero value, matches a fresh Console opening on the pick source.
	ActiveSection Section
	// SidebarPositions retains each Dispatch's live-tail offset and Follow state
	// across selection changes (ADR 0030), so a Dispatch selected before starts
	// again where it was left instead of at the top with Follow re-armed.
	SidebarPositions map[string]SidebarPosition
	// SidebarZoom is the operator's "z" fullscreen override, ADR 0030's "deep
	// reading" zoom — orthogonal to the narrow-terminal fallback View already
	// applies. Reset on SidebarCloseMsg so a later open on a wide terminal starts
	// docked.
	SidebarZoom bool
	// DetailModal is the open Backlog row's ticket detail modal, or nil. It floats
	// as a bordered box over the still-rendered list, falling back to a
	// fullscreen takeover only on a terminal too small for a legible box.
	DetailModal *DetailModalState
	// DetailCache holds every detail modal's fully-loaded content this session,
	// so reopening the same ticket applies it synchronously with no fetch. "r"
	// (DetailCacheInvalidatedMsg) is the only thing that clears it.
	DetailCache map[string]DetailModalCache
}

// DetailModalCache is one ticket's fully-loaded detail modal content, retained
// on Model.DetailCache across a close/reopen — everything DetailModalLoadedMsg
// carries except Err, since a failed load is never worth caching.
type DetailModalCache struct {
	Body      string
	BlockedBy []BlockerRef
	Blocks    []BlockerRef
}

// DetailModalState is one Backlog issue's open ticket detail modal: the
// number/title/labels a Backlog row already has in hand (set the instant Enter
// opens it), plus the body and Blocked-by/Blocks lists a background fetch fills
// in once it lands. Loading is true for the gap between the two.
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
	// Offset is the index of the first visible line in Lines.
	Offset int
	// Lines is Body word-wrapped to the modal's width, followed by the formatted
	// Blocked-by/Blocks sections — the flat, scrollable content
	// renderDetailModal windows through one Viewport, computed once when
	// DetailModalLoadedMsg lands rather than re-wrapped on every keystroke.
	Lines []string
}

// BlockerRef is one resolved entry in a ticket detail modal's Blocked-by or
// Blocks section: the referenced issue's number, the source its dependency edge
// was resolved from (native relationship vs body-text parsing), its open/closed
// state, and its title. Static text — there is no drill-down navigation into the
// referenced issue's own detail.
type BlockerRef struct {
	Number string
	Source forge.DepSource
	State  forge.IssueState
	Title  string
}

// SidebarPosition is one Dispatch's retained live-tail position — the
// SidebarPositions map's value (ADR 0030).
type SidebarPosition struct {
	Offset int
	Follow bool
}

// Focus names which pane keyboard input drives while a sidebar is open (ADR
// 0030). FocusList, the zero value, matches a fresh Console with no sidebar
// open.
type Focus int

const (
	FocusList Focus = iota
	FocusSidebar
)

// Mode names which modal state exclusively owns the keyboard: the first Mode in
// modePrecedence whose modeActive check passes owns a keypress. ModeList, the
// zero value, always reports active, so it is the table's last resort.
//
// ModeSidebar deliberately stays outside the one-field mutual exclusion: its
// ownership is derived from Sidebar/Focus/SidebarZoom (ADR 0030), so a Model can
// carry a stale Mode alongside an active Sidebar. modePrecedence's
// Sidebar-first check, not the type system, keeps that from misrouting a key.
type Mode int

const (
	ModeList Mode = iota
	// Derived from Sidebar/Focus/SidebarZoom rather than stored, since those
	// fields already govern View's layout choice and must never disagree with
	// routing about which pane is showing (ADR 0030).
	ModeSidebar
	// Entered only while RebuildStatus.Output is non-empty; a StaleStatusMsg
	// that empties Output leaves it, rather than rendering blank over nothing.
	ModeRebuildOutput
	ModeHelp
	// "/" pressed and not yet confirmed (Enter) or cancelled (Esc) — while
	// active the tea layer routes typed runes into FilterChangedMsg instead of
	// navigation keys.
	ModeFilterEdit
	// Awaits an explicit y/N; TerminateConfirm.Number names the issue (ADR 0024).
	ModeTerminateConfirm
	// Awaits the operator's drain/terminate-all/stay answer — only entered when
	// live Dispatches exist at quit time (ADR 0023).
	ModeQuitConfirm
	// Derived from DetailModal alone, the same shape ModeSidebar uses, since
	// nil-vs-non-nil already has to be the source of truth for View's routing.
	ModeDetailModal
)

// modePrecedence orders the ActiveMode scan. ModeDetailModal sits ahead of
// ModeSidebar; the two are not expected to both be open at once, but the
// ordering keeps that assumption from being load-bearing.
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
// ModeDetailModal and ModeSidebar are derived conditions over their own fields
// (see Mode), ModeList is the always-true fallback, and every other mode reduces
// to the stored m.Mode.
func (m Model) modeActive(mode Mode, layout Layout) bool {
	switch mode {
	case ModeDetailModal:
		return m.DetailModal != nil
	case ModeSidebar:
		return m.Sidebar != nil && (m.Focus == FocusSidebar || layout.SidebarBranch != BranchSidebarDocked)
	case ModeList:
		return true
	default:
		return m.Mode == mode
	}
}

// ActiveMode returns whichever Mode currently owns the keyboard, per
// modePrecedence — handleKey's whole dispatch decision.
func (m Model) ActiveMode() Mode {
	layout := ResolveLayout(m)
	for _, mode := range modePrecedence {
		if m.modeActive(mode, layout) {
			return mode
		}
	}
	return ModeList
}

// TerminateConfirmState is ModeTerminateConfirm's payload — the issue number "X"
// armed a pending y/N confirm for (ADR 0024).
type TerminateConfirmState struct {
	Number string
}

// SidebarState is one Dispatch's loaded live-tail sidebar content: its condensed
// Activity feed and its whole Driver-rendered Transcript plus byte-exact raw
// form, loaded together so the Activity/Transcript and rendered/raw toggles need
// no further I/O.
type SidebarState struct {
	Number, Title string
	// Activity is derived from the Dispatch's most-recent pass log — the
	// sidebar's default view.
	Activity []ActivityLine
	// Shown instead of Activity once ShowTranscript is set.
	TranscriptRendered, TranscriptRaw string
	// "t" advances ShowTranscript, and ShowRaw within it, around a three-step
	// cycle (Activity -> rendered -> raw -> Activity), keeping the byte-exact raw
	// form reachable without a second key.
	ShowTranscript bool
	// Meaningless while ShowTranscript is false.
	ShowRaw bool
	// Err is set when nothing could load at all (e.g. no Driver configured) —
	// shown regardless of ShowTranscript, since neither view has a fallback.
	Err error
	// TranscriptErr is set when only the Transcript's own load failed while
	// Activity loaded independently — shown only while ShowTranscript is true, so
	// a Transcript-only failure never blanks out a good Activity feed.
	TranscriptErr error
	// Notice is a graceful, non-error explanation shown in place of an empty pane
	// — currently only "no local logs for this dispatch" for an orphan-flagged
	// Dispatch with nothing on disk yet. sidebarLines shows it only while nothing
	// else has loaded; a live SidebarActivityMsg with real content clears it.
	Notice string
	// Offset is the index of the first visible line in the currently active form.
	Offset int
	// Follow auto-scrolls to the newest line as the Activity feed advances — true
	// the moment a feed opens; scrolling up detaches it so the operator can
	// review frozen history, and G/End re-attaches it (ADR 0030).
	Follow bool
	// Lines is the currently active form, pre-split on "\n". Update recomputes it
	// only on an actual content or toggle change, so neither Update's tail nor
	// the render functions re-split a multi-megabyte transcript per keystroke.
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

// IsOrphan reports whether num is one of the issues startup detection reported
// as an orphan — a running agent-issue-<N> sandbox this process has no live
// goroutine for. Gates the explicit adopt gesture, and flags the row so the
// Backlog renders it distinguishably from a Dispatch this session launched.
func (m Model) IsOrphan(num string) bool {
	for _, n := range m.OrphanNums {
		if n == num {
			return true
		}
	}
	return false
}

// IsAdoptingOrphan reports whether num's adopt gesture has a RecoverFn call
// still in flight — gating a second "A" press on the same row for the whole
// window between the keypress and OrphanAdoptedMsg/OrphanRecoveryMsg landing.
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
			// ADR 0030: the feed follows the newest line on any open — a
			// retained Offset from before a close would otherwise read as
			// "following" while showing stale lines. Overshoots on purpose;
			// Update's tail clamps it back to the true last page.
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
				// Cycling back to Activity while still following must land
				// on today's bottom: the Transcript view's Offset belongs to
				// a different form with a different line count, and would
				// read as "following" while showing non-bottom content.
				m.Sidebar.Offset = len(m.Sidebar.Lines)
			}
		}
	case SidebarActivityMsg:
		if m.Sidebar != nil && m.Sidebar.Number == msg.Number {
			// "changed", not "grew": ActivityFeed keys on only the latest
			// pass log, so a fresh pass's feed can be SHORTER than the one
			// it follows and a length check would miss the rollover. It
			// still skips no-op refreshes, which would otherwise re-snap a
			// Follow-ing operator's manual scroll back to the bottom.
			changed := !activityEqual(msg.Activity, m.Sidebar.Activity)
			m.Sidebar.Activity = msg.Activity
			if len(msg.Activity) > 0 {
				// The "no local logs" Notice only applies while there is
				// nothing else to show; real Activity arriving live
				// supersedes it.
				m.Sidebar.Notice = ""
			}
			if changed && !m.Sidebar.ShowTranscript {
				// The Lines cache exists so a re-split happens only on an
				// actual content change — otherwise every keystroke/tick
				// re-splits while an Activity sidebar is open on a running
				// Dispatch.
				m.Sidebar.Lines = sidebarLines(m.Sidebar)
				if m.Sidebar.Follow {
					m.Sidebar.Offset = len(m.Sidebar.Lines)
				}
			}
		}
	case SidebarTranscriptMsg:
		if m.Sidebar != nil && m.Sidebar.Number == msg.Number {
			// String equality, not activityEqual's line-slice comparison:
			// the Transcript render is a single blob, not a parsed sequence
			// of entries. Gating the Lines recompute on it skips the same
			// frequent no-op refreshes SidebarActivityMsg already skips.
			changed := msg.Rendered != m.Sidebar.TranscriptRendered || msg.Raw != m.Sidebar.TranscriptRaw
			m.Sidebar.TranscriptRendered = msg.Rendered
			m.Sidebar.TranscriptRaw = msg.Raw
			if changed && m.Sidebar.ShowTranscript {
				// Only while ShowTranscript is active: recomputing Lines
				// while the operator is looking at the Activity feed would
				// re-split a form they can't even see.
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
		// Adds Delta unconditionally; clampCursor below clamps by total row
		// count alone, not by how many rows the viewport can show. Known
		// rough edge rather than a design choice (#1053): a pgdown on
		// content that already fits still scrolls to the last row instead of
		// no-op'ing, hiding the earlier, already-visible rows.
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
			// A pane whose content empties out has nothing left to show —
			// close it rather than leave it rendering blank.
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
		// The banner only ever restates the last attempt's outcome, so a
		// later success must not leave an earlier failure's banner stuck.
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
		// Deliberately set past the last valid offset — the clamp block below
		// pulls it back to the last page that fills the viewport. The
		// rebuild-output pane is cursorless, so unlike CursorJumpToLastMsg
		// landing on that page-capped maxOffset is the whole jump.
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
			// so a resize must re-wrap or the modal keeps showing line breaks
			// sized for a width it no longer has.
			m.DetailModal.Lines = detailModalLines(ResolveLayout(m).DetailWrapWidth, *m.DetailModal)
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
		// Deliberately past the last valid offset — the DetailModal clamp
		// block below pulls it back to the last page that fills the box's
		// scroll budget, mirroring RebuildOutputJumpToLastMsg.
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
			m.DetailModal.Lines = detailModalLines(ResolveLayout(m).DetailWrapWidth, *m.DetailModal)
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

	// ResolveLayout is the single source of truth for the docked/modal/fullscreen
	// sidebar decision, the detail modal's scroll budget, and the list's
	// cursor-follow viewport height below.
	layout := ResolveLayout(m)

	total := sectionRowCount(m, m.ActiveSection)
	m.Cursor = clampCursor(m.Cursor, total)
	m.Offset = clampCursor(m.Offset, total)
	switch msg.(type) {
	case CursorMoveMsg, CursorJumpToLastMsg:
		// height is set directly rather than through SetHeight: backlog/queue
		// pgup/pgdown deliberately leaves Offset non-page-capped (#1053), and
		// SetHeight's clamp-on-shrink would re-cap an Offset a prior ScrollMsg
		// left past the fold. "gg" is absent because it sets Offset to 0.
		vp := Viewport{cursor: m.Cursor, offset: m.Offset, height: queueItemBudget(layout.Compact, layout.ListContentBudget)}
		vp.MoveCursor(0, total)
		m.Offset = vp.offset
	}

	if m.Sidebar != nil {
		vp := Viewport{offset: m.Sidebar.Offset}
		vp.Scroll(0, len(m.Sidebar.Lines))
		vp.SetHeight(layout.SidebarHeight)
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
		// Clamped against whichever row budget the render path is about to use
		// — the floating box's interior, or the fullscreen renderer's. The
		// budget folds in the labels line count dynamically, since long or
		// numerous labels wrap; this must match renderDetailModalContent
		// exactly, or it targets a budget the render has no room to show.
		vp := Viewport{offset: m.DetailModal.Offset}
		vp.Scroll(0, len(m.DetailModal.Lines))
		vp.SetHeight(layout.DetailScrollBudget)
		m.DetailModal.Offset = vp.offset
	}

	return m
}

// switchSection moves m to Section s. A different Section resets Cursor/Offset
// and closes any open Sidebar, which would otherwise stay pinned to the old
// Dispatch under a different Section's list — nonsensical over SectionBacklog,
// whose rows have no Sidebar at all. Jumping to the already-active Section is a
// no-op, so a repeated "1" never resets scroll or closes a just-opened Sidebar.
func switchSection(m Model, s Section) Model {
	if s != m.ActiveSection {
		m.Cursor = 0
		m.Offset = 0
		m = closeSidebar(m)
	}
	m.ActiveSection = s
	return m
}

// closeSidebar clears m.Sidebar, Focus, and SidebarZoom back to their
// no-sidebar-open state, saving the closed Sidebar's scroll/follow position
// first so a later reopen restores it.
func closeSidebar(m Model) Model {
	m = saveSidebarPosition(m)
	m.Sidebar = nil
	m.Focus = FocusList
	m.SidebarZoom = false
	return m
}

// sectionRowCount returns the row count of Section s's own list — the figure
// Cursor and Offset clamp against, since only one Section renders at a time.
func sectionRowCount(m Model, s Section) int {
	if s == SectionBacklog {
		return len(m.Visible())
	}
	return len(sectionPicks(m, s))
}

// sectionPicks returns m.Picks narrowed to the ones pickSection maps onto s, in
// pick order. Meaningless for SectionBacklog, whose rows are Visible() instead.
func sectionPicks(m Model, s Section) []Pick {
	var out []Pick
	for _, p := range m.Picks {
		if pickSection(p.State) == s {
			out = append(out, p)
		}
	}
	return out
}

// clampCursor pulls cursor into [0, n-1], or 0 when n is zero, so a list that
// shrinks never leaves the cursor pointing past the end.
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

// saveSidebarPosition records m.Sidebar's Offset/Follow into SidebarPositions
// before it is replaced or cleared — the write side of per-Dispatch position
// retention (ADR 0030). A nil Sidebar is a no-op.
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
// only when the loaded content or the toggle state changes.
func sidebarLines(s *SidebarState) []string {
	// Notice only ever accompanies an open with nothing else to show — it is set
	// on the no-local-logs early return and cleared the instant real Activity
	// arrives — so checking it alone covers every toggle state without an empty
	// pane reading as a hang.
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

// formatActivityLine renders one ActivityLine as the sidebar shows it: status
// text, no timestamp. The only clock available is the pass log's on-disk mtime,
// which advances to ~now on every refresh rather than reflecting when the record
// happened, so a precise-looking HH:MM:SS prefix would be misleading.
func formatActivityLine(a ActivityLine) string {
	return a.Text
}

// minTerminalDimension is the safe floor Width and Height clamp to, so a
// nonsensical size (zero, or negative from a malformed WindowSizeMsg) never
// leaves Model claiming a terminal too small to lay anything out in.
const minTerminalDimension = 1

// clampSize pulls a terminal dimension up to minTerminalDimension, keeping
// Update total over any resize input.
func clampSize(dim int) int {
	if dim < minTerminalDimension {
		return minTerminalDimension
	}
	return dim
}

// removePick drops the pick numbered num only while it holds at PickQueued or
// PickHeld; one already claiming, running, or settled is left alone.
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

// removeOrphan drops num out of orphans, so a successfully adopted issue stops
// reading as an orphan.
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
// case-insensitively — chosen so Filter narrows as the operator types rather
// than requiring an exact label.
func issueHasLabelContaining(iss forge.Issue, substr string) bool {
	substr = strings.ToLower(substr)
	for _, l := range iss.Labels {
		if strings.Contains(strings.ToLower(l), substr) {
			return true
		}
	}
	return false
}
