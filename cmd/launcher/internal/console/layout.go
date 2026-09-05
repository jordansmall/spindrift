package console

// arrangement names one of the console's five pane arrangements — the
// decision View's early-return/composite split (view.go) and Update's
// sidebar-viewport clamp (model.go) used to each independently rederive,
// now consolidated into the one enum both consume (issue #2922).
type arrangement int

const (
	// arrangementPlain is the single-list body with no Sidebar and no
	// DetailModal open — every Model starts here.
	arrangementPlain arrangement = iota
	// arrangementSidebarDocked is the Sidebar rendered beside the still-visible
	// list, narrowed to make room (View's docked layout).
	arrangementSidebarDocked
	// arrangementSidebarModal is the Sidebar floating as a bordered box over the
	// full-width list — too narrow/zoomed to dock, but big enough to float.
	arrangementSidebarModal
	// arrangementSidebarFullscreen is the Sidebar taking over the whole
	// terminal — too small even for the floating box.
	arrangementSidebarFullscreen
	// arrangementDetailFullscreen is the open DetailModal taking over the whole
	// terminal because it's too small to float — pre-empts every other
	// arrangement (see layout.sidebarArrangement's doc for why the Sidebar's
	// own arrangement survives this override).
	arrangementDetailFullscreen
)

// boxGeometry is a floating overlay's outer position and size within the
// terminal — compositeOverlay's own (x, y, width, height) parameters,
// resolved once by resolveLayout instead of recomputed at each overlay site.
type boxGeometry struct {
	X, Y, Width, Height int
}

// layout is one Model snapshot's fully resolved render geometry — the
// answer to "which pane arrangement, and how much room does each pane get"
// that View's early-return/composite split and Update's sidebar-viewport
// clamp both consume instead of each independently recomputing (issue
// #2922). resolveLayout is the only place that logic lives.
type layout struct {
	// arrangement is the outer pane arrangement View renders — see
	// layout.sidebarArrangement's doc for why a fullscreen DetailModal can
	// override this field but not that one.
	arrangement arrangement
	// sidebarArrangement is the Sidebar's own docked/modal/fullscreen
	// sub-decision, valid whenever m.Sidebar != nil — computed identically
	// to (and shared with) arrangement's own sidebar case, but never
	// overridden by a fullscreen DetailModal, so it can differ from
	// arrangement exactly when DetailModal's own too-small-to-float case
	// pre-empts arrangement to arrangementDetailFullscreen. This is what
	// Update's sidebar-viewport clamp needs: the sidebar keeps clamping
	// against the arrangement it would render in once the detail modal
	// closes, even while the detail modal is currently pre-empting the
	// screen. Zero value (arrangementPlain) when m.Sidebar == nil.
	sidebarArrangement arrangement
	// compact reports whether the list column is rendered at the
	// sidebar-docked narrowed width (queueNarrowed).
	compact bool
	// bodyBudget is the active Section's table row budget (bodyBudget) —
	// already correct for both the docked and non-docked case.
	bodyBudget int
	// sidebarWidth is the docked sidebar's interior column width
	// (computeSidebarWidth); valid when
	// sidebarArrangement == arrangementSidebarDocked.
	sidebarWidth int
	// listWidth is the docked list column's rendered width (m.Width less
	// sidebarWidth and the docked borders); valid when sidebarArrangement ==
	// arrangementSidebarDocked, the zero value otherwise.
	listWidth int
	// sidebarHeight is the row budget the Sidebar's own render/scroll-clamp
	// needs, computed from sidebarArrangement: bodyBudget minus the docked
	// footer, the floating modal box's scroll budget, or the whole-terminal
	// fullscreen budget. Meaningless when m.Sidebar == nil.
	sidebarHeight int
	// sidebarModalBox is the floating Sidebar modal box's outer position and
	// size (sidebarModalBoxSize/sidebarModalBoxOrigin) — valid when
	// arrangement == arrangementSidebarModal, the zero value otherwise.
	sidebarModalBox boxGeometry
	// detailModalFits reports whether the DetailModal has room to float as
	// a bordered box (detailModalFits); meaningless when m.DetailModal is
	// nil.
	detailModalFits bool
	// detailWrapWidth is the width the detail modal's body wraps against
	// (detailModalWrapWidth); meaningless when m.DetailModal is nil.
	detailWrapWidth int
	// detailScrollBudget is the detail modal's content row budget
	// (detailModalScrollBudget); meaningless when m.DetailModal is nil.
	detailScrollBudget int
	// detailModalBox is the floating detail modal box's outer position and
	// size (detailModalBoxSize/detailModalBoxOrigin) — valid when
	// m.DetailModal != nil && detailModalFits, the zero value otherwise.
	detailModalBox boxGeometry
	// listContentBudget is bodyBudget less ModeList's pinned footer row
	// (listFooterLines), clamped to 0 — zero-cost to compute here since
	// bodyBudget itself is already resolved.
	listContentBudget int
}

// sidebarDocked reports whether m.Sidebar, if present, is in its docked
// sub-state — sidebarArrangement's own arrangementSidebarDocked condition,
// factored out so modeActive can ask the same question without reaching
// into layout (issue #3017: ActiveMode must not depend on resolveLayout).
// Only meaningful when m.Sidebar != nil; callers gate on that themselves.
func sidebarDocked(m Model) bool {
	return sidebarFits(m) && !m.SidebarZoom
}

// sidebarArrangement resolves m.Sidebar's own docked/modal/fullscreen
// sub-decision — the one conditional both layout.arrangement (sidebar
// case) and layout.sidebarArrangement read from. The docked fits/zoom
// condition itself lives in sidebarDocked, not here, so every reader of
// it — this helper, queueNarrowed, bodyBudget, and modeActive's direct
// read (issue #3017) — shares one copy. Only meaningful when
// m.Sidebar != nil; callers gate on that themselves.
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

// resolveLayout computes m's full render geometry as one pure value — no
// rendering side effects beyond calling the existing pure view.go helpers.
func resolveLayout(m Model) layout {
	var l layout
	l.compact = queueNarrowed(m)
	l.bodyBudget = bodyBudget(m)
	l.listContentBudget = l.bodyBudget
	if m.Mode == ModeList {
		// Mirrors renderBody's own "-listFooterLines" reservation for
		// ModeList's pinned footer (issue #1792) — computed from
		// l.bodyBudget rather than a second bodyBudget(m) call, which
		// would otherwise render the boxed header twice per resolveLayout
		// (issue #1035 review).
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
			// Overrides arrangement only — see layout.sidebarArrangement's
			// doc for why that field is left untouched.
			l.arrangement = arrangementDetailFullscreen
		} else {
			boxWidth, boxHeight := detailModalBoxSize(m.Width, m.Height)
			x, y := detailModalBoxOrigin(m.Width, m.Height, boxWidth, boxHeight)
			l.detailModalBox = boxGeometry{X: x, Y: y, Width: boxWidth, Height: boxHeight}
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
// resolveLayout (via sidebarArrangement) uses to decide
// arrangementSidebarDocked over the fullscreen fallback (issue #1500's
// sectionTabsReserved precedent, extended to the sidebar, widened for
// the panel borders by issue #1755).
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
// the source resolveLayout populates layout.compact from; View (via
// l.compact) and Update's cursor-follow both read that one field, so
// neither can disagree about which is showing: a fullscreen sidebar, zoomed
// or too-narrow-to-dock, hides the list entirely, so it never counts as
// "narrowed."
func queueNarrowed(m Model) bool {
	return m.Sidebar != nil && sidebarDocked(m)
}

// bodyBudget returns the row budget left for the active Section's table
// after the header, Section tabs, and any active prompt/error lines — the
// same figure View renders against (issue #1035, ADR 0030). resolveLayout
// calls it once per resolve to populate layout.bodyBudget, which View and
// Update's cursor-follow (issue #1036) both then read from the shared
// layout value instead of each computing it separately.
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
// gate resolveLayout uses to decide layout.detailModalFits and
// arrangement == arrangementDetailFullscreen, and that detailModalWrapWidth/
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
// gate sidebarArrangement checks before resolveLayout resolves to
// arrangementSidebarModal over the small-terminal fullscreen fallback
// (renderSidebarFullscreen), detailModalFits' log-modal analogue (issue
// #1845). Delegates to modalBoxFits, the modal-agnostic gate (issue #1844).
func sidebarModalFits(m Model) bool {
	return modalBoxFits(m.Width, m.Height, sidebarModalBoxMinWidth, sidebarModalBoxMinHeight)
}

// sidebarModalScrollBudget returns the content-line budget the floating log
// modal box actually has room to show for an m.Width x m.Height terminal —
// detailModalScrollBudget's log-modal analogue (issue #1845). resolveLayout
// calls it inside the arrangementSidebarModal case to populate
// layout.sidebarHeight: renderSidebarModalContent budgets its content
// window as innerHeight minus sidebarModalLabelLines and trailingNewlineRow
// (the label row and the footer-hints row), so this must subtract exactly
// that, not the wider headerFooterLines budget the true fullscreen fallback
// (renderSidebarFullscreen, below sidebarModalFits) uses — Update's
// Sidebar.Offset clamp then reads layout.sidebarHeight to stay in lockstep
// with what View renders.
func sidebarModalScrollBudget(m Model) int {
	_, innerHeight := sidebarModalInnerSize(m.Width, m.Height)
	contentBudget := innerHeight - sidebarModalLabelLines - trailingNewlineRow
	if contentBudget < 0 {
		contentBudget = 0
	}
	return contentBudget
}
