package console

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/termenv"

	"spindrift.dev/launcher/internal/forge"
)

// boxBorderCols and boxBorderRows are the column and row overhead a single
// docked panel's rounded border adds — one per edge, all four sides.
// dockedBorderCols is the docked layout's total: both panels pay
// boxBorderCols.
const (
	boxBorderCols    = 2
	boxBorderRows    = 2
	dockedBorderCols = boxBorderCols * 2
)

// padColumnsToEqualHeight pads the shorter of the list and sidebar columns
// with trailing blank lines, so their bordered boxes close on the same row
// instead of one border floating above a gap while the other continues.
func padColumnsToEqualHeight(list, sidebar string) (string, string) {
	listLines := strings.Count(list, "\n")
	sidebarLines := strings.Count(sidebar, "\n")
	switch {
	case listLines > sidebarLines:
		sidebar += strings.Repeat("\n", listLines-sidebarLines)
	case sidebarLines > listLines:
		list += strings.Repeat("\n", sidebarLines-listLines)
	}
	return list, sidebar
}

// renderBoxedColumn wraps content in a muted (RoleDim) rounded border.
// content's lines must already be clipped to the panel's interior width;
// this only adds the border, sized to exactly that width so adjacent
// panels' edges line up. Under NO_COLOR or a dumb terminal the border
// degrades to ASCII glyphs. Empty content renders no box at all — a
// zero-height budget must not draw a stray empty frame.
//
// With title == "", the top border is a plain rule. With title set, it
// folds the title into the rule itself — "╭─ title ─…─╮". titleRole colors
// the title text distinctly from the border rule; RoleDim matches it.
func renderBoxedColumn(content string, width int, title string, titleRole Role) string {
	if content == "" {
		return ""
	}
	content = strings.TrimSuffix(content, "\n")
	border := lipgloss.RoundedBorder()
	if colorProfile() == termenv.Ascii {
		border = lipgloss.ASCIIBorder()
	}
	boxed := rendererFor(colorProfile()).NewStyle().
		Width(width).
		Border(border).
		BorderForeground(lipgloss.ANSIColor(ansiSlot(RoleDim))).
		Render(content)
	if title == "" {
		return boxed
	}
	_, rest, _ := strings.Cut(boxed, "\n")
	return renderTitledTopBorder(width+boxBorderCols, title, titleRole, border) + "\n" + rest
}

// renderTitledTopBorder builds a bordered panel's top edge at exactly width
// display columns, folding title into the rule: corner and top-rule glyphs
// (already ASCII-degraded by the caller's choice of border), a one-rune
// lead-in, the title, then rule fill out to width. A too-wide title
// truncates with an ellipsis; fill is recomputed from the title's *actual*
// rendered width afterward, since Truncate can stop short of its budget at
// a wide-rune boundary.
func renderTitledTopBorder(width int, title string, titleRole Role, border lipgloss.Border) string {
	inner := width - runewidth.StringWidth(border.TopLeft) - runewidth.StringWidth(border.TopRight)
	if inner < 0 {
		inner = 0
	}
	lead := border.Top + " "
	const tail = " "
	structural := runewidth.StringWidth(lead) + runewidth.StringWidth(tail)
	avail := inner - structural
	if avail < 0 {
		avail = 0
	}
	displayTitle := title
	if runewidth.StringWidth(displayTitle) > avail {
		displayTitle = runewidth.Truncate(displayTitle, avail, "…")
	}
	label := lead + displayTitle + tail
	if runewidth.StringWidth(label) > inner {
		// A panel too narrow even for the lead-in/trailing space can't be
		// fixed by shrinking the title alone — clamp the whole label
		// together so the rule never overflows width.
		label = runewidth.Truncate(label, inner, "")
		label += strings.Repeat(" ", inner-runewidth.StringWidth(label))
		return border.TopLeft + label + border.TopRight
	}
	fill := inner - runewidth.StringWidth(label)
	borderStyle := roleStyle(RoleDim)
	titleStyle := roleStyle(titleRole)
	return borderStyle.Render(border.TopLeft+lead) +
		titleStyle.Render(displayTitle) +
		borderStyle.Render(tail+strings.Repeat(border.Top, fill)+border.TopRight)
}

// View renders m as the text the run loop writes to the terminal: the
// full-width header (wordmark, status line, stale/dogfood alerts), the
// Section tabs, the active Section's aligned table, and any refresh error
// (ADR 0030). An open sidebar docks beside the still-visible list when it
// fits, or takes over fullscreen on a terminal too narrow for both. An open
// detail modal floats as a bordered box over the still-rendered list — the
// same "keep driving while you read" shape — unless the terminal is too
// small for a legible box, which falls back to the fullscreen renderer.
// Both decisions belong to ResolveLayout; View never re-derives them.
func View(m Model) string {
	layout := ResolveLayout(m)
	if layout.Branch == BranchDetailFullscreen {
		return renderDetailModal(*m.DetailModal, m.Width, m.Height)
	}
	if layout.Branch == BranchSidebarFullscreen {
		return renderSidebarFullscreen(*m.Sidebar, m.Width, m.Height)
	}
	base := viewBody(m, layout)
	if layout.Branch == BranchSidebarModal {
		b := layout.SidebarModalBox
		box := renderSidebarModalBox(*m.Sidebar, b.Width, b.Height)
		base = dimBase(padBaseForOverlay(base, m.Width, b.Y+b.Height))
		base = compositeOverlay(base, box, b.X, b.Y)
	}
	if m.DetailModal != nil && layout.DetailModalFits {
		b := layout.DetailModalBox
		box := renderDetailModalBox(*m.DetailModal, b.Width, b.Height)
		base = dimBase(padBaseForOverlay(base, m.Width, b.Y+b.Height))
		base = compositeOverlay(base, box, b.X, b.Y)
	}
	return base
}

// renderBoxedHeader wraps renderHeader in the muted-border panel look, with
// the "spindrift" wordmark folded into the top border rule. It always ends
// in exactly one trailing newline, so callers never special-case boxed vs
// unboxed. bodyBudget's row math must count exactly these rows — border rows
// included — or Update's cursor-follow clamps against a taller viewport than
// View has room to show; both callers therefore share this one helper.
//
// Too narrow, or taller than m.Height allows, and the header renders
// unboxed rather than forcing a degenerate box or overrunning Height. The
// fitness check leaves a row of slack (< m.Height, not <=) for View()'s
// trailing "\n", and measures the boxed render's own line count rather than
// predicting from the unboxed content — renderHeader doesn't pre-wrap to
// m.Width, so a narrow terminal can make the box's word-wrap add lines.
func renderBoxedHeader(m Model) string {
	header := renderHeader(m)
	if headerWidth := m.Width - boxBorderCols; headerWidth > 0 {
		if boxed := renderBoxedColumn(header, headerWidth, headerTitle, RoleDim) + "\n"; strings.Count(boxed, "\n") < m.Height {
			return boxed
		}
	}
	return header
}

// viewBody renders everything View shows below/behind an open detail or log
// modal — the header, Section tabs, and either the docked sidebar layout or
// the plain single-list body — so View can composite a floating box over it.
// A zoomed or too-narrow-to-dock Sidebar never short-circuits into
// renderSidebarFullscreen here: that decision belongs to View's own
// sidebarModal branch, and the docked check below reads Layout.SidebarBranch
// rather than re-deriving fits/zoom.
func viewBody(m Model, layout Layout) string {
	if m.Mode == ModeRebuildOutput {
		return renderRebuildOutputPane(m)
	}
	if m.Mode == ModeHelp {
		return renderHelp()
	}

	var b strings.Builder
	header := renderBoxedHeader(m)
	b.WriteString(header)
	headerLines := strings.Count(header, "\n")
	reservedLines := sectionTabsReserved(m, headerLines)
	if reservedLines > 0 {
		b.WriteString(renderSectionTabs(m))
	}
	if m.Mode == ModeFilterEdit {
		prefix := fmt.Sprintf("/%s  ", m.Filter)
		fmt.Fprintf(&b, "%s%s\n", prefix,
			renderFooterHints(ModeFilterEdit, []string{"enter", "esc"}, footerHintWidth(m.Width, prefix), false))
		reservedLines++
	}
	if m.Mode == ModeTerminateConfirm {
		prefix := fmt.Sprintf("terminate #%s? ", m.TerminateConfirm.Number)
		fmt.Fprintf(&b, "%s%s\n", prefix,
			renderFooterHints(ModeTerminateConfirm, []string{"y"}, footerHintWidth(m.Width, prefix), false))
		reservedLines++
	}
	if m.Mode == ModeQuitConfirm {
		fmt.Fprintf(&b, "%s\n", renderFooterHints(ModeQuitConfirm, []string{"d"}, m.Width, false))
		reservedLines++
	}
	if m.QueueEnterNotice != "" {
		fmt.Fprintf(&b, "%s\n", m.QueueEnterNotice)
		reservedLines++
	}
	if m.Toast != "" {
		fmt.Fprintf(&b, "%s\n", clip(m.Toast, m.Width, false))
		reservedLines++
	}
	if m.Err != nil {
		// The refresh-error line renders after the body, but must still be
		// subtracted from budget up front or a long list plus an error
		// together overflow Height by one line.
		reservedLines++
	}
	// The extra "-1" reserves the row View()'s guaranteed trailing "\n"
	// needs. The body is the only budget component still free to shrink, so
	// the reservation lands here. Without it, a body that *exactly* fills
	// what's left still ends in a "\n" with no row to advance into, which
	// scrolls the pinned top banner off screen just as visibly as an
	// outright overflow.
	budget := m.Height - headerLines - reservedLines - 1
	if budget < 0 {
		budget = 0
	}
	// Computed once against m before any width narrowing below: re-deriving
	// it from listModel would compare an already-narrowed Width against
	// sidebarFits' full-width threshold and misfire.
	compact := layout.Compact
	if layout.SidebarBranch == BranchSidebarDocked {
		width := layout.SidebarWidth
		listModel := m
		listModel.Width = layout.ListWidth
		// bodyBudget(m) already subtracts boxBorderRows for the docked case,
		// so View's render and Update's scroll/cursor clamps agree on how
		// many rows the bordered panels have room for.
		panelBudget := layout.Budget
		list := renderBody(listModel, panelBudget, compact)
		sidebar := renderSidebarDocked(*m.Sidebar, width, panelBudget)
		list, sidebar = padColumnsToEqualHeight(list, sidebar)
		listBox := renderBoxedColumn(list, listModel.Width, "", RoleDim)
		sidebarTitleRole := RoleDim
		if m.Focus == FocusSidebar {
			sidebarTitleRole = RoleAccent
		}
		sidebarBox := renderBoxedColumn(sidebar, width, sidebarLabel(*m.Sidebar), sidebarTitleRole)
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, listBox, sidebarBox))
	} else {
		b.WriteString(renderBody(m, budget, compact))
	}
	if m.Err != nil {
		fmt.Fprintf(&b, "refresh failed: %s\n", m.Err)
	}
	return b.String()
}

// numberColWidth, stateColWidth, and ageColWidth are the work table's fixed
// column widths — all three cells have a bounded vocabulary, so a fixed
// width keeps every row's title column starting in the same screen column
// without measuring content first. stateColWidth fits "terminated", the
// longest PickState word, plus its cursor-side padding.
const (
	numberColWidth = 7
	stateColWidth  = 11
	ageColWidth    = 7
)

// sectionTabsLines is the row budget the Section tabs line costs when it
// renders at all — see sectionTabsReserved.
const sectionTabsLines = 1

// sectionTabsReserved returns sectionTabsLines when the terminal has room
// for the tabs line after headerLines, with one further row of slack for
// View()'s guaranteed trailing "\n" — 0 otherwise, so an extremely short
// terminal never renders more than Height lines total. Shared by View's
// budget calc and bodyBudget so the two can never diverge.
func sectionTabsReserved(m Model, headerLines int) int {
	if m.Height <= headerLines+1 {
		return 0
	}
	return sectionTabsLines
}

// roleForSection returns the Role a Section's content styles with (ADR
// 0031). SectionBacklog, which pickSection never returns, styles as
// RoleAccent.
func roleForSection(s Section) Role {
	switch s {
	case SectionRunning:
		return RoleRunning
	case SectionHeld:
		return RoleHeld
	case SectionSettled:
		return RoleSettled
	case SectionFailed:
		return RoleFailed
	default:
		return RoleAccent
	}
}

// sectionTabsHint is the trailing "how to switch" hint renderSectionTabs
// appends after the five tabs when there's room — keymap's own Footer text,
// not a literal of its own.
var sectionTabsHint = fmt.Sprintf(" [%s,%s]", footerHint(ModeList, "H"), footerHint(ModeList, "1"))

// renderSectionTabs renders the fixed row of five Section tabs above the
// body: each names its direct-jump number and Section, the four work tabs
// carry their row count, the active tab styles by its own Role and the rest
// dim, and a trailing hint spells out how to switch (ADR 0030). Measured
// and clipped as plain text *before* any styling — clipping already-styled
// text with the runewidth-based clip() would miscount ANSI escape bytes as
// display columns and risks truncating mid-sequence. The hint drops first
// on a narrow terminal; bare tabs that still overflow clip with an ellipsis.
func renderSectionTabs(m Model) string {
	labels := make([]string, 0, sectionCount)
	roles := make([]Role, 0, sectionCount)
	for s := Section(0); s < sectionCount; s++ {
		label := fmt.Sprintf("[%d] %s", s+1, s)
		if s != SectionBacklog {
			label = fmt.Sprintf("%s(%d)", label, len(sectionPicks(m, s)))
		}
		labels = append(labels, label)
		role := RoleDim
		if s == m.ActiveSection {
			role = roleForSection(s)
		}
		roles = append(roles, role)
	}
	plain := strings.Join(labels, " ")
	if runewidth.StringWidth(plain+sectionTabsHint) <= m.Width {
		tabs := make([]string, len(labels))
		for i, label := range labels {
			tabs[i] = roleStyle(roles[i]).Render(label)
		}
		return strings.Join(tabs, " ") + roleStyle(RoleDim).Render(sectionTabsHint) + "\n"
	}
	return clip(plain, m.Width, false) + "\n"
}

// listFooterKeys are the ModeList bindings the main list view's pinned
// footer hints — the list's action verbs with no other on-screen
// affordance. Navigation and Section-jump keys are deliberately left out
// (the tabs row already shows the latter inline). Ordered as the
// read-then-act sequence an operator follows, deliberately not keymap's own
// declaration order.
var listFooterKeys = []string{"/", "p", "P", "r", "R"}

// renderBody renders the active Section's table under the header and
// Section tabs (ADR 0030), followed by ModeList's pinned keystroke-hint
// footer. budget is the row count left after the header, tabs, and any
// prompt lines — always a real, already-clamped-to-nonnegative figure from
// View, never Viewport's "unbounded" height==0 case. Only ModeList spends a
// row on the footer; the other Modes reaching here already show their own
// single-line prompt in that same reserved row, so both would double up.
func renderBody(m Model, budget int, compact bool) string {
	if budget <= 0 {
		return ""
	}
	tableBudget := budget
	if m.Mode == ModeList {
		tableBudget -= listFooterLines
	}
	var body string
	if m.ActiveSection == SectionBacklog {
		body = renderBacklogSection(m, tableBudget, compact)
	} else {
		body = renderWorkSection(m, tableBudget, compact)
	}
	if m.Mode != ModeList {
		return body
	}
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return body + renderFooterHints(ModeList, listFooterKeys, m.Width, compact) + "\n"
}

// renderTable writes header followed by rows windowed through vp against
// total, budgeted to itemBudget rows (the header's row already spent). A
// non-positive itemBudget writes no rows and no affordance; the guard lives
// here because Viewport's SetHeight(0) means unbounded, not zero rows. vp's
// height is set directly rather than through SetHeight: that clamp-on-shrink
// is Update's job and is already folded into the stored offset, so
// reapplying it against a fresh vp would misfire as a shrink from unbounded
// and re-cap an offset pgup/pgdown deliberately left uncapped. sep, when
// non-empty, is written between (not after) consecutive rows.
func renderTable(header string, rows []string, vp Viewport, total, itemBudget int, sep string) string {
	var b strings.Builder
	b.WriteString(header)
	if itemBudget <= 0 {
		return b.String()
	}
	vp.height = itemBudget
	w := vp.Window(total)
	shown, moreBelow := w.Shown()
	for i, r := range rows[w.Start : w.Start+shown] {
		if i > 0 && sep != "" {
			b.WriteString(sep)
		}
		b.WriteString(r)
	}
	if moreBelow > 0 {
		fmt.Fprintf(&b, "… %d more below\n", moreBelow)
	}
	return b.String()
}

// extrasBudget is the width reserved for a row's trailing, unaligned
// content — a work row's blocker/reason/heartbeat annotation, or a Backlog
// row's label list. Reserving it up front, rather than letting the title
// column consume the whole remaining width, keeps a joined row at or under
// m.Width once the trailing content is appended; exceeding it wraps the
// line in a real terminal.
const extrasBudget = 30

// backlogFixedWidth is a Backlog row's width outside the title and label
// columns: the cursor marker, the number cell, and the two literal
// separators plus brackets the row format spends (`"%s %s %s [%s]\n"`).
const backlogFixedWidth = 1 + 1 + numberColWidth + 1 + 2 + 1

// renderBacklogSection renders the Backlog Section: one line per visible
// issue (number, title, labels), cursor-marked, under a column-header row —
// ADR 0030's pick source. State and age don't apply to a plain issue. An
// orphan-flagged row's live heartbeat rides in the same bracket as its
// labels, sharing labelsWidth rather than carving out a new column.
func renderBacklogSection(m Model, budget int, compact bool) string {
	if budget <= 0 {
		return ""
	}
	visible := m.Visible()
	titleWidth := m.Width - backlogFixedWidth - extrasBudget
	if titleWidth < 1 {
		titleWidth = 1
	}
	labelsWidth := m.Width - backlogFixedWidth - titleWidth
	if labelsWidth < 0 {
		labelsWidth = 0
	}
	rows := make([]string, 0, len(visible))
	for i, iss := range visible {
		marker := " "
		if i == m.Cursor {
			marker = ">"
		}
		title := SanitizeControlSequences(iss.Title)
		labels := make([]string, len(iss.Labels))
		for j, l := range iss.Labels {
			labels[j] = SanitizeControlSequences(l)
		}
		// A running sandbox with no live goroutine in this process reads as
		// "orphan" alongside its real labels — the only Backlog signal
		// distinguishing it from a Dispatch this session launched, since
		// startup detects but never adopts one.
		if m.IsOrphan(iss.Number) {
			labels = append([]string{"orphan"}, labels...)
			if heartbeat := m.OrphanHeartbeats[iss.Number]; heartbeat != "" {
				labels = append(labels, SanitizeControlSequences(heartbeat))
			}
		}
		if compact {
			rows = append(rows, compactBacklogRow(m.Width, marker, iss.Number, title, labels))
			continue
		}
		rows = append(rows, fmt.Sprintf("%s %s %s [%s]\n", marker, clip("#"+iss.Number, numberColWidth, true), clip(title, titleWidth, true), clipLabels(labels, labelsWidth)))
	}
	// Two spaces, not one, before "labels": each row's label list sits after
	// a literal " [", one column wider than a bare space separator, so this
	// aligns the header with where label text starts, not the bracket.
	headerText := fmt.Sprintf("  %s %s  labels", clip("issue", numberColWidth, true), clip("title", titleWidth, true))
	if compact {
		// The classic header's column words don't describe the compact row's
		// two-line shape — echo its own header-line format instead.
		headerText = "  #  [labels]"
	}
	header := roleStyle(RoleDim).Render(headerText)
	itemBudget := columnItemBudget(budget)
	sep := ""
	if compact {
		itemBudget = compactColumnItemBudget(budget)
		sep = compactQueueSeparator(m.Width)
	}
	vp := Viewport{offset: m.Offset}
	header += positionLabel(vp, itemBudget, len(visible)) + "\n"
	return renderTable(header, rows, vp, len(visible), itemBudget, sep)
}

// workFixedWidth is a work-Section row's width outside the title and extras
// columns: the cursor marker, the number/state/age cells, and the four
// literal single-space separators the row format spends. There is no
// separator between the age cell and the extras, which sit flush against it.
const workFixedWidth = 1 + 1 + numberColWidth + 1 + 1 + stateColWidth + 1 + ageColWidth

// renderWorkSection renders whichever work Section is active: one
// pick-ordered line per Pick, columned as number/title/state/age (ADR 0030),
// the state cell styled by its own Role (ADR 0031). Held's blocker and
// Running's heartbeat render as a trailing annotation after the fixed
// columns, so neither signal is lost nor disturbs the row's alignment.
func renderWorkSection(m Model, budget int, compact bool) string {
	if budget <= 0 {
		return ""
	}
	picks := sectionPicks(m, m.ActiveSection)
	titleWidth := m.Width - workFixedWidth - extrasBudget
	if titleWidth < 1 {
		titleWidth = 1
	}
	extrasWidth := m.Width - workFixedWidth - titleWidth
	if extrasWidth < 0 {
		extrasWidth = 0
	}
	// By sectionPicks' construction every row maps onto m.ActiveSection, so
	// the Role is the same for all of them.
	role := roleForSection(m.ActiveSection)
	rows := make([]string, 0, len(picks))
	for i, p := range picks {
		marker := " "
		if i == m.Cursor {
			marker = ">"
		}
		title := SanitizeControlSequences(p.Title)
		reason := SanitizeControlSequences(p.Reason)
		// A held pick's Reason (blockerFailedPrefix + "#N failed") names the
		// same blocker BlockedBy already does — skip it so a failed blocker
		// isn't named twice on one row.
		showReason := reason != "" && !(p.BlockedBy != "" && strings.HasPrefix(reason, blockerFailedPrefix))
		var extras strings.Builder
		if p.effectiveKind() == KindResearch {
			fmt.Fprintf(&extras, "  %s", researchMarker)
		}
		if p.BlockedBy != "" {
			fmt.Fprintf(&extras, "  (held by %s)", p.BlockedBy)
		}
		if showReason {
			fmt.Fprintf(&extras, "  (%s)", reason)
		}
		if p.Heartbeat != "" {
			fmt.Fprintf(&extras, "  %s", SanitizeControlSequences(p.Heartbeat))
		}
		if compact {
			rows = append(rows, compactWorkRow(m.Width, marker, p, title, role, extras.String()))
			continue
		}
		state := roleStyle(role).Render(clip(p.State.String(), stateColWidth, true))
		rows = append(rows, fmt.Sprintf("%s %s %s %s %s%s\n", marker, clip("#"+p.Number, numberColWidth, true), clip(title, titleWidth, true), state, clip(p.Age, ageColWidth, true), clip(extras.String(), extrasWidth, false)))
	}
	headerText := fmt.Sprintf("  %s %s %s %s", clip("issue", numberColWidth, true), clip("title", titleWidth, true), clip("state", stateColWidth, true), "age")
	if compact {
		// The classic header's column words don't describe the compact row's
		// two-line shape — echo its own header-line format instead.
		headerText = "  # · state · age"
	}
	header := roleStyle(RoleDim).Render(headerText)
	itemBudget := columnItemBudget(budget)
	sep := ""
	if compact {
		itemBudget = compactColumnItemBudget(budget)
		sep = compactQueueSeparator(m.Width)
	}
	vp := Viewport{offset: m.Offset}
	header += positionLabel(vp, itemBudget, len(picks)) + "\n"
	return renderTable(header, rows, vp, len(picks), itemBudget, sep)
}

// compactQueueIndent is the left indent the compact form's title line sits
// at, under its own header line.
const compactQueueIndent = "  "

// compactQueueSeparatorGlyph is the compact form's per-issue delimiter — a
// faint rule so the two-line stacked entries don't run together.
const compactQueueSeparatorGlyph = "─"

// compactQueueSeparator renders one row's worth of that delimiter at width
// display columns, styled RoleDim (ADR 0031) so it reads as chrome.
func compactQueueSeparator(width int) string {
	if width < 1 {
		width = 1
	}
	return roleStyle(RoleDim).Render(strings.Repeat(compactQueueSeparatorGlyph, width)) + "\n"
}

// compactRowLines is the physical line count one compact entry's
// header+title block spends, excluding the separator renderTable inserts
// between (not after) entries.
const compactRowLines = 2

// compactColumnItemBudget is columnItemBudget's compact-form counterpart: it
// converts a Section's row budget (header row included) into how many
// compact entries fit. Each spends compactRowLines lines, plus one more per
// entry after the first for its separator — N*compactRowLines + (N-1) <=
// available, i.e. N <= (available+1)/(compactRowLines+1).
func compactColumnItemBudget(columnBudget int) int {
	available := columnBudget - 1 // header row
	if available <= 0 {
		return 0
	}
	return (available + 1) / (compactRowLines + 1)
}

// compactWorkRow renders one work-Section Pick in the compact form: a "#num
// · state · age" header line with the cursor marker and trailing extras,
// then the title on a whole line of its own so a squeezed column stops
// clipping it to a sliver. title must be pre-sanitized.
func compactWorkRow(width int, marker string, p Pick, title string, role Role, extras string) string {
	stateText := clip(p.State.String(), stateColWidth, false)
	// number and age reuse the classic form's column budgets as a defensive
	// cap; real values never approach it. clip("#"+p.Number, ...), not
	// "#"+clip(p.Number, ...), so the cap is numberColWidth total rather
	// than numberColWidth plus an unclipped literal "#".
	number := clip("#"+p.Number, numberColWidth, false)
	age := clip(p.Age, ageColWidth, false)
	// Measured plain, before roleStyle wraps stateText in ANSI escapes, so
	// extrasWidth counts display columns and not escape bytes.
	plainPrefix := fmt.Sprintf("%s %s · %s · %s", marker, number, stateText, age)
	extrasWidth := width - runewidth.StringWidth(plainPrefix)
	if extrasWidth < 0 {
		extrasWidth = 0
	}
	header := fmt.Sprintf("%s %s · %s · %s%s\n", marker, number, roleStyle(role).Render(stateText), age, clip(extras, extrasWidth, false))
	return header + compactQueueTitleLine(width, title)
}

// compactQueueTitleLine renders the compact form's title line — an indent,
// then title given the whole remainder of width. Shared by compactWorkRow
// and compactBacklogRow.
func compactQueueTitleLine(width int, title string) string {
	titleWidth := width - runewidth.StringWidth(compactQueueIndent)
	if titleWidth < 1 {
		titleWidth = 1
	}
	return compactQueueIndent + clip(title, titleWidth, false) + "\n"
}

// compactBacklogRow renders one Backlog issue in the compact form: a "#num
// [labels]" header line with the cursor marker, then the title on a whole
// line of its own. title and labels must be pre-sanitized.
func compactBacklogRow(width int, marker, number, title string, labels []string) string {
	// clip("#"+number, ...), not "#"+clip(number, ...), matching the classic
	// row and kept in sync with labelsWidth's reservation below.
	number = clip("#"+number, numberColWidth, false)
	// " " before number, " [" and "]" around labels: four literal columns
	// the "%s %s [%s]\n" format spends outside marker/number/labels.
	const backlogHeaderLiteralWidth = 4
	labelsWidth := width - runewidth.StringWidth(marker) - runewidth.StringWidth(number) - backlogHeaderLiteralWidth
	if labelsWidth < 0 {
		labelsWidth = 0
	}
	header := fmt.Sprintf("%s %s [%s]\n", marker, number, clipLabels(labels, labelsWidth))
	return header + compactQueueTitleLine(width, title)
}

// truncateWithEllipsis fits s into exactly width display columns, marking
// the cut with a trailing "…". runewidth.Truncate can stop one column short
// when a wide (2-column) rune straddles the boundary, so the result is
// re-measured and padded back to exactly width rather than trusted as-is.
func truncateWithEllipsis(s string, width int) string {
	if width <= 1 {
		return runewidth.Truncate(s, width, "")
	}
	cut := runewidth.Truncate(s, width-1, "") + "…"
	return cut + strings.Repeat(" ", width-runewidth.StringWidth(cut))
}

// clip fits s into width display columns (not runes — a wide CJK rune is 2
// columns): truncated with a trailing ellipsis if s runs over (regardless
// of pad — that case always lands on exactly width), space-padded out to
// width if pad is true and s is shorter, left as-is otherwise.
func clip(s string, width int, pad bool) string {
	w := runewidth.StringWidth(s)
	switch {
	case w > width:
		return truncateWithEllipsis(s, width)
	case pad:
		return s + strings.Repeat(" ", width-w)
	default:
		return s
	}
}

// clipLabels fits a label list into width display columns. Unlike clip()'s
// ellipsis, an over-width list drops whole labels from the tail and replaces
// them with a "+N" count, so no label text is mangled mid-word.
func clipLabels(labels []string, width int) string {
	full := strings.Join(labels, ", ")
	if runewidth.StringWidth(full) <= width {
		return full
	}
	bare := fmt.Sprintf("+%d", len(labels))
	for k := len(labels) - 1; k > 0; k-- {
		suffix := fmt.Sprintf("+%d", len(labels)-k)
		candidate := strings.Join(labels[:k], ", ") + ", " + suffix
		if runewidth.StringWidth(candidate) <= width {
			return candidate
		}
	}
	// Not even one whole label fits alongside its count — fall back to the
	// bare "+N" for every label, clipped further if that itself overflows.
	return clip(bare, width, false)
}

// bannerErrWidth bounds a single-line header error banner to one row's worth
// of text: RunNixBuild wraps the merged nix stdout+stderr, often many lines,
// into one error. Fixed rather than tied to m.Width — this only needs to be
// "one reasonable terminal row", not exact.
const bannerErrWidth = 200

// clipBannerErr collapses an error's embedded newlines to single spaces and
// clips the result to width, so a header error banner stays one row however
// verbose the underlying error was.
func clipBannerErr(s string, width int) string {
	return clip(strings.Join(strings.Fields(s), " "), width, false)
}

// headerTitle is the Console's fixed wordmark, folded into the header
// panel's top border rule by renderBoxedHeader rather than rendered as a
// separate interior banner.
const headerTitle = "spindrift"

// renderHeader renders the Console's full-width header: a status line, then
// the six alert lines, in fixed order with no priority or dismissal logic —
// any subset can be true at once. The waiting/held/settled/failed counts
// derive from the Picks slice's PickState tags, i.e. this session's own
// launches; recoverable is a session-independent axis, counting pre-existing
// terminal state from a prior run that never appears in Picks (ADR 0039).
func renderHeader(m Model) string {
	var waiting, held, settled, failed int
	for _, p := range m.Picks {
		switch p.State {
		case PickQueued:
			waiting++
		case PickHeld:
			held++
		case PickSettled:
			settled++
		case PickFailed:
			failed++
		}
	}

	var b strings.Builder
	// The status line always renders, even in a launch-less session where
	// Live/Cap read zero: session-at-a-glance context must not disappear
	// when the queue happens to be empty. Each segment is styled by its own
	// semantic role (ADR 0031), so content survives styling as separate
	// substrings rather than one contiguous line.
	fmt.Fprintf(&b, "%s · %s · %s · %s · %s · %s\n",
		roleStyle(RoleRunning).Render(fmt.Sprintf("running %d/%d", m.Live, m.Cap)),
		roleStyle(RoleDim).Render(fmt.Sprintf("waiting %d", waiting)),
		roleStyle(RoleHeld).Render(fmt.Sprintf("held %d", held)),
		roleStyle(RoleSettled).Render(fmt.Sprintf("settled %d", settled)),
		roleStyle(RoleFailed).Render(fmt.Sprintf("failed %d", failed)),
		roleStyle(RoleRecoverable).Render(fmt.Sprintf("recoverable %d", m.RecoverableCount)))
	if m.RebuildStatus.Stale {
		b.WriteString(roleStyle(RoleHeld).Render(fmt.Sprintf("%s image stale: %s — new launches held; press [b] to rebuild", glyphWarning, m.RebuildStatus.Message)))
		b.WriteString("\n")
	}
	if m.RebuildStatus.Rebuilding {
		b.WriteString(roleStyle(RoleRunning).Render(glyphRebuilding + " rebuilding image..."))
		b.WriteString("\n")
	}
	if m.RebuildStatus.Err != "" {
		// Only the glyph+label is styled, unlike the whole-line styling
		// above: the clipped error text must keep its trailing "…" as the
		// line's literal last character, with no styling reset after it.
		fmt.Fprintf(&b, "%s %s\n",
			roleStyle(RoleFailed).Render(glyphWarning+" rebuild failed:"),
			clipBannerErr(m.RebuildStatus.Err, bannerErrWidth))
	}
	if m.OrphanRecoveryErr != "" {
		// Same split as RebuildErr above, same reason.
		fmt.Fprintf(&b, "%s %s\n",
			roleStyle(RoleFailed).Render(glyphWarning+" orphan adopt failed:"),
			clipBannerErr(m.OrphanRecoveryErr, bannerErrWidth))
	}
	if m.RebuildStatus.BranchSwitchNotice != "" {
		b.WriteString(roleStyle(RoleDim).Render(fmt.Sprintf("%s notice: %s", glyphNotice, m.RebuildStatus.BranchSwitchNotice)))
		b.WriteString("\n")
	}
	if m.RebuildStatus.StaleDrainSummary != "" {
		b.WriteString(roleStyle(RoleDim).Render(fmt.Sprintf("%s notice: %s", glyphNotice, strings.TrimPrefix(m.RebuildStatus.StaleDrainSummary, "==> "))))
		b.WriteString("\n")
	}
	if m.DogfoodLive {
		b.WriteString(roleStyle(RoleDim).Render(glyphNotice + " notice: a live dogfood loop (.spindrift/dogfood.pid) is competing for the same queue"))
		b.WriteString("\n")
	}
	return b.String()
}

// renderHelp renders the "?" overlay: every key the tea layer binds,
// replacing the backlog/queue rendering entirely while open. Each keymap
// Binding with non-empty Help contributes its line(s), in declared order.
func renderHelp() string {
	lines := []string{"help"}
	for _, b := range keymap {
		if b.Help == "" {
			continue
		}
		lines = append(lines, strings.Split(b.Help, "\n")...)
	}
	lines = append(lines, "")
	return strings.Join(lines, "\n")
}

// positionLabel returns a compact " (X-Y of N)" position indicator for a
// column's label, describing the rows vp actually renders at itemBudget of
// total — or "" when there is no range to show, so a column that renders no
// rows never grows a misleading "(1-0 of 0)" label. vp's height is set
// directly rather than through SetHeight, for renderTable's reason.
func positionLabel(vp Viewport, itemBudget, total int) string {
	if total == 0 || itemBudget <= 0 {
		return ""
	}
	vp.height = itemBudget
	w := vp.Window(total)
	shown, _ := w.Shown()
	if shown <= 0 {
		return ""
	}
	return fmt.Sprintf(" (%d-%d of %d)", w.Start+1, w.Start+shown, total)
}

// sectionPageSize returns how many rows one page jump moves the active
// Section's viewport by — the row count actually rendered at its current
// offset, not the raw item budget. A truncated window holds one row back for
// the "N more below" affordance, so paging by the raw budget would overshoot
// and skip the row right past the fold. Recomputed on every keypress.
func sectionPageSize(m Model) int {
	layout := ResolveLayout(m)
	itemBudget := queueItemBudget(layout.Compact, layout.ListContentBudget)
	if itemBudget <= 0 {
		return 0
	}
	total := sectionRowCount(m, m.ActiveSection)
	vp := Viewport{offset: m.Offset, height: itemBudget}
	shown, _ := vp.Window(total).Shown()
	return shown
}

// columnItemBudget converts a Section's row budget (header row included)
// into the budget for its item rows alone. Window.Shown() already holds one
// row back for the "… N more below" line, so the truncated case never
// exceeds itemBudget without a further reservation here; the trailing-"\n"
// slack comes from viewBody/bodyBudget, which covers every render — a second
// reservation here would double-count. A non-positive budget yields zero.
func columnItemBudget(columnBudget int) int {
	if columnBudget <= 0 {
		return 0
	}
	return columnBudget - 1
}

// queueItemBudget is columnItemBudget's compact-aware wrapper: callers pass
// Layout.Compact rather than re-deriving it, so the cursor-follow and
// page-size math never assumes the classic one-line-per-item budget while
// the compact form is what actually renders.
func queueItemBudget(compact bool, columnBudget int) int {
	if compact {
		return compactColumnItemBudget(columnBudget)
	}
	return columnItemBudget(columnBudget)
}

// windowSidebarLines returns s.Lines windowed through a Viewport at
// s.Offset, budget rows deep — so a render joins only what the viewport can
// show instead of the whole tail from Offset to the end of a potentially
// multi-MB transcript. A non-positive budget yields nil rather than asking
// Viewport to represent it (SetHeight(0) means unbounded, not zero lines).
func windowSidebarLines(s SidebarState, budget int) []string {
	if budget <= 0 {
		return nil
	}
	vp := Viewport{offset: s.Offset, total: len(s.Lines)}
	vp.SetHeight(budget)
	w := vp.Window(len(s.Lines))
	return s.Lines[w.Start:w.End]
}

// headerFooterLines is the sidebar chrome budget (label + keystroke-hint
// footer) that renderSidebarFullscreen and Update's tail both subtract from
// height — shared so the clamp's last-page cap always matches what
// renderSidebarFullscreen has room to show. renderSidebarDocked uses
// sidebarDockedFooterLines instead.
const headerFooterLines = 2

// trailingNewlineRow is the extra row renderRebuildOutputPane,
// renderSidebarFullscreen, and their model.go cursor-follow mirrors reserve
// for View()'s guaranteed trailing "\n". Without it, output that exactly
// fills the budget renders header(1)+budget+footer(1) == m.Height lines, one
// over once that newline counts as its own physical row. Named and shared,
// rather than a bare "-1" per site, so the budgets can't drift apart.
// renderSidebarDocked inherits bodyBudget's own "-1" and never needs it.
const trailingNewlineRow = 1

// sidebarDockedFooterLines is the docked sidebar's chrome budget
// (keystroke-hint footer only) — narrower than headerFooterLines because the
// docked panel's label folds into its border title instead of spending an
// interior row, while renderSidebarFullscreen still spends one on its label.
const sidebarDockedFooterLines = 1

// listFooterLines is the plain list body's chrome budget (keystroke-hint
// footer only) that renderBody and viewBody's reservedLines subtract for
// ModeList.
const listFooterLines = 1

// sidebarErr returns the error the current view should surface: s.Err
// unconditionally (nothing loaded at all), otherwise s.TranscriptErr only
// while ShowTranscript is true — a Transcript-only load failure must never
// blank out an independently-loaded, otherwise-good Activity feed.
func sidebarErr(s SidebarState) error {
	if s.Err != nil {
		return s.Err
	}
	if s.ShowTranscript {
		return s.TranscriptErr
	}
	return nil
}

// sidebarLabel renders s's one-line pane header: "activity #N" by default,
// "transcript #N" once toggled, "(raw)" appended while ShowRaw. The Activity
// feed also carries a "[follow]"/"[paused]" tag — the operator's only
// render-level signal for whether the feed is live-tailing or detached after
// a scroll-up (ADR 0030). The Transcript is a one-shot load with nothing to
// follow, so the tag is meaningless there.
func sidebarLabel(s SidebarState) string {
	if !s.ShowTranscript {
		label := "activity #" + s.Number
		if s.Follow {
			return label + " [follow]"
		}
		return label + " [paused]"
	}
	label := "transcript #" + s.Number
	if s.ShowRaw {
		label += " (raw)"
	}
	return label
}

// wrapText greedily word-wraps s into lines of at most width display
// columns, preserving blank lines verbatim — the detail modal body's
// plain-text renderer, hand-rolled because there is no markdown renderer in
// the dependency tree. A single word wider than width is placed alone on its
// own (overflowing) line rather than broken mid-word.
func wrapText(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		if para == "" {
			out = append(out, "")
			continue
		}
		var line string
		for _, word := range strings.Fields(para) {
			candidate := word
			if line != "" {
				candidate = line + " " + word
			}
			if line != "" && runewidth.StringWidth(candidate) > width {
				out = append(out, line)
				line = word
				continue
			}
			line = candidate
		}
		out = append(out, line)
	}
	return out
}

// detailModalTitleLines is the fullscreen detail modal's fixed number/title
// header row spend, shared by renderDetailModal and
// detailModalScrollBudget's Offset clamp. The labels line is not a fixed
// spend alongside it — it wraps onto further rows once bracketed, so both
// callers count it dynamically via detailModalLabelLinesCapped.
const detailModalTitleLines = 1

// detailModalLines flattens s's body (word-wrapped to width) and its
// Blocked-by/Blocks sections into one scrollable line list, computed once
// when DetailModalLoadedMsg lands rather than re-wrapped on every keystroke.
// A section with nothing to list contributes no lines at all.
func detailModalLines(width int, s DetailModalState) []string {
	lines := wrapText(SanitizeControlSequences(s.Body), width)
	lines = append(lines, detailModalBlockerLines("Blocked by", s.BlockedBy)...)
	lines = append(lines, detailModalBlockerLines("Blocks", s.Blocks)...)
	return lines
}

// detailModalBlockerLines renders one of the detail modal's Blocked-by/
// Blocks sections: a blank separator, a header, then one line per
// BlockerRef — nil when refs is empty, so nothing grows an empty header.
func detailModalBlockerLines(header string, refs []BlockerRef) []string {
	if len(refs) == 0 {
		return nil
	}
	lines := make([]string, 0, len(refs)+2)
	lines = append(lines, "", header+":")
	for _, r := range refs {
		lines = append(lines, formatBlockerRef(r))
	}
	return lines
}

// windowDetailModalLines is windowSidebarLines' detail-modal analogue.
func windowDetailModalLines(s DetailModalState, budget int) []string {
	if budget <= 0 {
		return nil
	}
	vp := Viewport{offset: s.Offset, total: len(s.Lines)}
	vp.SetHeight(budget)
	w := vp.Window(len(s.Lines))
	return s.Lines[w.Start:w.End]
}

// detailModalBoxWidthPercent and detailModalBoxHeightPercent are the share
// of the terminal the floating detail modal box targets before the min/max
// clamps apply, so the box scales with the terminal rather than shrinking by
// a fixed margin.
const (
	detailModalBoxWidthPercent  = 80
	detailModalBoxHeightPercent = 80
)

// detailModalBoxMinWidth and detailModalBoxMinHeight floor the box at a size
// where the border plus a line or two of interior stays legible.
// detailModalFits gates the floating layout on the terminal being at least
// this large, so the clamp never inflates the box past the terminal.
const (
	detailModalBoxMinWidth  = 40
	detailModalBoxMinHeight = 10
)

// detailModalBoxMaxWidth and detailModalBoxMaxHeight cap the box at a
// comfortable reading size instead of stretching it corner to corner.
const (
	detailModalBoxMaxWidth  = 100
	detailModalBoxMaxHeight = 30
)

// detailModalBoxSize returns the floating detail modal box's outer width and
// height for a termWidth x termHeight terminal. Only meaningful when
// detailModalFits(m) is true — below that threshold the min clamp would
// inflate the box past the terminal, which the fullscreen fallback path
// never observes.
func detailModalBoxSize(termWidth, termHeight int) (width, height int) {
	return modalBoxSize(termWidth, termHeight, modalBoxSpec{
		WidthPercent:  detailModalBoxWidthPercent,
		HeightPercent: detailModalBoxHeightPercent,
		MinWidth:      detailModalBoxMinWidth,
		MinHeight:     detailModalBoxMinHeight,
		MaxWidth:      detailModalBoxMaxWidth,
		MaxHeight:     detailModalBoxMaxHeight,
	})
}

// detailModalBoxOrigin centers a boxWidth x boxHeight box within a
// termWidth x termHeight terminal, the (x, y) compositeOverlay places it at.
func detailModalBoxOrigin(termWidth, termHeight, boxWidth, boxHeight int) (x, y int) {
	return modalBoxOrigin(termWidth, termHeight, boxWidth, boxHeight)
}

// detailModalInnerSize returns the floating detail modal box's interior
// width/height — the outer size minus the one-column/one-row border on every
// side. The width-dependent modal machinery (Lines word-wrap, scroll budget)
// must key off this rather than Model.Width/Model.Height, so a resize and
// the box's own render agree on how wide the body was actually wrapped.
func detailModalInnerSize(termWidth, termHeight int) (width, height int) {
	boxWidth, boxHeight := detailModalBoxSize(termWidth, termHeight)
	return modalBoxInnerSize(boxWidth, boxHeight)
}

// sidebarModalBoxWidthPercent and sidebarModalBoxHeightPercent are the log
// modal's share of the terminal — the same target the detail modal uses. The
// two modals share this percent but diverge on the max clamp below.
const (
	sidebarModalBoxWidthPercent  = 80
	sidebarModalBoxHeightPercent = 80
)

// sidebarModalBoxMinWidth and sidebarModalBoxMinHeight floor the log modal
// at the same legibility floor detailModalBoxMin{Width,Height} use.
const (
	sidebarModalBoxMinWidth  = 40
	sidebarModalBoxMinHeight = 10
)

// sidebarModalBoxMaxWidth and sidebarModalBoxMaxHeight cap the log modal
// deliberately larger than detailModalBoxMax{Width,Height}: the zoom must
// read as visibly bigger than the detail modal on a roomy terminal, while
// still pinning well short of corner-to-corner on very large monitors.
const (
	sidebarModalBoxMaxWidth  = 180
	sidebarModalBoxMaxHeight = 54
)

// sidebarModalBoxSize is detailModalBoxSize's log-modal analogue.
func sidebarModalBoxSize(termWidth, termHeight int) (width, height int) {
	return modalBoxSize(termWidth, termHeight, modalBoxSpec{
		WidthPercent:  sidebarModalBoxWidthPercent,
		HeightPercent: sidebarModalBoxHeightPercent,
		MinWidth:      sidebarModalBoxMinWidth,
		MinHeight:     sidebarModalBoxMinHeight,
		MaxWidth:      sidebarModalBoxMaxWidth,
		MaxHeight:     sidebarModalBoxMaxHeight,
	})
}

// sidebarModalBoxOrigin is detailModalBoxOrigin's log-modal analogue.
func sidebarModalBoxOrigin(termWidth, termHeight, boxWidth, boxHeight int) (x, y int) {
	return modalBoxOrigin(termWidth, termHeight, boxWidth, boxHeight)
}

// sidebarModalInnerSize is detailModalInnerSize's log-modal analogue.
func sidebarModalInnerSize(termWidth, termHeight int) (width, height int) {
	boxWidth, boxHeight := sidebarModalBoxSize(termWidth, termHeight)
	return modalBoxInnerSize(boxWidth, boxHeight)
}

// padBaseForOverlay pads every line of s out to at least width display
// columns and appends blank width-wide lines until s has at least height
// lines. compositeLine leaves a base row untouched unless its width already
// reaches the box's x origin, and compositeOverlay only overwrites rows base
// already has — but viewBody's rows stop at whatever content they have, so
// the base must be padded to the terminal's full frame before a box lower on
// screen, or wider than a short row, can land on it.
func padBaseForOverlay(s string, width, height int) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if w := ansi.StringWidth(line); w < width {
			lines[i] = line + strings.Repeat(" ", width-w)
		}
	}
	blank := strings.Repeat(" ", width)
	for len(lines) < height {
		lines = append(lines, blank)
	}
	return strings.Join(lines, "\n")
}

// padDisplay right-pads (or, if it overflows, truncates) s to exactly width
// display columns — every interior row of the floating box must land at
// exactly its inner width, or the side border runes drift out of column. An
// overflowing s is truncated with a trailing ellipsis, mirroring clip, so
// the cut is visible. Measured with ansi.StringWidth, so a caller may hand
// it an already-styled row without ANSI escape bytes being miscounted as
// display columns. The truncate branch stays runewidth-based and is only
// safe to reach with plain content already clipped to width.
func padDisplay(s string, width int) string {
	if width < 0 {
		width = 0
	}
	w := ansi.StringWidth(s)
	if w > width {
		return truncateWithEllipsis(s, width)
	}
	if w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}

// detailModalFooterLines is the floating detail modal box's fixed
// keystroke-hint footer row spend — shared by renderDetailModalContent's
// body budget and Update's Offset clamp so the two never drift apart.
const detailModalFooterLines = 1

// detailModalLabelLines word-wraps a ticket's labels to width display
// columns, comma-joined inside brackets and dim-styled. The brackets are
// literal characters wrapText wraps, so they count toward width like any
// other rune. Each wrapped line is clip()ped *before* it is styled, never
// after: padDisplay's truncate branch is runewidth-based and would otherwise
// miscount the style's ANSI escape bytes as display columns. Shared by
// renderDetailModalContent and Update's Offset clamp, which must agree on
// how many interior rows the labels spend.
func detailModalLabelLines(labels []string, width int) []string {
	sanitized := make([]string, len(labels))
	for i, l := range labels {
		sanitized[i] = SanitizeControlSequences(l)
	}
	lines := wrapText("["+strings.Join(sanitized, ", ")+"]", width)
	for i, l := range lines {
		lines[i] = roleStyle(RoleDim).Render(clip(l, width, false))
	}
	return lines
}

// detailModalLabelLinesCapped wraps labels like detailModalLabelLines, but
// caps the result at maxLines: labels are dropped from the tail and replaced
// with a "+N more labels" entry folded inside the same bracket —
// "[alpha, +3 more labels]" — so the indicator still reads as part of the
// one "these are labels" row. Without the cap, a ticket with enough labels
// to fill the box's whole interior would have renderDetailModalContent
// silently tail-truncate its label lines and/or its footer row instead.
// maxLines <= 0 yields the bare, unbracketed "+N more labels" alone.
func detailModalLabelLinesCapped(labels []string, width, maxLines int) []string {
	lines := detailModalLabelLines(labels, width)
	if len(labels) == 0 || len(lines) <= maxLines {
		return lines
	}
	for k := len(labels) - 1; k >= 0; k-- {
		trial := detailModalLabelLines(append(append([]string{}, labels[:k]...), fmt.Sprintf("+%d more labels", len(labels)-k)), width)
		if len(trial) <= maxLines {
			return trial
		}
	}
	return []string{fmt.Sprintf("+%d more labels", len(labels))}
}

// renderDetailModalContent renders the floating detail modal box's interior
// — the labels line, the loading/error/body-window content, and the
// scroll/close footer hint — as exactly innerHeight lines, wrapped and
// scrolled against the box interior rather than Model.Width/Model.Height.
func renderDetailModalContent(s DetailModalState, innerWidth, innerHeight int) []string {
	contentBudget := innerHeight - detailModalFooterLines
	lines := detailModalLabelLinesCapped(s.Labels, innerWidth, contentBudget)
	bodyBudget := contentBudget - len(lines)
	switch {
	case s.Loading:
		lines = append(lines, "loading...")
	case s.Err != nil:
		lines = append(lines, fmt.Sprintf("failed to load: %s", SanitizeControlSequences(s.Err.Error())))
	default:
		lines = append(lines, windowDetailModalLines(s, bodyBudget)...)
	}
	// Capped against contentBudget, not innerHeight, before the footer is
	// appended — so the footer's own reserved line is never among those a
	// too-long labels/loading/error block pushes past the end. When labels
	// alone consume contentBudget, this is what drops the loading/error
	// line: the "+N more labels" indicator takes budget precedence over the
	// one-line status text, never the reverse.
	if len(lines) > contentBudget {
		lines = lines[:contentBudget]
	}
	lines = append(lines, renderFooterHints(ModeDetailModal, []string{"esc", "p", "r", "u"}, innerWidth, false))
	for len(lines) < innerHeight {
		lines = append(lines, "")
	}
	if len(lines) > innerHeight {
		lines = lines[:innerHeight]
	}
	return lines
}

// renderDetailModalBox renders s as a bordered floating box exactly
// width x height display cells: "#number title" set in the top border, the
// interior renderDetailModalContent produces, and every row padded to width
// so compositeOverlay fully occludes the list content behind it. Boxed via
// the shared renderBoxedColumn/renderTitledTopBorder helper rather than
// hand-rolled runes, so the border degrades to ASCII like every other panel.
func renderDetailModalBox(s DetailModalState, width, height int) string {
	if width < 4 || height < 3 {
		return ""
	}
	innerWidth := width - 2
	innerHeight := height - 2
	title := fmt.Sprintf("#%s %s", SanitizeControlSequences(s.Number), SanitizeControlSequences(s.Title))

	lines := renderDetailModalContent(s, innerWidth, innerHeight)
	// Each content line must be clipped to exactly innerWidth before it
	// reaches renderBoxedColumn: lipgloss's Width() only pads a line up to
	// width, never truncates one down, so an over-wide line (e.g. a label
	// wrapText left unbroken) would widen the whole box instead of getting
	// cut with an ellipsis.
	for i, l := range lines {
		lines[i] = padDisplay(l, innerWidth)
	}
	return renderBoxedColumn(strings.Join(lines, "\n"), innerWidth, title, RoleDim)
}

// renderDetailModal renders a Backlog issue's fullscreen ticket detail
// modal: number/title, labels capped via detailModalLabelLinesCapped to stay
// in parity with the floating box, and — once the async fetch lands — a
// word-wrapped body plus Blocked-by/Blocks sections scrolled through one
// Viewport. It opens the instant Enter fires, before the fetch resolves, so
// a "loading..." placeholder stands in until DetailModalLoadedMsg arrives.
// Reached only on the small-terminal fallback path.
func renderDetailModal(s DetailModalState, width, height int) string {
	if height <= 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "#%s %s\n", s.Number, SanitizeControlSequences(s.Title))
	contentBudget := height - detailModalTitleLines - detailModalFooterLines
	if contentBudget < 0 {
		contentBudget = 0
	}
	labelLines := detailModalLabelLinesCapped(s.Labels, width, contentBudget)
	b.WriteString(strings.Join(labelLines, "\n"))
	b.WriteString("\n")
	bodyBudget := contentBudget - len(labelLines)
	switch {
	case s.Loading:
		b.WriteString("loading...\n")
	case s.Err != nil:
		fmt.Fprintf(&b, "failed to load: %s\n", SanitizeControlSequences(s.Err.Error()))
	default:
		visible := strings.Join(windowDetailModalLines(s, bodyBudget), "\n")
		b.WriteString(visible)
		if visible != "" && !strings.HasSuffix(visible, "\n") {
			b.WriteString("\n")
		}
	}
	fmt.Fprintf(&b, "%s\n", renderFooterHints(ModeDetailModal, []string{"esc", "p", "r", "u"}, width, false))
	return b.String()
}

// blockerOpenGlyph and blockerClosedGlyph mark a BlockerRef's open/closed
// state at a glance, ahead of the spelled-out state word.
const (
	blockerOpenGlyph   = "✗"
	blockerClosedGlyph = "✓"
)

// formatBlockerRef renders one Blocked-by/Blocks entry: an open/closed
// glyph, the issue number, its dependency source (native vs body-parsed),
// its state spelled out, and its title — e.g.
// `✗ #1540 (native) open "Waves core"`. Static text, no drill-down.
func formatBlockerRef(r BlockerRef) string {
	glyph := blockerOpenGlyph
	if r.State == forge.IssueClosed || r.State == forge.IssueMerged {
		glyph = blockerClosedGlyph
	}
	state := strings.ToLower(string(r.State))
	if state == "" {
		// resolveBlockerRef's failure fallback (the ref was deleted, or its
		// Issue fetch erred) leaves State/Title blank — render "unknown" in
		// both rather than a double space and an empty quoted string.
		state = "unknown"
	}
	title := SanitizeControlSequences(r.Title)
	if title == "" {
		title = "unknown"
	}
	// forge.Ref centralizes the "#N (source)" annotation every other
	// blocker-diagnostic call site shares; reusing it prevents drift.
	return fmt.Sprintf("%s %s %s %q", glyph, forge.Ref(r.Number, r.Source), state, title)
}

// renderSidebarFullscreen renders one Dispatch's live-tail sidebar at full
// terminal width and height — the narrow-terminal fallback View reaches for
// when sidebarFits is false. Err renders in place of content rather than
// leaving a blank pane. The label, footer, and Err line are themselves
// budgeted against height: at height 1 only the label renders, and whichever
// of the footer or Err line would come next is dropped.
func renderSidebarFullscreen(s SidebarState, width, height int) string {
	if height <= 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(sidebarLabel(s))
	b.WriteString("\n")

	const labelLines = 1
	if height <= headerFooterLines-labelLines {
		return b.String()
	}

	if err := sidebarErr(s); err != nil {
		fmt.Fprintf(&b, "sidebar failed: %s\n", err)
		return b.String()
	}

	lines := windowSidebarLines(s, height-headerFooterLines-trailingNewlineRow)
	clipped := make([]string, len(lines))
	for i, line := range lines {
		clipped[i] = clip(line, width, false)
	}
	visible := strings.Join(clipped, "\n")
	b.WriteString(visible)
	if visible != "" && !strings.HasSuffix(visible, "\n") {
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "%s\n", renderFooterHints(ModeSidebar, []string{"t", "x", "z", "H"}, width, false))
	return b.String()
}

// sidebarModalLabelLines is the one row renderSidebarModalContent reserves
// for sidebarLabel, so the floating box keeps the
// activity/transcript-and-follow-state info the fullscreen pane shows.
const sidebarModalLabelLines = 1

// renderSidebarModalContent renders s's body for the floating log modal,
// windowed to innerWidth x innerHeight — renderDetailModalContent's sidebar
// analogue. Its rows of chrome (label, sidebarErr's error line in place of
// content, footer) are budgeted the way renderSidebarFullscreen budgets
// headerFooterLines, so a resize never renders past innerHeight.
func renderSidebarModalContent(s SidebarState, innerWidth, innerHeight int) []string {
	lines := []string{clip(sidebarLabel(s), innerWidth, false)}
	contentBudget := innerHeight - sidebarModalLabelLines - trailingNewlineRow
	if contentBudget < 0 {
		contentBudget = 0
	}
	switch err := sidebarErr(s); {
	case err != nil:
		lines = append(lines, clip("sidebar failed: "+err.Error(), innerWidth, false))
	default:
		for _, line := range windowSidebarLines(s, contentBudget) {
			lines = append(lines, clip(line, innerWidth, false))
		}
	}
	if len(lines) > innerHeight-trailingNewlineRow {
		lines = lines[:innerHeight-trailingNewlineRow]
	}
	lines = append(lines, renderFooterHints(ModeSidebar, []string{"t", "x", "z", "H"}, innerWidth, false))
	for len(lines) < innerHeight {
		lines = append(lines, "")
	}
	if len(lines) > innerHeight {
		lines = lines[:innerHeight]
	}
	return lines
}

// renderSidebarModalBox renders s as a bordered floating box exactly
// width x height display cells, matching renderDetailModalBox: same shared
// border helper, same padDisplay-every-row treatment so compositeOverlay
// fully occludes the list content behind it.
func renderSidebarModalBox(s SidebarState, width, height int) string {
	if width < 4 || height < 3 {
		return ""
	}
	innerWidth := width - 2
	innerHeight := height - 2
	title := fmt.Sprintf("#%s %s", SanitizeControlSequences(s.Number), SanitizeControlSequences(s.Title))

	lines := renderSidebarModalContent(s, innerWidth, innerHeight)
	for i, l := range lines {
		lines[i] = padDisplay(l, innerWidth)
	}
	return renderBoxedColumn(strings.Join(lines, "\n"), innerWidth, title, RoleDim)
}

// footerHintWidth returns the width left for a fullscreen overlay's hint
// text once its literal prefix (e.g. "/filter  ") has eaten into the same
// line: total minus the prefix's rendered columns, floored at 1 whenever
// total is itself positive — so a terminal narrow enough that the prefix
// eats the whole line still clips the hint, rather than going negative and
// falling through renderFooterHints' width<=0 "leave unclipped" sentinel.
// total<=0 (an unset Model.Width) passes the negative through unchanged, so
// that sentinel still reaches renderFooterHints.
func footerHintWidth(total int, prefix string) int {
	w := total - lipgloss.Width(prefix)
	if total > 0 && w < 1 {
		w = 1
	}
	return w
}

// renderFooterHints renders one mode's pinned keystroke-hint line: each
// key's Footer text from keymap, joined and dim-styled, so every footer
// shares one source instead of hand-building its own hint string. compact
// switches to footerHintCompact and the docked sidebar's tighter separator.
// width clips the joined line *before* styling — clipping after would
// miscount the styling's ANSI escape bytes as display columns. A width of 0
// or less leaves the line unclipped, a sentinel renderRebuildOutputPane
// still relies on.
func renderFooterHints(mode Mode, keys []string, width int, compact bool) string {
	hintFor := footerHint
	sep := " · "
	if compact {
		hintFor = footerHintCompact
		sep = " ·"
	}
	hints := make([]string, len(keys))
	for i, key := range keys {
		hints[i] = hintFor(mode, key)
	}
	line := strings.Join(hints, sep)
	if width > 0 {
		line = clip(line, width, false)
	}
	return roleStyle(RoleDim).Render(line)
}

// renderSidebarDocked renders one Dispatch's live-tail sidebar as a column
// beside the still-visible list (ADR 0030): content clipped to width so an
// overflowing line can't blow out the column join, and budgeted in rows to
// match renderTable's row-budget contract so the two columns agree before
// lipgloss.JoinHorizontal pads whichever falls short. The label is not
// rendered here — the caller folds it into the panel's top border — so
// budget reserves only sidebarDockedFooterLines. A budget of 1 renders the
// footer alone, since the border title shows regardless.
func renderSidebarDocked(s SidebarState, width, budget int) string {
	if budget <= 0 {
		return ""
	}

	var b strings.Builder

	if err := sidebarErr(s); err != nil {
		fmt.Fprintf(&b, "%s\n", clip("sidebar failed: "+err.Error(), width, false))
		return b.String()
	}

	for _, line := range windowSidebarLines(s, budget-sidebarDockedFooterLines) {
		b.WriteString(clip(line, width, false))
		b.WriteString("\n")
	}
	// Deliberately tighter than the fullscreen footer's " · " spacing: five
	// hints plus full separators would overflow sidebarWidth's 42-column
	// budget, so the space after each "·" is dropped and "z"'s hint shortens
	// to "[z]" (FooterCompact) to fit all five without clipping the last.
	b.WriteString(renderFooterHints(ModeSidebar, []string{"t", "h", "x", "z", "H"}, width, true))
	b.WriteString("\n")
	return b.String()
}

// renderRebuildOutputPane renders the last rebuild's captured nix output
// full-screen, from RebuildOutputOffset onward, plus a close-key hint. It
// has no docked/floating mode: the output is a flat log, not a Dispatch's
// Transcript worth keeping alongside the backlog/queue.
func renderRebuildOutputPane(m Model) string {
	var b strings.Builder
	b.WriteString("rebuild output:\n")

	budget := m.Height - headerFooterLines - trailingNewlineRow
	lines := strings.Split(m.RebuildStatus.Output, "\n")
	var visible string
	if budget > 0 {
		vp := Viewport{offset: m.RebuildOutputOffset, total: len(lines)}
		vp.SetHeight(budget)
		w := vp.Window(len(lines))
		visible = strings.Join(lines[w.Start:w.End], "\n")
	}
	b.WriteString(visible)
	if visible != "" && !strings.HasSuffix(visible, "\n") {
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "%s\n", renderFooterHints(ModeRebuildOutput, []string{"x"}, 0, false))
	return b.String()
}
