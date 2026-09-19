package console

// arrangement names one of the console's five pane arrangements, so View
// (view.go) and Update's sidebar clamp (model.go) stop each rederiving it
// (issue #2922).
type arrangement int

const (
	// arrangementPlain is the single-list body: no Sidebar, no DetailModal.
	arrangementPlain arrangement = iota
	// arrangementSidebarDocked is the Sidebar beside the narrowed list.
	arrangementSidebarDocked
	// arrangementSidebarModal floats the Sidebar over the full-width list when
	// it is zoomed or too narrow to dock.
	arrangementSidebarModal
	// arrangementSidebarFullscreen is the Sidebar taking the whole terminal,
	// too small even for the floating box.
	arrangementSidebarFullscreen
	// arrangementDetailFullscreen is an open DetailModal too small to float,
	// taking the whole terminal. It pre-empts every other arrangement but
	// leaves layout.sidebarArrangement untouched.
	arrangementDetailFullscreen
)

// boxGeometry is a floating overlay's outer position and size, resolved once by
// resolveLayout instead of at each overlay site.
type boxGeometry struct {
	X, Y, Width, Height int
}

// layout is one Model snapshot's fully resolved render geometry. View and
// Update's sidebar clamp both read it instead of each recomputing (issue
// #2922); resolveLayout is the only place that logic lives.
type layout struct {
	arrangement arrangement
	// sidebarArrangement is the Sidebar's own docked/modal/fullscreen
	// sub-decision, which a fullscreen DetailModal never overrides, so Update's
	// clamp keeps clamping against the arrangement the sidebar returns to once
	// the detail modal closes. arrangementPlain when m.Sidebar == nil.
	sidebarArrangement arrangement
	// compact reports whether the list column renders at the docked narrowed
	// width.
	compact bool
	// bodyBudget is the active Section's table row budget.
	bodyBudget int
	// sidebarWidth is zero unless the sidebar is docked.
	sidebarWidth int
	// listWidth is zero unless the sidebar is docked.
	listWidth int
	// sidebarHeight is the Sidebar's render and scroll-clamp row budget.
	sidebarHeight int
	// sidebarModalBox is zero unless arrangement == arrangementSidebarModal.
	sidebarModalBox boxGeometry
	// detailModalFits and the two budgets below are meaningless when
	// m.DetailModal is nil.
	detailModalFits bool
	// detailWrapWidth is the width the detail modal's body wraps against.
	detailWrapWidth int
	// detailScrollBudget is the detail modal's content row budget.
	detailScrollBudget int
	// detailModalBox is zero unless m.DetailModal != nil && detailModalFits.
	detailModalBox boxGeometry
	// listContentBudget is bodyBudget less listFooterLines, clamped to 0.
	listContentBudget int
}

// sidebarDocked reports whether m.Sidebar, if present, is docked. It sits
// outside layout so modeActive can ask without depending on resolveLayout
// (issue #3017). Only meaningful when m.Sidebar != nil; callers gate on that.
func sidebarDocked(m Model) bool {
	return sidebarFits(m) && !m.SidebarZoom
}

// sidebarArrangement resolves m.Sidebar's docked/modal/fullscreen sub-decision.
// Only meaningful when m.Sidebar != nil; callers gate on that.
func sidebarArrangement(m Model) arrangement {
	switch {
	case sidebarDocked(m):
		return arrangementSidebarDocked
	case sidebarModalFits(m):
		return arrangementSidebarModal
	default:
		return arrangementSidebarFullscreen
	}
}

// resolveLayout computes m's full render geometry as one pure value: nothing in
// its call graph renders or touches colorProfile/rendererFor/roleStyle, so line
// counts are predicted rather than rendered and counted (issue #3019).
// TestResolveLayoutCallGraphNeverRenders walks the call graph to enforce that.
func resolveLayout(m Model) layout {
	var l layout
	l.compact = queueNarrowed(m)
	l.bodyBudget = bodyBudget(m)
	l.listContentBudget = l.bodyBudget
	if m.Mode == ModeList {
		// Mirrors renderBody's own reservation for ModeList's pinned footer
		// (issue #1792). Reuses l.bodyBudget rather than calling bodyBudget(m)
		// again, which would re-measure the header text (issue #1035).
		l.listContentBudget -= listFooterLines
		if l.listContentBudget < 0 {
			l.listContentBudget = 0
		}
	}

	if m.Sidebar != nil {
		l.sidebarArrangement = sidebarArrangement(m)
		l.arrangement = l.sidebarArrangement
		switch l.sidebarArrangement {
		case arrangementSidebarDocked:
			l.sidebarWidth = computeSidebarWidth(m.Width)
			l.sidebarHeight = l.bodyBudget - sidebarDockedFooterLines
			l.listWidth = m.Width - l.sidebarWidth - dockedBorderCols
		case arrangementSidebarModal:
			l.sidebarHeight = sidebarModalScrollBudget(m)
			boxWidth, boxHeight := sidebarModalBoxSize(m.Width, m.Height)
			x, y := sidebarModalBoxOrigin(m.Width, m.Height, boxWidth, boxHeight)
			l.sidebarModalBox = boxGeometry{X: x, Y: y, Width: boxWidth, Height: boxHeight}
		case arrangementSidebarFullscreen:
			l.sidebarHeight = m.Height - headerFooterLines - trailingNewlineRow
		}
	}

	if m.DetailModal != nil {
		l.detailModalFits = detailModalFits(m)
		l.detailWrapWidth = detailModalWrapWidth(m)
		l.detailScrollBudget = detailModalScrollBudget(m)
		if !l.detailModalFits {
			// Overrides arrangement only; sidebarArrangement stays untouched.
			l.arrangement = arrangementDetailFullscreen
		} else {
			boxWidth, boxHeight := detailModalBoxSize(m.Width, m.Height)
			x, y := detailModalBoxOrigin(m.Width, m.Height, boxWidth, boxHeight)
			l.detailModalBox = boxGeometry{X: x, Y: y, Width: boxWidth, Height: boxHeight}
		}
	}

	return l
}

// sidebarWidth is the docked sidebar's minimum interior column width, wide
// enough for a realistic Activity status line without wrapping (ADR 0030). The
// bordered panel renders boxBorderCols wider still.
const sidebarWidth = 42

// sidebarMinListWidth is the narrowest the list column can render at beside a
// docked sidebar; below it the sidebar takes over fullscreen (ADR 0030). Sized
// against the wider table, a work Section's (workFixedWidth + extrasBudget,
// currently 60), so a docked row's title keeps about 20 columns everywhere.
const sidebarMinListWidth = 80

// sidebarFits reports whether m.Width has room for the list column, the docked
// sidebar, and dockedBorderCols for the two panels' edges (issues #1500,
// #1755).
func sidebarFits(m Model) bool {
	return m.Width >= sidebarMinListWidth+sidebarWidth+dockedBorderCols
}

// sidebarWidthTargetPercent is the share of terminal width the docked sidebar
// targets once there is room to grow past the sidebarWidth floor (issue #1751).
const sidebarWidthTargetPercent = 45

// computeSidebarWidth returns the docked sidebar's interior column width for a
// terminal totalWidth columns wide (issue #1751). Only meaningful when
// sidebarFits is true: a smaller totalWidth drives the clamp's upper bound
// under its lower one, which fullscreen-fallback callers never observe.
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

// queueNarrowed reports whether the queue list is rendered at the docked
// narrowed width, the trigger for the compact wrapped row form (issue #1752). A
// fullscreen sidebar hides the list, so it never counts as narrowed.
func queueNarrowed(m Model) bool {
	return m.Sidebar != nil && sidebarDocked(m)
}

// bodyBudget returns the row budget left for the active Section's table after
// the header, Section tabs, and any prompt or error lines, the same figure View
// renders against (issue #1035, ADR 0030, issue #1036).
func bodyBudget(m Model) int {
	headerLines, _ := headerGeometry(m)
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
	// Mirrors viewBody's own "-1" (issue #1825): the body is the only budget
	// component free to shrink, so it absorbs the reservation for View's
	// guaranteed trailing "\n".
	budget := m.Height - headerLines - reservedLines - 1
	if budget < 0 {
		budget = 0
	}
	if m.Sidebar != nil && sidebarDocked(m) {
		// Docked, both bordered panels eat boxBorderRows out of the same row
		// band. Without matching that, Update's clamps cap the last page
		// against a taller budget than the bordered render can show, stranding
		// its last lines behind the border forever (issues #1755, #1501,
		// #1502).
		budget -= boxBorderRows
		if budget < 0 {
			budget = 0
		}
	}
	return budget
}

// detailModalFits reports whether the terminal leaves room for a floating
// detail modal box (issue #1759). detailModalWrapWidth and
// detailModalScrollBudget check the same gate, so all three agree on which box
// they measure (issue #1844).
func detailModalFits(m Model) bool {
	return modalBoxFits(m.Width, m.Height, detailModalBoxMinWidth, detailModalBoxMinHeight)
}

// detailModalWrapWidth returns the width the detail modal's body wraps against.
// Gating on detailModalFits makes a resize across the fit threshold rewrap
// against the width the render is about to use (issue #1759).
func detailModalWrapWidth(m Model) int {
	if !detailModalFits(m) {
		return m.Width
	}
	innerWidth, _ := detailModalInnerSize(m.Width, m.Height)
	return innerWidth
}

// detailModalScrollBudget returns the row budget the detail modal's scroll clamp
// windows against (issue #1759). Both branches subtract wrapped label lines
// rather than a fixed row, since labels wrap in the fullscreen renderer too
// (issues #1772, #1832), and both cap them so the clamp matches the "+N more
// labels" the render shows (issue #1778).
func detailModalScrollBudget(m Model) int {
	if !detailModalFits(m) {
		contentBudget := m.Height - detailModalTitleLines - detailModalFooterLines
		if contentBudget < 0 {
			contentBudget = 0
		}
		labelLines := detailModalLabelLinesCappedWith(m.DetailModal.Labels, m.Width, contentBudget, plainText)
		return contentBudget - len(labelLines)
	}
	innerWidth, innerHeight := detailModalInnerSize(m.Width, m.Height)
	contentBudget := innerHeight - detailModalFooterLines
	labelLines := detailModalLabelLinesCappedWith(m.DetailModal.Labels, innerWidth, contentBudget, plainText)
	return contentBudget - len(labelLines)
}

// sidebarModalFits reports whether the terminal leaves room for a floating log
// modal box, the gate sidebarArrangement checks before choosing
// arrangementSidebarModal over the fullscreen fallback (issues #1845, #1844).
func sidebarModalFits(m Model) bool {
	return modalBoxFits(m.Width, m.Height, sidebarModalBoxMinWidth, sidebarModalBoxMinHeight)
}

// sidebarModalScrollBudget returns the content-line budget the floating log
// modal box has room to show (issue #1845). It subtracts exactly what
// renderSidebarModalContent does, not the wider headerFooterLines budget the
// fullscreen fallback uses, so Update's Sidebar.Offset clamp stays in lockstep
// with View.
func sidebarModalScrollBudget(m Model) int {
	_, innerHeight := sidebarModalInnerSize(m.Width, m.Height)
	contentBudget := innerHeight - sidebarModalLabelLines - trailingNewlineRow
	if contentBudget < 0 {
		contentBudget = 0
	}
	return contentBudget
}
