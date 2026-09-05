package console

import "strings"

// Branch names one of the console's five pane arrangements — the decision
// View's early-return/composite split (view.go) and Update's sidebar-viewport
// clamp (model.go) used to each independently rederive, now consolidated
// into the one enum both consume (issue #2922).
type Branch int

const (
	// BranchPlain is the single-list body with no Sidebar and no
	// DetailModal open — every Model starts here.
	BranchPlain Branch = iota
	// BranchSidebarDocked is the Sidebar rendered beside the still-visible
	// list, narrowed to make room (View's docked layout).
	BranchSidebarDocked
	// BranchSidebarModal is the Sidebar floating as a bordered box over the
	// full-width list — too narrow/zoomed to dock, but big enough to float.
	BranchSidebarModal
	// BranchSidebarFullscreen is the Sidebar taking over the whole
	// terminal — too small even for the floating box.
	BranchSidebarFullscreen
	// BranchDetailFullscreen is the open DetailModal taking over the whole
	// terminal because it's too small to float — pre-empts every other
	// branch (see Layout.SidebarBranch's doc for why the Sidebar's own
	// branch survives this override).
	BranchDetailFullscreen
)

// BoxGeometry is a floating overlay's outer position and size within the
// terminal — compositeOverlay's own (x, y, width, height) parameters,
// resolved once by ResolveLayout instead of recomputed at each overlay site.
type BoxGeometry struct {
	X, Y, Width, Height int
}

// Layout is one Model snapshot's fully resolved render geometry — the
// answer to "which pane arrangement, and how much room does each pane get"
// that View's early-return/composite split and Update's sidebar-viewport
// clamp both consume instead of each independently recomputing (issue
// #2922). ResolveLayout is the only place that logic lives.
type Layout struct {
	// Branch is the outer pane arrangement View renders — see
	// SidebarBranch's doc for why a fullscreen DetailModal can override
	// this field but not that one.
	Branch Branch
	// SidebarBranch is the Sidebar's own docked/modal/fullscreen
	// sub-decision, valid whenever m.Sidebar != nil — computed identically
	// to (and shared with) Branch's own sidebar case, but never overridden
	// by a fullscreen DetailModal, so it can differ from Branch exactly
	// when DetailModal's own too-small-to-float case pre-empts Branch to
	// BranchDetailFullscreen. This is what Update's sidebar-viewport clamp
	// needs: the sidebar keeps clamping against the arrangement it would
	// render in once the detail modal closes, even while the detail modal
	// is currently pre-empting the screen. Zero value (BranchPlain) when
	// m.Sidebar == nil.
	SidebarBranch Branch
	// Compact reports whether the list column is rendered at the
	// sidebar-docked narrowed width (queueNarrowed).
	Compact bool
	// Budget is the active Section's table row budget (bodyBudget) — already
	// correct for both the docked and non-docked case.
	Budget int
	// SidebarWidth is the docked sidebar's interior column width
	// (computeSidebarWidth); valid when SidebarBranch == BranchSidebarDocked.
	SidebarWidth int
	// ListWidth is the docked list column's rendered width (m.Width less
	// SidebarWidth and the docked borders); valid when SidebarBranch ==
	// BranchSidebarDocked, the zero value otherwise.
	ListWidth int
	// SidebarHeight is the row budget the Sidebar's own render/scroll-clamp
	// needs, computed from SidebarBranch: Budget minus the docked footer,
	// the floating modal box's scroll budget, or the whole-terminal
	// fullscreen budget. Meaningless when m.Sidebar == nil.
	SidebarHeight int
	// SidebarModalBox is the floating Sidebar modal box's outer position and
	// size (sidebarModalBoxSize/sidebarModalBoxOrigin) — valid when Branch ==
	// BranchSidebarModal, the zero value otherwise.
	SidebarModalBox BoxGeometry
	// DetailModalFits reports whether the DetailModal has room to float as
	// a bordered box (detailModalFits); meaningless when m.DetailModal is
	// nil.
	DetailModalFits bool
	// DetailWrapWidth is the width the detail modal's body wraps against
	// (detailModalWrapWidth); meaningless when m.DetailModal is nil.
	DetailWrapWidth int
	// DetailScrollBudget is the detail modal's content row budget
	// (detailModalScrollBudget); meaningless when m.DetailModal is nil.
	DetailScrollBudget int
	// DetailModalBox is the floating detail modal box's outer position and
	// size (detailModalBoxSize/detailModalBoxOrigin) — valid when
	// m.DetailModal != nil && DetailModalFits, the zero value otherwise.
	DetailModalBox BoxGeometry
	// ListContentBudget is Budget less ModeList's pinned footer row
	// (listFooterLines), clamped to 0 — zero-cost to compute here since
	// Budget itself is already resolved.
	ListContentBudget int
}

// sidebarDocked reports whether m.Sidebar, if present, is in its docked
// sub-state — sidebarBranch's own BranchSidebarDocked condition, factored
// out so modeActive can ask the same question without reaching into Layout
// (issue #3017: ActiveMode must not depend on ResolveLayout). Only
// meaningful when m.Sidebar != nil; callers gate on that themselves.
func sidebarDocked(m Model) bool {
	return sidebarFits(m) && !m.SidebarZoom
}

// sidebarBranch resolves m.Sidebar's own docked/modal/fullscreen
// sub-decision — the one conditional both Layout.Branch (sidebar case) and
// Layout.SidebarBranch read from. The docked fits/zoom condition itself
// lives in sidebarDocked, not here, so every reader of it — this branch,
// queueNarrowed, bodyBudget, and modeActive's direct read (issue #3017) —
// shares one copy. Only meaningful when m.Sidebar != nil; callers gate on
// that themselves.
func sidebarBranch(m Model) Branch {
	switch {
	case sidebarDocked(m):
		return BranchSidebarDocked
	case sidebarModalFits(m):
		return BranchSidebarModal
	default:
		return BranchSidebarFullscreen
	}
}

// ResolveLayout computes m's full render geometry as one pure value — no
// rendering side effects beyond calling the existing pure view.go helpers.
func ResolveLayout(m Model) Layout {
	var l Layout
	l.Compact = queueNarrowed(m)
	l.Budget = bodyBudget(m)
	l.ListContentBudget = l.Budget
	if m.Mode == ModeList {
		// Mirrors renderBody's own "-listFooterLines" reservation for
		// ModeList's pinned footer (issue #1792) — computed from l.Budget
		// rather than a second bodyBudget(m) call, which would otherwise
		// render the boxed header twice per ResolveLayout (issue #1035
		// review).
		l.ListContentBudget -= listFooterLines
		if l.ListContentBudget < 0 {
			l.ListContentBudget = 0
		}
	}

	if m.Sidebar != nil {
		l.SidebarBranch = sidebarBranch(m)
		l.Branch = l.SidebarBranch
		switch l.SidebarBranch {
		case BranchSidebarDocked:
			l.SidebarWidth = computeSidebarWidth(m.Width)
			l.SidebarHeight = l.Budget - sidebarDockedFooterLines
			l.ListWidth = m.Width - l.SidebarWidth - dockedBorderCols
		case BranchSidebarModal:
			l.SidebarHeight = sidebarModalScrollBudget(m)
			boxWidth, boxHeight := sidebarModalBoxSize(m.Width, m.Height)
			x, y := sidebarModalBoxOrigin(m.Width, m.Height, boxWidth, boxHeight)
			l.SidebarModalBox = BoxGeometry{X: x, Y: y, Width: boxWidth, Height: boxHeight}
		case BranchSidebarFullscreen:
			l.SidebarHeight = m.Height - headerFooterLines - trailingNewlineRow
		}
	}

	if m.DetailModal != nil {
		l.DetailModalFits = detailModalFits(m)
		l.DetailWrapWidth = detailModalWrapWidth(m)
		l.DetailScrollBudget = detailModalScrollBudget(m)
		if !l.DetailModalFits {
			// Overrides Branch only — see SidebarBranch's doc for why that
			// field is left untouched.
			l.Branch = BranchDetailFullscreen
		} else {
			boxWidth, boxHeight := detailModalBoxSize(m.Width, m.Height)
			x, y := detailModalBoxOrigin(m.Width, m.Height, boxWidth, boxHeight)
			l.DetailModalBox = BoxGeometry{X: x, Y: y, Width: boxWidth, Height: boxHeight}
		}
	}

	return l
}

// sidebarWidth is the docked live-tail sidebar's minimum column width — wide
// enough for a realistic Activity status line without wrapping in the
// common case (ADR 0030), and the floor computeSidebarWidth never shrinks
// below regardless of terminal width. This is the sidebar's interior
// content width; its bordered panel renders boxBorderCols wider still.
const sidebarWidth = 42

// sidebarMinListWidth is the narrowest the list column can render at and
// still be usable beside a docked sidebar — the threshold sidebarFits checks
// against, below which the sidebar falls back to a fullscreen takeover
// instead of squeezing both columns illegibly (ADR 0030's narrow-terminal
// degradation). Sized against the wider of the two tables, a work Section's
// (workFixedWidth + extrasBudget, currently 60), so a docked row's title
// keeps a legible ~20 columns on every Section, not just the Backlog's
// narrower one.
const sidebarMinListWidth = 80

// sidebarFits reports whether m.Width has room for the list column (at
// least sidebarMinListWidth) plus the docked sidebar (sidebarWidth) plus
// dockedBorderCols for the two panels' bordered edges — the single gate
// ResolveLayout (via sidebarBranch) uses to decide BranchSidebarDocked over
// the fullscreen fallback (issue #1500's sectionTabsReserved precedent,
// extended to the sidebar, widened for the panel borders by issue #1755).
func sidebarFits(m Model) bool {
	return m.Width >= sidebarMinListWidth+sidebarWidth+dockedBorderCols
}

// sidebarWidthTargetPercent is the share of the terminal's total width the
// docked sidebar targets once there's room to grow past its sidebarWidth
// floor (issue #1751) — the activity stream should read as a real column,
// not a sliver, on a wide terminal.
const sidebarWidthTargetPercent = 45

// computeSidebarWidth returns the docked sidebar's interior column width for
// a terminal totalWidth columns wide: sidebarWidthTargetPercent of
// totalWidth, clamped down to whatever leaves the queue list at least
// sidebarMinListWidth (plus dockedBorderCols for both panels' borders), and
// clamped up to never shrink below the sidebarWidth floor (issue #1751).
// Only meaningful when sidebarFits(m) is true — totalWidth values below that
// threshold can drive the clamp's upper bound under its lower one, which
// callers on the fullscreen fallback path never observe.
func computeSidebarWidth(totalWidth int) int {
	target := totalWidth * sidebarWidthTargetPercent / 100
	if target < sidebarWidth {
		target = sidebarWidth
	}
	if listFloorMax := totalWidth - sidebarMinListWidth - dockedBorderCols; target > listFloorMax {
		target = listFloorMax
	}
	return target
}

// queueNarrowed reports whether the queue list column is currently rendered
// at the sidebar-docked narrowed width rather than the terminal's full width
// — the trigger for the compact/wrapped queue-row form (issue #1752). This is
// the source ResolveLayout populates Layout.Compact from; View (via
// layout.Compact) and Update's cursor-follow both read that one field, so
// neither can disagree about which is showing: a fullscreen sidebar, zoomed
// or too-narrow-to-dock, hides the list entirely, so it never counts as
// "narrowed."
func queueNarrowed(m Model) bool {
	return m.Sidebar != nil && sidebarDocked(m)
}

// bodyBudget returns the row budget left for the active Section's table
// after the header, Section tabs, and any active prompt/error lines — the
// same figure View renders against (issue #1035, ADR 0030). ResolveLayout
// calls it once per resolve to populate Layout.Budget, which View and
// Update's cursor-follow (issue #1036) both then read from the shared
// Layout value instead of each computing it separately.
func bodyBudget(m Model) int {
	headerLines := strings.Count(renderBoxedHeader(m), "\n")
	reservedLines := sectionTabsReserved(m, headerLines)
	if m.Mode == ModeFilterEdit {
		reservedLines++
	}
	if m.Mode == ModeTerminateConfirm {
		reservedLines++
	}
	if m.Mode == ModeQuitConfirm {
		reservedLines++
	}
	if m.QueueEnterNotice != "" {
		reservedLines++
	}
	if m.Toast != "" {
		reservedLines++
	}
	if m.Err != nil {
		reservedLines++
	}
	// Mirrors viewBody's own "-1" (issue #1825): the body is the only
	// budget component still free to shrink, so it's where the reservation
	// for View()'s guaranteed trailing "\n" lands, keeping this figure in
	// agreement with the one View actually renders against.
	budget := m.Height - headerLines - reservedLines - 1
	if budget < 0 {
		budget = 0
	}
	if m.Sidebar != nil && sidebarDocked(m) {
		// Docked, both bordered panels eat boxBorderRows out of the same
		// row band View renders them into — bodyBudget must match, or
		// Update's scroll/cursor clamps cap the last page against a taller
		// budget than the bordered render actually has room to show,
		// stranding the last couple of lines behind the border forever
		// (issue #1755, extending the #1501/#1502 shared-budget invariant).
		budget -= boxBorderRows
		if budget < 0 {
			budget = 0
		}
	}
	return budget
}

// detailModalFits reports whether m.Width and m.Height leave room for a
// floating detail modal box at least detailModalBoxMin{Width,Height} — the
// gate ResolveLayout uses to decide Layout.DetailModalFits and Branch ==
// BranchDetailFullscreen, and that detailModalWrapWidth/
// detailModalScrollBudget also check before computing the wrap width and
// scroll budget against the same box (sidebarFits' detail-modal analogue,
// issue #1759). Delegates to modalBoxFits, the modal-agnostic gate (issue
// #1844).
func detailModalFits(m Model) bool {
	return modalBoxFits(m.Width, m.Height, detailModalBoxMinWidth, detailModalBoxMinHeight)
}

// detailModalWrapWidth returns the width the detail modal's body should wrap
// against: the floating box's interior width when detailModalFits(m), the
// same fullscreen renderer's raw m.Width otherwise — so a resize that
// crosses the fit threshold rewraps against whichever width the render path
// (gated by the same predicate) is actually about to show, instead of a
// floating-box width that never fit the terminal in the first place (issue
// #1759).
func detailModalWrapWidth(m Model) int {
	if !detailModalFits(m) {
		return m.Width
	}
	innerWidth, _ := detailModalInnerSize(m.Width, m.Height)
	return innerWidth
}

// detailModalScrollBudget returns the row budget the detail modal's scroll
// clamp windows against: the floating box's interior body rows
// (detailModalInnerSize, minus its own wrapped labels line count and
// detailModalFooterLines — the same accounting renderDetailModalContent
// does, since a ticket's labels wrap onto further interior rows instead of
// spending a fixed one-row budget, issue #1772) when detailModalFits(m), or
// the fullscreen renderer's own title-line-plus-wrapped-labels budget
// otherwise — detailModalWrapWidth's height analogue, gated by the same
// predicate (issue #1759). Both branches use detailModalLabelLinesCapped,
// not the bare detailModalLabelLines, so a ticket with enough labels to
// fill the content budget clamps against the same "+N more labels" row
// count the render actually shows, not the uncapped wrap it never shows
// (issue #1778) — the fullscreen renderer's own pinned label row wraps and
// brackets the same way the floating box's does (issue #1832), so its
// budget can no longer assume a fixed one-row label spend either.
func detailModalScrollBudget(m Model) int {
	if !detailModalFits(m) {
		contentBudget := m.Height - detailModalTitleLines - detailModalFooterLines
		if contentBudget < 0 {
			contentBudget = 0
		}
		labelLines := detailModalLabelLinesCapped(m.DetailModal.Labels, m.Width, contentBudget)
		return contentBudget - len(labelLines)
	}
	innerWidth, innerHeight := detailModalInnerSize(m.Width, m.Height)
	contentBudget := innerHeight - detailModalFooterLines
	labelLines := detailModalLabelLinesCapped(m.DetailModal.Labels, innerWidth, contentBudget)
	return contentBudget - len(labelLines)
}

// sidebarModalFits reports whether m.Width and m.Height leave room for a
// floating log modal box at least sidebarModalBoxMin{Width,Height} — the
// gate sidebarBranch checks before ResolveLayout resolves to
// BranchSidebarModal over the small-terminal fullscreen fallback
// (renderSidebarFullscreen), detailModalFits' log-modal analogue (issue
// #1845). Delegates to modalBoxFits, the modal-agnostic gate (issue #1844).
func sidebarModalFits(m Model) bool {
	return modalBoxFits(m.Width, m.Height, sidebarModalBoxMinWidth, sidebarModalBoxMinHeight)
}

// sidebarModalScrollBudget returns the content-line budget the floating log
// modal box actually has room to show for an m.Width x m.Height terminal —
// detailModalScrollBudget's log-modal analogue (issue #1845). ResolveLayout
// calls it inside the BranchSidebarModal case to populate
// Layout.SidebarHeight: renderSidebarModalContent budgets its content
// window as innerHeight minus sidebarModalLabelLines and trailingNewlineRow
// (the label row and the footer-hints row), so this must subtract exactly
// that, not the wider headerFooterLines budget the true fullscreen fallback
// (renderSidebarFullscreen, below sidebarModalFits) uses — Update's
// Sidebar.Offset clamp then reads Layout.SidebarHeight to stay in lockstep
// with what View renders.
func sidebarModalScrollBudget(m Model) int {
	_, innerHeight := sidebarModalInnerSize(m.Width, m.Height)
	contentBudget := innerHeight - sidebarModalLabelLines - trailingNewlineRow
	if contentBudget < 0 {
		contentBudget = 0
	}
	return contentBudget
}
