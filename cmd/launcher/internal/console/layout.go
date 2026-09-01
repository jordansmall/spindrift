package console

import "strings"

// Branch names one of the console's five pane arrangements — the decision
// View's early-return/composite split (view.go) and Update's sidebar-viewport
// clamp (model.go) both consume instead of each rederiving it.
type Branch int

const (
	// BranchPlain is the single-list body: no Sidebar, no DetailModal.
	BranchPlain Branch = iota
	// BranchSidebarDocked is the Sidebar beside the still-visible list,
	// narrowed to make room.
	BranchSidebarDocked
	// BranchSidebarModal is the Sidebar floating as a bordered box over the
	// full-width list — too narrow/zoomed to dock, but big enough to float.
	BranchSidebarModal
	// BranchSidebarFullscreen is the Sidebar taking over the whole
	// terminal — too small even for the floating box.
	BranchSidebarFullscreen
	// BranchDetailFullscreen is the open DetailModal taking over the whole
	// terminal because it's too small to float — pre-empts every other branch
	// (see Layout.SidebarBranch for why the Sidebar's own branch survives).
	BranchDetailFullscreen
)

// BoxGeometry is a floating overlay's outer position and size within the
// terminal, resolved once by ResolveLayout instead of at each overlay site.
type BoxGeometry struct {
	X, Y, Width, Height int
}

// Layout is one Model snapshot's fully resolved render geometry — the answer
// to "which pane arrangement, and how much room does each pane get" that
// View's early-return/composite split and Update's sidebar-viewport clamp both
// consume. ResolveLayout is the only place that logic lives.
type Layout struct {
	// Branch is the outer pane arrangement View renders — see SidebarBranch
	// for why a fullscreen DetailModal can override this field but not that
	// one.
	Branch Branch
	// SidebarBranch is the Sidebar's own docked/modal/fullscreen
	// sub-decision, valid whenever m.Sidebar != nil, and never overridden by
	// a fullscreen DetailModal — so it differs from Branch exactly when
	// DetailModal pre-empts Branch to BranchDetailFullscreen. That is what
	// Update's sidebar-viewport clamp needs: the sidebar keeps clamping
	// against the arrangement it will render in once the modal closes. Zero
	// value (BranchPlain) when m.Sidebar == nil.
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
	// ListWidth is the docked list column's rendered width — valid when
	// SidebarBranch == BranchSidebarDocked, the zero value otherwise.
	ListWidth int
	// SidebarHeight is the row budget the Sidebar's own render/scroll-clamp
	// needs, computed from SidebarBranch. Meaningless when m.Sidebar == nil.
	SidebarHeight int
	// SidebarModalBox is the floating Sidebar modal box's outer position and
	// size — valid when Branch == BranchSidebarModal, zero otherwise.
	SidebarModalBox BoxGeometry
	// DetailModalFits reports whether the DetailModal has room to float as a
	// bordered box; meaningless when m.DetailModal is nil.
	DetailModalFits bool
	// DetailWrapWidth is the width the detail modal's body wraps against
	// (detailModalWrapWidth); meaningless when m.DetailModal is nil.
	DetailWrapWidth int
	// DetailScrollBudget is the detail modal's content row budget
	// (detailModalScrollBudget); meaningless when m.DetailModal is nil.
	DetailScrollBudget int
	// DetailModalBox is the floating detail modal box's outer position and
	// size — valid when m.DetailModal != nil && DetailModalFits, zero
	// otherwise.
	DetailModalBox BoxGeometry
	// ListContentBudget is Budget less ModeList's pinned footer row
	// (listFooterLines), clamped to 0.
	ListContentBudget int
}

// sidebarBranch resolves m.Sidebar's own docked/modal/fullscreen
// sub-decision — the one conditional both Layout.Branch (sidebar case) and
// Layout.SidebarBranch read from. Only meaningful when m.Sidebar != nil;
// callers gate on that themselves.
func sidebarBranch(m Model) Branch {
	switch {
	case sidebarFits(m) && !m.SidebarZoom:
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
		// ModeList's pinned footer, computed from l.Budget rather than a
		// second bodyBudget(m) call, which would render the boxed header
		// twice per ResolveLayout.
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
// enough for a realistic Activity status line without wrapping (ADR 0030), and
// the floor computeSidebarWidth never shrinks below. Interior content width;
// the bordered panel renders boxBorderCols wider still.
const sidebarWidth = 42

// sidebarMinListWidth is the narrowest the list column can render at and still
// be usable beside a docked sidebar; below it the sidebar takes over
// fullscreen instead of squeezing both columns illegibly (ADR 0030's
// narrow-terminal degradation). Sized against the wider of the two tables so a
// docked row's title keeps a legible ~20 columns on every Section.
const sidebarMinListWidth = 80

// sidebarFits reports whether m.Width has room for the list column (at least
// sidebarMinListWidth) plus the docked sidebar (sidebarWidth) plus
// dockedBorderCols for the two panels' bordered edges — the single gate behind
// BranchSidebarDocked over the fullscreen fallback.
func sidebarFits(m Model) bool {
	return m.Width >= sidebarMinListWidth+sidebarWidth+dockedBorderCols
}

// sidebarWidthTargetPercent is the share of the terminal's total width the
// docked sidebar targets once there's room to grow past its sidebarWidth
// floor — the activity stream should read as a real column, not a sliver.
const sidebarWidthTargetPercent = 45

// computeSidebarWidth returns the docked sidebar's interior column width for a
// terminal totalWidth columns wide: sidebarWidthTargetPercent of totalWidth,
// clamped down to leave the queue list at least sidebarMinListWidth (plus
// dockedBorderCols), and never below the sidebarWidth floor. Only meaningful
// when sidebarFits(m) — below that threshold the clamp's upper bound can fall
// under its lower one, which the fullscreen path never observes.
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

// queueNarrowed reports whether the queue list column is currently rendered at
// the sidebar-docked narrowed width — the trigger for the compact/wrapped
// queue-row form, and the source Layout.Compact is populated from. A
// fullscreen sidebar hides the list entirely, so it never counts as
// "narrowed."
func queueNarrowed(m Model) bool {
	return m.Sidebar != nil && !m.SidebarZoom && sidebarFits(m)
}

// bodyBudget returns the row budget left for the active Section's table after
// the header, Section tabs, and any active prompt/error lines — the same
// figure View renders against (ADR 0030). ResolveLayout calls it once and both
// View and Update's cursor-follow read the result from Layout.Budget.
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
	// Mirrors viewBody's own "-1": the body is the only budget component
	// still free to shrink, so it's where the reservation for View()'s
	// guaranteed trailing "\n" lands.
	budget := m.Height - headerLines - reservedLines - 1
	if budget < 0 {
		budget = 0
	}
	if m.Sidebar != nil && sidebarFits(m) && !m.SidebarZoom {
		// Docked, both bordered panels eat boxBorderRows out of the same row
		// band View renders them into — bodyBudget must match, or Update's
		// scroll/cursor clamps cap the last page against a taller budget than
		// the bordered render has room to show, stranding the last lines
		// behind the border forever (#1755).
		budget -= boxBorderRows
		if budget < 0 {
			budget = 0
		}
	}
	return budget
}

// detailModalFits reports whether m.Width and m.Height leave room for a
// floating detail modal box at least detailModalBoxMin{Width,Height} — the
// gate behind Layout.DetailModalFits, Branch == BranchDetailFullscreen, and
// the wrap-width/scroll-budget helpers below.
func detailModalFits(m Model) bool {
	return modalBoxFits(m.Width, m.Height, detailModalBoxMinWidth, detailModalBoxMinHeight)
}

// detailModalWrapWidth returns the width the detail modal's body should wrap
// against: the floating box's interior width when detailModalFits(m), the
// fullscreen renderer's raw m.Width otherwise — so a resize crossing the fit
// threshold rewraps against whichever width the render path is about to show,
// not a floating-box width that never fit the terminal.
func detailModalWrapWidth(m Model) int {
	if !detailModalFits(m) {
		return m.Width
	}
	innerWidth, _ := detailModalInnerSize(m.Width, m.Height)
	return innerWidth
}

// detailModalScrollBudget returns the row budget the detail modal's scroll
// clamp windows against: the floating box's interior body rows when
// detailModalFits(m), the fullscreen renderer's own budget otherwise. Both
// branches count label rows via detailModalLabelLinesCapped rather than
// assuming a fixed one-row spend, so a ticket with enough labels to fill the
// budget clamps against the same "+N more labels" count the render shows.
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
// floating log modal box at least sidebarModalBoxMin{Width,Height} — the gate
// sidebarBranch checks before resolving to BranchSidebarModal over the
// small-terminal fullscreen fallback.
func sidebarModalFits(m Model) bool {
	return modalBoxFits(m.Width, m.Height, sidebarModalBoxMinWidth, sidebarModalBoxMinHeight)
}

// sidebarModalScrollBudget returns the content-line budget the floating log
// modal box actually has room to show. It must subtract exactly what
// renderSidebarModalContent does -- sidebarModalLabelLines and
// trailingNewlineRow -- not the wider headerFooterLines budget the true
// fullscreen fallback uses, so Update's Sidebar.Offset clamp stays in lockstep
// with what View renders.
func sidebarModalScrollBudget(m Model) int {
	_, innerHeight := sidebarModalInnerSize(m.Width, m.Height)
	contentBudget := innerHeight - sidebarModalLabelLines - trailingNewlineRow
	if contentBudget < 0 {
		contentBudget = 0
	}
	return contentBudget
}
