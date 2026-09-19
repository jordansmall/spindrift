package console

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/cellbuf"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/termenv"

	"spindrift.dev/launcher/internal/forge"
)

// boxBorderCols and boxBorderRows are the column and row overhead a single
// docked panel's rounded border adds, one per edge. dockedBorderCols covers
// both docked panels, which replaced the old one-column divider between them
// with two adjacent box edges (issue #1755).
const (
	boxBorderCols    = 2
	boxBorderRows    = 2
	dockedBorderCols = boxBorderCols * 2
)

// padColumnsToEqualHeight pads the shorter of the list and sidebar columns
// with trailing blank lines so their bordered boxes close on the same row
// (issue #1755).
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

// renderBoxedColumn wraps content in a muted (RoleDim) rounded border (issue
// #1755) that degrades to ASCII glyphs under NO_COLOR or a dumb terminal
// (#1797). content's lines must already be clipped to the panel's interior
// width. Empty content renders no box, so a zero-height budget draws no stray
// frame. With title set, the top border folds the title into the rule (#1758).
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
// display columns, folding title into the rule (issue #1758). A too-wide title
// truncates with an ellipsis, and the fill is recomputed from its actual
// rendered width, so the rule lands on exactly width even where Truncate stops
// a column short of a wide rune (issue #1785).
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
		// A panel too narrow even for the lead-in and trailing space
		// (inner < structural) needs the whole label clamped together, not
		// the title alone, or the rule overflows width (issue #1797 review).
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

// View renders m as the text the run loop writes to the terminal: the header,
// the Section tabs, the active Section's aligned table, and any refresh error
// (ADR 0030). A sidebar docks beside the list or goes fullscreen on a narrow
// terminal (#1501); a detail modal floats over it, or falls back to fullscreen
// when the terminal is too small (#1759). resolveLayout owns both (#2922).
func View(m Model) string {
	return viewWithLayout(m, resolveLayout(m))
}

// viewWithLayout is View's body over a caller-supplied layout: teaModel.View
// passes its own cached layout rather than paying for a second resolveLayout
// per keystroke (issue #3018).
func viewWithLayout(m Model, l layout) string {
	if l.arrangement == arrangementDetailFullscreen {
		return renderDetailModal(*m.DetailModal, m.Width, m.Height)
	}
	if l.arrangement == arrangementSidebarFullscreen {
		return renderSidebarFullscreen(*m.Sidebar, m.Width, m.Height)
	}
	base := viewBody(m, l)
	if l.arrangement == arrangementSidebarModal {
		b := l.sidebarModalBox
		box := renderSidebarModalBox(*m.Sidebar, b.Width, b.Height)
		base = dimBase(padBaseForOverlay(base, m.Width, b.Y+b.Height))
		base = compositeOverlay(base, box, b.X, b.Y)
	}
	if m.DetailModal != nil && l.detailModalFits {
		b := l.detailModalBox
		box := renderDetailModalBox(*m.DetailModal, b.Width, b.Height)
		base = dimBase(padBaseForOverlay(base, m.Width, b.Y+b.Height))
		base = compositeOverlay(base, box, b.X, b.Y)
	}
	return base
}

// renderBoxedHeader renders the status/alert block in a bordered panel with
// the wordmark in its top border rule (issues #1756, #1798). The result always
// ends in exactly one trailing newline, boxed or not. bodyBudget must count
// exactly these rows, borders included, or Update's cursor-follow clamps too
// tall, so both take the boxed verdict from headerGeometry (issue #3019).
func renderBoxedHeader(m Model) string {
	header := renderHeader(m)
	if _, boxed := headerGeometry(m); boxed {
		return renderBoxedColumn(header, m.Width-boxBorderCols, headerTitle, RoleDim) + "\n"
	}
	return header
}

// headerGeometry predicts renderBoxedHeader's line count without rendering:
// renderHeaderWith(m, plainText), lipgloss's pre-wrap normalization, then the
// same cellbuf.Wrap lipgloss uses. ANSI escapes are zero-width to that wrap,
// so plain and styled headers wrap alike; ansi.Wrap diverges on tabs, fullwidth
// runes, and ZWJ clusters (#3019). boxed is the fitness verdict (#1035, #1825).
func headerGeometry(m Model) (lines int, boxed bool) {
	header := renderHeaderWith(m, plainText)
	headerWidth := m.Width - boxBorderCols
	if headerWidth <= 0 {
		return strings.Count(header, "\n"), false
	}
	// renderBoxedColumn returns "" for empty content rather than a degenerate
	// empty frame (issue #1755), so boxedLines is the single line "" + "\n"
	// leaves behind, not wrapped(0) + boxBorderRows.
	content := strings.TrimSuffix(header, "\n")
	boxedLines := 1
	if content != "" {
		// lipgloss's unexported maybeConvertTabs (tab width 4) and its
		// "\r\n" replacement run inside Style.Render before cellbuf.Wrap ever
		// sees the string, so replicate them to predict the same wrap.
		normalized := strings.ReplaceAll(content, "\t", "    ")
		normalized = strings.ReplaceAll(normalized, "\r\n", "\n")
		wrapped := strings.Count(cellbuf.Wrap(normalized, headerWidth, ""), "\n") + 1
		boxedLines = wrapped + boxBorderRows
	}
	if boxedLines < m.Height {
		return boxedLines, true
	}
	return strings.Count(header, "\n"), false
}

// viewBody renders everything View shows behind an open detail or log modal:
// the header, Section tabs, and either the docked sidebar layout or the plain
// single-list body (issue #1758). A zoomed or too-narrow-to-dock Sidebar does
// not short-circuit here into renderSidebarFullscreen; View's own sidebarModal
// branch owns that decision (issue #1845).
func viewBody(m Model, l layout) string {
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
		// The refresh-error line renders after the body, but must be
		// subtracted from budget up front or a long list plus an error
		// together overflow Height by one line (issue #1035 review).
		reservedLines++
	}
	// The extra "-1" reserves the row View's guaranteed trailing "\n" needs,
	// and it lands on the body because every other component is already
	// fixed by this point. Without it a body that exactly fills what is left
	// still ends in a "\n" with no row to advance into, scrolling the pinned
	// top banner off screen (issue #1825; #1794 fixed only the truncated case).
	budget := m.Height - headerLines - reservedLines - 1
	if budget < 0 {
		budget = 0
	}
	// Taken from the layout rather than re-derived below the width narrowing:
	// queueNarrowed(listModel) would compare an already-narrowed Width against
	// sidebarFits' full-width threshold and misfire (issue #1752 review).
	compact := l.compact
	if l.sidebarArrangement == arrangementSidebarDocked {
		width := l.sidebarWidth
		listModel := m
		listModel.Width = l.listWidth
		// bodyBudget already subtracts boxBorderRows for the docked case, so
		// View's render and Update's scroll clamps agree on how many rows the
		// bordered panels have room for (issue #1755).
		panelBudget := l.bodyBudget
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
// column widths: each column's vocabulary is bounded, so a fixed width aligns
// every row's title column without measuring content first (ADR 0030).
// stateColWidth fits "terminated", the longest PickState word, plus padding.
const (
	numberColWidth = 7
	stateColWidth  = 11
	ageColWidth    = 7
)

// sectionTabsLines is the row budget the Section tabs line costs when it
// renders at all (issue #1500).
const sectionTabsLines = 1

// sectionTabsReserved returns sectionTabsLines when the terminal still has
// room for the tabs line after headerLines, and 0 otherwise, so a very short
// terminal never renders more than Height lines (issue #1500). The extra row
// of slack keeps room for View's trailing "\n" (issue #1825). Shared with
// bodyBudget so the two budgets cannot diverge (issue #1035).
func sectionTabsReserved(m Model, headerLines int) int {
	if m.Height <= headerLines+1 {
		return 0
	}
	return sectionTabsLines
}

// roleForSection returns the Role a Section's content styles with. Each
// pickSection Section maps onto its same-named Role; SectionBacklog, which
// pickSection never returns, styles as RoleAccent (ADR 0031).
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
// appends when there is room, built from keymap's own Footer text rather than
// a literal of its own (issue #1789).
var sectionTabsHint = fmt.Sprintf(" [%s,%s]", footerHint(ModeList, "H"), footerHint(ModeList, "1"))

// renderSectionTabs renders the fixed row of five Section tabs above the body:
// the active tab styles by its Role (ADR 0030). The line is measured and
// clipped as plain text before styling, since clip() would miscount ANSI
// escape bytes as display columns and could cut mid-sequence. The hint drops
// first on a narrow terminal, then the tabs clip (issue #1500).
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

// listFooterKeys are the ModeList bindings the main list view pins in its
// footer (issue #1792): the action verbs with no other on-screen affordance.
// Navigation and Section-jump keys are left out deliberately. The order is
// the read-then-act sequence an operator follows, not keymap's declaration
// order, so do not "fix" it back (#1838 added "P", #1839 added "R").
var listFooterKeys = []string{"/", "p", "P", "r", "R"}

// renderBody renders the active Section's table under the header and Section
// tabs (ADR 0030), followed by ModeList's pinned footer (issue #1792). budget
// is the rows left after the header, tabs, and prompt lines, always clamped
// nonnegative, never Viewport's "unbounded" case (#1540). Only ModeList spends
// a row here; the other Modes already use that reserved row for their prompt.
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
// total, budgeted to itemBudget rows, shared by renderBacklogSection and
// renderWorkSection (ADR 0030, #1540). A non-positive itemBudget writes no
// rows, since Viewport's SetHeight(0) means unbounded. vp.height is set
// directly: SetHeight's clamp (#829) would re-cap a pgup offset (#1060).
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

// extrasBudget is the width reserved for a row's trailing, unaligned content:
// a work row's blocker, reason, or heartbeat annotation, or a Backlog row's
// label list. Reserving it up front keeps a joined row at or under m.Width
// once the trailing content is appended; exceeding m.Width wraps the line in
// a real terminal and can split an assertion substring (issue #1500).
const extrasBudget = 30

// backlogFixedWidth is a Backlog row's width outside the title and label
// columns: the cursor marker, the number cell, and the literal separators and
// brackets the row format spends (`"%s %s %s [%s]\n"`).
const backlogFixedWidth = 1 + 1 + numberColWidth + 1 + 2 + 1

// renderBacklogSection renders the Backlog Section: one line per visible
// issue (number, title, labels), cursor-marked, under a column-header row
// (ADR 0030, #844). An orphan-flagged row's live heartbeat goes in the same
// bracket as its labels, sharing labelsWidth rather than taking a new column
// (issue #1621).
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
		// "orphan": the only Backlog signal separating it from a Dispatch this
		// session launched, since startup detects but never adopts one (issue
		// #1619). Its heartbeat comes off the same on-disk pass log a
		// session-launched Dispatch's does (issue #1621).
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
	// Two spaces, not one, before "labels": each row's label list sits after a
	// literal " [", one column wider than a bare space, so two spaces align
	// the header word with where the label text starts (issue #1500 review).
	headerText := fmt.Sprintf("  %s %s  labels", clip("issue", numberColWidth, true), clip("title", titleWidth, true))
	if compact {
		// The classic header's column words do not describe the compact row's
		// two-line shape, so echo its own header-line format instead of a
		// stale "title ... labels" claim (issue #1752 review).
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
// columns: the cursor marker, the number/state/age cells, and the four literal
// single-space separators the row format spends (`"%s %s %s %s %s%s\n"`).
// There is no separator between the age cell and the extras flush against it.
const workFixedWidth = 1 + 1 + numberColWidth + 1 + 1 + stateColWidth + 1 + ageColWidth

// renderWorkSection renders whichever work Section is active: one pick-ordered
// line per Pick, cursor-marked, columned as number/title/state/age under a
// column-header row (ADR 0030), the state cell styled by its Role (ADR 0031).
// Held's blocker and Running's heartbeat (#858, #647) render as a trailing
// annotation after the fixed columns rather than inside the aligned part.
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
	// By sectionPicks' construction every row's PickState maps onto
	// m.ActiveSection, so the Role is the same for every row.
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
		// blocker BlockedBy already names, so skip it rather than print the
		// same failed blocker twice on one row (issue #755).
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
		if p.PassState != "" {
			fmt.Fprintf(&extras, "  %s", SanitizeControlSequences(p.PassState))
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
		// The classic header's column words do not describe the compact row's
		// two-line shape, so echo its own header-line format instead of a
		// stale "title ... state age" claim (issue #1752 review).
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

// compactQueueIndent is the left indent the compact queue-row form's title
// line sits at (issue #1752).
const compactQueueIndent = "  "

// compactQueueSeparatorGlyph is the compact form's per-issue delimiter rune,
// a faint rule so the two-line stacked entries do not run together (#1752).
const compactQueueSeparatorGlyph = "─"

// compactQueueSeparator renders one row's worth of the compact form's
// per-issue delimiter at width display columns, styled RoleDim so it reads as
// chrome rather than content (ADR 0031, issue #1752).
func compactQueueSeparator(width int) string {
	if width < 1 {
		width = 1
	}
	return roleStyle(RoleDim).Render(strings.Repeat(compactQueueSeparatorGlyph, width)) + "\n"
}

// compactRowLines is the line count one compact queue entry's header and title
// block spends, excluding the separator renderTable inserts between entries
// (issue #1752).
const compactRowLines = 2

// compactColumnItemBudget is columnItemBudget's compact-form counterpart: how
// many compact entries fit a Section's row budget, each spending
// compactRowLines plus a separator for every entry but the first. N solves
// N*compactRowLines + (N-1) <= available (issue #1752).
func compactColumnItemBudget(columnBudget int) int {
	available := columnBudget - 1 // header row
	if available <= 0 {
		return 0
	}
	return (available + 1) / (compactRowLines + 1)
}

// compactWorkRow renders one work-Section Pick in the compact form: a
// "#num · state · age" header line with the cursor marker and any extras,
// then the title on a line of its own so a narrowed queue column stops
// clipping it down to a sliver (issue #1752). title must arrive already
// sanitized (SanitizeControlSequences), like the classic row's.
func compactWorkRow(width int, marker string, p Pick, title string, role Role, extras string) string {
	stateText := clip(p.State.String(), stateColWidth, false)
	// number and age reuse the classic form's column budgets as a defensive
	// cap; real values never approach them. clip("#"+p.Number, ...), not
	// "#"+clip(p.Number, ...), so the cap is numberColWidth total rather than
	// numberColWidth plus an unclipped "#" (issue #1752 review).
	number := clip("#"+p.Number, numberColWidth, false)
	age := clip(p.Age, ageColWidth, false)
	// plainPrefix is measured before roleStyle wraps stateText in ANSI
	// escapes, so extrasWidth counts display columns and not escape bytes.
	plainPrefix := fmt.Sprintf("%s %s · %s · %s", marker, number, stateText, age)
	extrasWidth := width - runewidth.StringWidth(plainPrefix)
	if extrasWidth < 0 {
		extrasWidth = 0
	}
	header := fmt.Sprintf("%s %s · %s · %s%s\n", marker, number, roleStyle(role).Render(stateText), age, clip(extras, extrasWidth, false))
	return header + compactQueueTitleLine(width, title)
}

// compactQueueTitleLine renders the compact form's title line: an indent, then
// the title given the whole remainder of width. Shared by compactWorkRow and
// compactBacklogRow (issue #1752 review).
func compactQueueTitleLine(width int, title string) string {
	titleWidth := width - runewidth.StringWidth(compactQueueIndent)
	if titleWidth < 1 {
		titleWidth = 1
	}
	return compactQueueIndent + clip(title, titleWidth, false) + "\n"
}

// compactBacklogRow renders one Backlog issue in the compact form: a
// "#num [labels]" header line with the cursor marker, then the title on a line
// of its own (issue #1752). title and labels must arrive already sanitized
// (SanitizeControlSequences), like the classic row's.
func compactBacklogRow(width int, marker, number, title string, labels []string) string {
	// clip("#"+number, ...), not "#"+clip(number, ...), matching the classic
	// row exactly and staying in sync with labelsWidth below (#1752 review).
	number = clip("#"+number, numberColWidth, false)
	// The four literal columns "%s %s [%s]\n" spends outside marker, number,
	// and labels: the space before number, and " [" and "]" around labels.
	const backlogHeaderLiteralWidth = 4
	labelsWidth := width - runewidth.StringWidth(marker) - runewidth.StringWidth(number) - backlogHeaderLiteralWidth
	if labelsWidth < 0 {
		labelsWidth = 0
	}
	header := fmt.Sprintf("%s %s [%s]\n", marker, number, clipLabels(labels, labelsWidth))
	return header + compactQueueTitleLine(width, title)
}

// truncateWithEllipsis fits s into exactly width display columns, marking the
// cut with a trailing "…" (issue #1779). runewidth.Truncate can stop a column
// short when a wide rune straddles the boundary, so the result is re-measured
// and padded back to exactly width rather than trusted as-is (issue #1785).
func truncateWithEllipsis(s string, width int) string {
	if width <= 1 {
		return runewidth.Truncate(s, width, "")
	}
	cut := runewidth.Truncate(s, width-1, "") + "…"
	return cut + strings.Repeat(" ", width-runewidth.StringWidth(cut))
}

// clip fits s into width display columns, not runes, since a wide CJK rune is
// two columns (issue #859). An over-width s truncates with a trailing ellipsis
// and lands on exactly width whatever pad says; a short s is space-padded only
// when pad is true.
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

// clipLabels fits a label list into width display columns. Unlike clip's
// ellipsis, it drops whole labels from the tail and replaces them with a "+N"
// count, so no label text is mangled mid-word (issue #1631).
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
	// Not even one whole label fits alongside its count, so fall back to a
	// bare "+N" for every label, clipped further if that itself overflows.
	return clip(bare, width, false)
}

// bannerErrWidth bounds a single-line header error banner to one row's worth
// of text. RunNixBuild wraps many lines of merged nix output into one error,
// so printing it unbounded blew the banner out to arbitrary length (issue
// #1131, #1218). Fixed rather than tied to m.Width, since this only needs to
// be one reasonable terminal row.
const bannerErrWidth = 200

// clipBannerErr collapses an error's embedded newlines to single spaces and
// clips the result to width, so a header error banner stays one row however
// verbose the underlying error was (issue #1131).
func clipBannerErr(s string, width int) string {
	return clip(strings.Join(strings.Fields(s), " "), width, false)
}

// headerTitle is the Console's fixed wordmark, folded into the header panel's
// top border rule by renderBoxedHeader rather than rendered as its own
// interior banner (issue #1798).
const headerTitle = "spindrift"

// renderHeader renders the Console's full-width header: a status line, then
// six alert lines in fixed order with no priority or dismissal logic, since
// any subset can be true at once. The waiting/held/settled/failed counts come
// from the Picks slice, this session's own launches (issue #843, ADR 0025);
// recoverable counts prior-run state absent from Picks (#2255, ADR 0039).
func renderHeader(m Model) string {
	return renderHeaderWith(m, styledText)
}

// renderHeaderWith is renderHeader's body over the styleFunc seam (issue
// #3019), so a pure caller drives it with plainText and gets the same text and
// line count back, instead of a second implementation that could drift.
func renderHeaderWith(m Model, style styleFunc) string {
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
	// The status line always renders, even in a launch-less session reading
	// `running 0/0`: this context must not disappear when the queue is empty
	// (issue #843 AC5). Each segment is styled by its own role (ADR 0031), so
	// the content survives styling as separate substrings (issue #1499).
	fmt.Fprintf(&b, "%s · %s · %s · %s · %s · %s\n",
		style(RoleRunning, fmt.Sprintf("running %d/%d", m.Live, m.Cap)),
		style(RoleDim, fmt.Sprintf("waiting %d", waiting)),
		style(RoleHeld, fmt.Sprintf("held %d", held)),
		style(RoleSettled, fmt.Sprintf("settled %d", settled)),
		style(RoleFailed, fmt.Sprintf("failed %d", failed)),
		style(RoleRecoverable, fmt.Sprintf("recoverable %d", m.RecoverableCount)))
	if m.RebuildStatus.Stale {
		b.WriteString(style(RoleHeld, fmt.Sprintf("%s image stale: %s — new launches held; press [b] to rebuild", glyphWarning, m.RebuildStatus.Message)))
		b.WriteString("\n")
	}
	if m.RebuildStatus.Rebuilding {
		b.WriteString(style(RoleRunning, glyphRebuilding+" rebuilding image..."))
		b.WriteString("\n")
	}
	if m.RebuildStatus.Err != "" {
		// Only the glyph and label are styled: the clipped error text must
		// keep its trailing "…" as the line's literal last character, with no
		// styling reset after it, or TestView_RebuildErr_Truncated breaks.
		fmt.Fprintf(&b, "%s %s\n",
			style(RoleFailed, glyphWarning+" rebuild failed:"),
			clipBannerErr(m.RebuildStatus.Err, bannerErrWidth))
	}
	if m.OrphanRecoveryErr != "" {
		// Same split as RebuildErr above, same reason.
		fmt.Fprintf(&b, "%s %s\n",
			style(RoleFailed, glyphWarning+" orphan adopt failed:"),
			clipBannerErr(m.OrphanRecoveryErr, bannerErrWidth))
	}
	if m.RebuildStatus.BranchSwitchNotice != "" {
		b.WriteString(style(RoleDim, fmt.Sprintf("%s notice: %s", glyphNotice, m.RebuildStatus.BranchSwitchNotice)))
		b.WriteString("\n")
	}
	if m.RebuildStatus.StaleDrainSummary != "" {
		b.WriteString(style(RoleDim, fmt.Sprintf("%s notice: %s", glyphNotice, strings.TrimPrefix(m.RebuildStatus.StaleDrainSummary, "==> "))))
		b.WriteString("\n")
	}
	if m.DogfoodLive {
		b.WriteString(style(RoleDim, glyphNotice+" notice: a live dogfood loop (.spindrift/dogfood.pid) is competing for the same queue"))
		b.WriteString("\n")
	}
	return b.String()
}

// renderHelp renders the "?" overlay, replacing the backlog and queue
// rendering entirely while open (issue #784). The lines come from keymap:
// each Binding with non-empty Help contributes its own, in declared order
// (issue #1789).
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

// positionLabel returns a compact " (X-Y of N)" indicator for the rows vp
// renders at itemBudget of total, or "" when there is no range to show, so a
// column rendering no rows never grows a misleading "(1-0 of 0)" label (issue
// #1037 AC3). vp.height is set directly, for renderTable's reason.
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

// sectionPageSize returns the rows one page jump moves the active Section's
// viewport by: the count actually rendered at the current offset, not the raw
// item budget. A truncated window holds one row back for the "N more below"
// affordance, so paging by the raw budget would overshoot and skip the row
// past the fold (issue #1037 AC1/AC2, ADR 0030).
func sectionPageSize(m Model, l layout) int {
	itemBudget := queueItemBudget(l.compact, l.listContentBudget)
	if itemBudget <= 0 {
		return 0
	}
	total := sectionRowCount(m, m.ActiveSection)
	vp := Viewport{offset: m.Offset, height: itemBudget}
	shown, _ := vp.Window(total).Shown()
	return shown
}

// columnItemBudget converts a Section's row budget into the budget for its
// item rows alone, the "-1 for the header". Window.Shown() already holds a row
// back for the "… N more below" line, and the trailing-"\n" slack comes from
// bodyBudget (issue #1825), so do not reserve a second row here: that was
// #1794's fix, reverted because it double-counted.
func columnItemBudget(columnBudget int) int {
	if columnBudget <= 0 {
		return 0
	}
	return columnBudget - 1
}

// queueItemBudget is columnItemBudget's compact-aware wrapper. Callers pass
// layout.compact rather than re-deriving it, so the cursor-follow and
// page-size math never assumes the classic one-line-per-item budget while the
// compact form is what renders (issue #1752).
func queueItemBudget(compact bool, columnBudget int) int {
	if compact {
		return compactColumnItemBudget(columnBudget)
	}
	return columnItemBudget(columnBudget)
}

// windowSidebarLines returns s.Lines windowed through a Viewport at s.Offset,
// budget rows deep, so a render joins only what the viewport shows instead of
// the whole tail of a multi-MB transcript: 21.0MB and 7 allocs per op down to
// 3.39KB and 5 (issues #722, #1016). A non-positive budget yields nil, since
// Viewport's SetHeight(0) means unbounded, not zero lines.
func windowSidebarLines(s SidebarState, budget int) []string {
	if budget <= 0 {
		return nil
	}
	vp := Viewport{offset: s.Offset, total: len(s.Lines)}
	vp.SetHeight(budget)
	w := vp.Window(len(s.Lines))
	return s.Lines[w.Start:w.End]
}

// headerFooterLines is the sidebar chrome budget (label plus footer) that
// renderSidebarFullscreen and Update's tail both subtract from height, shared
// so the clamp's last-page cap matches what the render has room to show
// (issues #829, #1002). renderSidebarDocked uses sidebarDockedFooterLines.
const headerFooterLines = 2

// trailingNewlineRow is the extra row renderRebuildOutputPane,
// renderSidebarFullscreen, and their model.go cursor-follow mirrors reserve
// for View's trailing "\n" (issues #1827, #1841). Without it, output that
// exactly fills the budget renders m.Height lines and runs one over once that
// newline counts as a row. Named and shared so the budgets cannot drift.
const trailingNewlineRow = 1

// sidebarDockedFooterLines is the docked sidebar's chrome budget, the footer
// alone, that renderSidebarDocked and Update's tail subtract from bodyBudget.
// It is narrower than headerFooterLines because the docked panel's label folds
// into its border title instead of spending an interior row (issue #1799).
const sidebarDockedFooterLines = 1

// listFooterLines is the plain list body's chrome budget, the footer alone,
// that renderBody and viewBody's reservedLines subtract for ModeList (issue
// #1792).
const listFooterLines = 1

// sidebarErr returns the error the current view should show: s.Err always,
// otherwise s.TranscriptErr only while ShowTranscript is true, since a
// Transcript-only load failure must never blank out an otherwise-good
// Activity feed (#1501 review).
func sidebarErr(s SidebarState) error {
	if s.Err != nil {
		return s.Err
	}
	if s.ShowTranscript {
		return s.TranscriptErr
	}
	return nil
}

// sidebarLabel renders s's one-line pane header: "activity #N", "transcript
// #N" once toggled, "(raw)" appended while ShowRaw (#1501). The Activity
// feed's "[follow]"/"[paused]" tag is the only render-level signal of whether
// the feed is live-tailing or detached after a scroll-up (#1502, ADR 0030);
// the Transcript is a one-shot load, so the tag would mean nothing there.
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

// wrapText greedily word-wraps s into lines of at most width display columns,
// preserving blank lines verbatim. It is hand-rolled because there is no
// glamour renderer in the dependency tree (issue #1632). A word wider than width
// stands alone on its own overflowing line rather than breaking mid-word.
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

// detailModalTitleLines is the fullscreen detail modal's number/title header
// row spend, shared by renderDetailModal and detailModalScrollBudget's Offset
// clamp so the clamp matches what the render has room to show (issue #1632).
// The labels line is not a fixed spend alongside it: it wraps onto further
// rows, counted via detailModalLabelLinesCapped (issue #1832).
const detailModalTitleLines = 1

// detailModalLines flattens s's word-wrapped body and its Blocked-by/Blocks
// sections into one scrollable line list, computed once when
// DetailModalLoadedMsg lands rather than re-wrapped on every keystroke (issue
// #722's caching). An empty section contributes no lines at all (#1632).
func detailModalLines(width int, s DetailModalState) []string {
	lines := wrapText(SanitizeControlSequences(s.Body), width)
	lines = append(lines, detailModalBlockerLines("Blocked by", s.BlockedBy)...)
	lines = append(lines, detailModalBlockerLines("Blocks", s.Blocks)...)
	return lines
}

// detailModalBlockerLines renders one of the detail modal's Blocked-by or
// Blocks sections: a blank separator, a header, then one line per BlockerRef.
// It returns nil for empty refs so the section grows no empty header (#1632).
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

// windowDetailModalLines returns s.Lines windowed through a Viewport at
// s.Offset, budget rows deep (issue #1632).
func windowDetailModalLines(s DetailModalState, budget int) []string {
	if budget <= 0 {
		return nil
	}
	vp := Viewport{offset: s.Offset, total: len(s.Lines)}
	vp.SetHeight(budget)
	w := vp.Window(len(s.Lines))
	return s.Lines[w.Start:w.End]
}

// detailModalBoxWidthPercent and detailModalBoxHeightPercent are the share of
// the terminal the floating detail modal box targets before the min/max clamps
// apply, so the box scales with the terminal instead of shrinking by a fixed
// margin (issue #1759 AC).
const (
	detailModalBoxWidthPercent  = 80
	detailModalBoxHeightPercent = 80
)

// detailModalBoxMinWidth and detailModalBoxMinHeight floor the floating box at
// a legible size (issue #1759 AC). detailModalFits gates the floating layout
// on the terminal being at least this large, so this clamp never inflates the
// box past the terminal's own size.
const (
	detailModalBoxMinWidth  = 40
	detailModalBoxMinHeight = 10
)

// detailModalBoxMaxWidth and detailModalBoxMaxHeight cap the floating box at a
// comfortable reading size on a large terminal instead of stretching it corner
// to corner (issue #1758 AC; width widened from 84 by issue #1796).
const (
	detailModalBoxMaxWidth  = 100
	detailModalBoxMaxHeight = 30
)

// detailModalBoxSize returns the floating detail modal box's outer width and
// height for a termWidth x termHeight terminal (issue #1759 AC). Only
// meaningful when detailModalFits(m) is true: below that threshold the min
// clamp would inflate the box past the terminal's own size.
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

// detailModalInnerSize returns the floating detail modal box's interior size:
// the outer size minus the one-column, one-row border on every side. The
// width-dependent modal machinery must key off this and not Model.Width or
// Model.Height, so a resize and the box's render agree on the body's wrap
// width (issue #1758).
func detailModalInnerSize(termWidth, termHeight int) (width, height int) {
	boxWidth, boxHeight := detailModalBoxSize(termWidth, termHeight)
	return modalBoxInnerSize(boxWidth, boxHeight)
}

// sidebarModalBoxWidthPercent and sidebarModalBoxHeightPercent are the log
// modal's share of the terminal, the same target the detail modal uses (issue
// #1845). The two modals share this percent but diverge on the max clamp
// (issue #1875).
const (
	sidebarModalBoxWidthPercent  = 80
	sidebarModalBoxHeightPercent = 80
)

// sidebarModalBoxMinWidth and sidebarModalBoxMinHeight floor the log modal's
// floating box at the detail modal's legibility floor. sidebarModalFits gates
// the floating layout on the terminal being at least this large, so this clamp
// never inflates the box past the terminal's own size (issue #1845).
const (
	sidebarModalBoxMinWidth  = 40
	sidebarModalBoxMinHeight = 10
)

// sidebarModalBoxMaxWidth and sidebarModalBoxMaxHeight cap the log modal's
// floating box deliberately larger than the detail modal's, so [z] reads as
// visibly bigger on a roomy terminal (issue #1845 unified the two caps; issue
// #1875 reverses that), while still staying short of corner to corner.
const (
	sidebarModalBoxMaxWidth  = 180
	sidebarModalBoxMaxHeight = 54
)

// sidebarModalBoxSize returns the floating log modal box's outer width and
// height for a termWidth x termHeight terminal (issue #1845).
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

// sidebarModalBoxOrigin centers a boxWidth x boxHeight box within a
// termWidth x termHeight terminal, the (x, y) compositeOverlay places it at
// (issue #1845).
func sidebarModalBoxOrigin(termWidth, termHeight, boxWidth, boxHeight int) (x, y int) {
	return modalBoxOrigin(termWidth, termHeight, boxWidth, boxHeight)
}

// sidebarModalInnerSize returns the floating log modal box's interior
// width and height for a termWidth x termHeight terminal (issue #1845).
func sidebarModalInnerSize(termWidth, termHeight int) (width, height int) {
	boxWidth, boxHeight := sidebarModalBoxSize(termWidth, termHeight)
	return modalBoxInnerSize(boxWidth, boxHeight)
}

// padBaseForOverlay pads every line of s out to at least width display columns
// and appends blank lines until s has at least height lines. compositeLine
// leaves a row too short to reach the box's x origin untouched, and
// compositeOverlay only overwrites rows base already has, so a base at its own
// natural size must be padded to the full frame first (issue #1758).
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

// padDisplay pads or truncates s to exactly width display columns: every
// interior row of the floating box must land on its inner width or the side
// border runes drift out of column (issue #1758, ellipsis per #1779). It
// measures with ansi.StringWidth so an already-styled row is safe, but the
// truncate branch stays runewidth-based and needs plain, pre-clipped content.
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

// detailModalFooterLines is the floating detail modal box's footer row spend,
// shared by renderDetailModalContent's body budget and Update's Offset clamp
// so the two never disagree on what the footer costs (issue #1772 review).
const detailModalFooterLines = 1

// detailModalLabelLines word-wraps a ticket's labels to width as one
// comma-separated, bracketed, dim-styled string, the backlog row's `[bug,
// console]` idiom (issue #1832, ADR 0031). The brackets are literal runes
// wrapText counts toward width. Each line is clipped before styling, never
// after, or padDisplay's runewidth truncate would mangle the escapes (#1772).
func detailModalLabelLines(labels []string, width int) []string {
	return detailModalLabelLinesWith(labels, width, styledText)
}

// detailModalLabelLinesWith is detailModalLabelLines's body over the styleFunc
// seam, so detailModalScrollBudget can predict the wrapped line count through
// plainText instead of paying for a real lipgloss render (issue #3019).
func detailModalLabelLinesWith(labels []string, width int, style styleFunc) []string {
	sanitized := make([]string, len(labels))
	for i, l := range labels {
		sanitized[i] = SanitizeControlSequences(l)
	}
	lines := wrapText("["+strings.Join(sanitized, ", ")+"]", width)
	for i, l := range lines {
		lines[i] = style(RoleDim, clip(l, width, false))
	}
	return lines
}

// detailModalLabelLinesCapped wraps labels like detailModalLabelLines but caps
// the result at maxLines, dropping labels from the tail for a "+N more labels"
// entry kept inside the bracket (#1631, #1832). Without the cap, a ticket with
// enough labels to fill the interior loses its footer and body to a silent
// tail-truncate (#1778). maxLines <= 0 yields the bare indicator alone.
func detailModalLabelLinesCapped(labels []string, width, maxLines int) []string {
	return detailModalLabelLinesCappedWith(labels, width, maxLines, styledText)
}

// detailModalLabelLinesCappedWith is detailModalLabelLinesCapped's body over
// the styleFunc seam. Its trial call must thread the identical style through,
// or the plain and styled variants cap at different label counts (#3019).
func detailModalLabelLinesCappedWith(labels []string, width, maxLines int, style styleFunc) []string {
	lines := detailModalLabelLinesWith(labels, width, style)
	if len(labels) == 0 || len(lines) <= maxLines {
		return lines
	}
	for k := len(labels) - 1; k >= 0; k-- {
		trial := detailModalLabelLinesWith(append(append([]string{}, labels[:k]...), fmt.Sprintf("+%d more labels", len(labels)-k)), width, style)
		if len(trial) <= maxLines {
			return trial
		}
	}
	return []string{fmt.Sprintf("+%d more labels", len(labels))}
}

// renderDetailModalContent renders the floating detail modal box's interior as
// exactly innerHeight lines: the labels line, the loading, error, or body
// window, and the footer hint. It wraps and scrolls against the box interior,
// never Model.Width or Model.Height (issue #1758).
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
	// appended, so a too-long labels or status block never pushes the footer
	// off the end (issue #1778). Where labels alone consume contentBudget,
	// this drops the loading or error line: the visible "+N more labels"
	// indicator takes precedence over the status text, never the reverse.
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

// renderDetailModalBox renders s as a bordered floating box of exactly
// width x height display cells: "#number title" in the top border, the
// interior from renderDetailModalContent, and every row padded to width so
// compositeOverlay fully occludes the list content behind it (issue #1758).
// The shared boxing helper degrades the border to ASCII (issue #1797).
func renderDetailModalBox(s DetailModalState, width, height int) string {
	if width < 4 || height < 3 {
		return ""
	}
	innerWidth := width - 2
	innerHeight := height - 2
	title := fmt.Sprintf("#%s %s", SanitizeControlSequences(s.Number), SanitizeControlSequences(s.Title))

	lines := renderDetailModalContent(s, innerWidth, innerHeight)
	// Each content line must be clipped to exactly innerWidth before it
	// reaches renderBoxedColumn: lipgloss's Width() only pads a line up, never
	// truncates it down, so an over-wide line (a label wrapText left unbroken,
	// say) would widen the whole box instead of getting cut (#1779, #1785).
	for i, l := range lines {
		lines[i] = padDisplay(l, innerWidth)
	}
	return renderBoxedColumn(strings.Join(lines, "\n"), innerWidth, title, RoleDim)
}

// renderDetailModal renders a Backlog issue's fullscreen ticket detail modal:
// number and title, labels capped through detailModalLabelLinesCapped so this
// stays in parity with the floating box (#1832), and once the async fetch
// lands a wrapped body plus Blocked-by/Blocks sections (#1632). It opens
// before that fetch resolves, so "loading..." stands in until it fills.
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

// blockerOpenGlyph and blockerClosedGlyph mark a BlockerRef's open or closed
// state ahead of the spelled-out state word (issue #1632).
const (
	blockerOpenGlyph   = "✗"
	blockerClosedGlyph = "✓"
)

// formatBlockerRef renders one Blocked-by or Blocks entry: a state glyph, the
// issue number, its dependency source, its state spelled out, and its title,
// as in `✗ #1540 (native) open "Waves core"` (issue #1632 AC).
func formatBlockerRef(r BlockerRef) string {
	glyph := blockerOpenGlyph
	if r.State == forge.IssueClosed || r.State == forge.IssueMerged {
		glyph = blockerClosedGlyph
	}
	state := strings.ToLower(string(r.State))
	if state == "" {
		// resolveBlockerRef leaves State and Title blank when the ref was
		// deleted or its fetch erred, so render "unknown" rather than a bare
		// double space and an empty quoted string (issue #1632 review).
		state = "unknown"
	}
	title := SanitizeControlSequences(r.Title)
	if title == "" {
		title = "unknown"
	}
	// forge.Ref owns the "#N (source)" annotation every other
	// blocker-diagnostic call site shares, so reusing it keeps this format
	// from drifting from theirs (issue #1632 review).
	return fmt.Sprintf("%s %s %s %q", glyph, forge.Ref(r.Number, r.Source), state, title)
}

// renderSidebarFullscreen renders one Dispatch's live-tail sidebar at full
// terminal size, the fallback View reaches for when sidebarFits is false: a
// header, as much loaded content as height allows, and a keystroke hint, with
// Err in place of content. The label, footer, and Err line are themselves
// budgeted against height, so at height 1 only the label renders (#1534).
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

// sidebarModalLabelLines is the one row renderSidebarModalContent reserves for
// sidebarLabel, so the floating box keeps the activity/transcript and
// follow-state signal the fullscreen takeover showed (issue #1845).
const sidebarModalLabelLines = 1

// renderSidebarModalContent renders s's body for the floating log modal,
// windowed to innerWidth x innerHeight (issue #1845). Its chrome rows (label,
// an error line in place of content, footer) are budgeted like
// renderSidebarFullscreen's, so a resize never renders past innerHeight.
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

// renderSidebarModalBox renders s as a bordered floating box of exactly
// width x height display cells, with "#number title" in the top edge (issue
// #1845 AC). Every row is padded so compositeOverlay fully occludes the list
// content behind it, matching renderDetailModalBox.
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

// footerHintWidth returns the width left for an overlay's hint text once its
// literal prefix has eaten into the same line. The floor of 1 for a positive
// total keeps a narrow terminal from going negative and falling through
// renderFooterHints' width<=0 "leave unclipped" sentinel (issue #1818 review).
// total<=0, an unset Model.Width, still reaches that sentinel unchanged.
func footerHintWidth(total int, prefix string) int {
	w := total - lipgloss.Width(prefix)
	if total > 0 && w < 1 {
		w = 1
	}
	return w
}

// renderFooterHints renders one mode's pinned keystroke-hint line from
// keymap's Footer text, joined and dim-styled, so every footer shares one
// source (issues #1789, #1791). width clips the line before styling, never
// after, or clip() miscounts ANSI escape bytes as display columns; 0 leaves it
// unclipped, the sentinel renderRebuildOutputPane relies on (issue #1818).
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
// beside the list (ADR 0030): content clipped to width so an overflowing line
// cannot blow out the column join, and budgeted in rows to match renderTable
// so both columns agree before JoinHorizontal pads the shorter. The caller
// renders the label in the panel's border title, not here (issue #1799).
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
	// hints with full separators overflow sidebarWidth's 42-column budget, so
	// the space after each "·" goes and "z" shortens to "[z]" (FooterCompact)
	// to fit all five, "H/L" included (issue #1846).
	b.WriteString(renderFooterHints(ModeSidebar, []string{"t", "h", "x", "z", "H"}, width, true))
	b.WriteString("\n")
	return b.String()
}

// renderRebuildOutputPane renders the last rebuild's captured nix output
// full-screen from RebuildOutputOffset onward, plus a close-key hint (issue
// #1128). It has no docked or floating mode: the output is a flat log, not a
// Transcript worth keeping beside the backlog.
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
