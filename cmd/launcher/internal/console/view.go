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
// docked panel's rounded border adds — one column and one row per edge, on
// all four sides. dockedBorderCols is the docked layout's total column
// overhead: both the list panel and the sidebar panel pay boxBorderCols,
// replacing the old one-column divider between them with two adjacent box
// edges (issue #1755).
const (
	boxBorderCols    = 2
	boxBorderRows    = 2
	dockedBorderCols = boxBorderCols * 2
)

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
// dockedBorderCols for the two panels' bordered edges — the gate View and
// handleKey both check before choosing the docked layout over the
// fullscreen fallback, so the two can never disagree about which one is
// showing (issue #1500's sectionTabsReserved precedent, extended to the
// sidebar, widened for the panel borders by issue #1755).
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
// — the trigger for the compact/wrapped queue-row form (issue #1752). Mirrors
// View's own condition for choosing the docked layout over the fullscreen
// sidebar takeover (m.SidebarZoom or !sidebarFits(m)), so a caller checking
// before the list is even rendered (model.go's cursor-follow) can never
// disagree with what View ends up drawing: a fullscreen sidebar, zoomed or
// too-narrow-to-dock, hides the list entirely, so it never counts as
// "narrowed."
func queueNarrowed(m Model) bool {
	return m.Sidebar != nil && !m.SidebarZoom && sidebarFits(m)
}

// padColumnsToEqualHeight pads the shorter of the list and sidebar columns'
// rendered content with trailing blank lines up to the taller one's line
// count, so their bordered boxes close on the same row instead of the
// shorter panel's border floating above a blank gap while the taller one
// continues (issue #1755). Both are already budgeted from the same
// panelBudget, so the only way they legitimately differ is by how much of
// that shared budget each one's own content actually used.
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

// renderBoxedColumn wraps content in a muted (RoleDim) rounded border — the
// bordered-panel look that replaces the bare column divider between the
// docked list and sidebar, so the split reads as two distinct boxes (issue
// #1755). content's lines are assumed already clipped to the panel's
// interior width; renderBoxedColumn only adds the border around them, sized
// to exactly that width so the two panels' edges line up regardless of how
// short any individual line is. Under NO_COLOR or a dumb terminal
// (colorProfile() degrading to termenv.Ascii), the border falls back to
// plain ASCII glyphs instead of the rounded Unicode box-drawing set, the
// same degradation renderHeader's role coloring already follows. Empty
// content renders no box at all: callers pad the shorter of the list and
// sidebar columns to match the taller one before boxing either
// (padColumnsToEqualHeight), so this only ever fires when both are empty —
// a zero-height budget must not draw a stray empty frame.
//
// With title == "", the top border is the plain rule above — untouched, so
// every existing untitled call site (header, docked list, docked sidebar)
// renders exactly the bytes it always has. With title set, the top border
// instead folds it into the rule itself — "╭─ title ─…─╮" ("+- title -…-+"
// under the ASCII fallback) — generalizing the detail modal's title-in-border
// trick (issue #1758) into this one shared helper so the modal's border gets
// the same ASCII degradation every other panel already has (issue #1797).
// titleRole lets a future caller (the sidebar's focus indicator) color the
// title text distinctly from the border rule; RoleDim matches the border and
// reproduces today's look.
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
// display columns, folding title into the rule: the border's own corner and
// top-rule glyphs (already ASCII-degraded by the caller's choice of border),
// a one-rune lead-in, the title, then rule fill out to width — generalized
// from the detail modal's original hand-rolled Unicode-only top border
// (issue #1758) so both the rounded and ASCII rule sets pass through. A
// title too wide for the panel truncates with an ellipsis (runewidth.Truncate,
// the same primitive truncateWithEllipsis uses); fill is recomputed from the
// title's actual rendered width afterward, so the rule always lands on
// exactly width regardless of how short Truncate's own output comes in
// (issue #1785's wide-rune-boundary lesson).
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
		// A panel too narrow even for the lead-in/trailing space (inner <
		// structural) can't be fixed by shrinking the title alone — clamp
		// the whole label together instead, so the rule never overflows
		// width regardless of how small inner is (issue #1797 review).
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
// full-width header (wordmark, status line, stale/dogfood alerts), the Section
// tabs, the active Section's own aligned table, and any refresh error (ADR
// 0030). An open sidebar (m.Sidebar != nil) docks beside the still-visible
// list when sidebarFits, or takes over fullscreen on a terminal too narrow
// to show both (ADR 0030, #1501) — replacing the interim fullscreen-only
// drill-in of issue #1500. An open detail modal (m.DetailModal != nil)
// floats as a bordered box over the still-rendered list instead of a
// fullscreen takeover (issue #1758) — the same "keep driving while you
// read" shape ADR 0030's sidebar already established for the transcript —
// unless detailModalFits rejects the terminal as too small for a legible
// box, in which case it falls back to the fullscreen renderer instead
// (issue #1759).
func View(m Model) string {
	if m.DetailModal != nil && !detailModalFits(m) {
		return renderDetailModal(*m.DetailModal, m.Height)
	}
	base := viewBody(m)
	if m.DetailModal != nil {
		boxWidth, boxHeight := detailModalBoxSize(m.Width, m.Height)
		x, y := detailModalBoxOrigin(m.Width, m.Height, boxWidth, boxHeight)
		box := renderDetailModalBox(*m.DetailModal, boxWidth, boxHeight)
		base = dimBase(padBaseForOverlay(base, m.Width, y+boxHeight))
		base = compositeOverlay(base, box, x, y)
	}
	return base
}

// renderBoxedHeader renders the status/alert block (renderHeader), wrapped in
// the same muted-border panel look as the docked list/sidebar (issue #1756),
// with the "spindrift" wordmark folded into the panel's top border rule
// instead of sitting on an interior row (issue #1798) — so it reads as its
// own region instead of running straight into the Section tabs below it. The
// result always ends in exactly one
// trailing newline, matching renderHeader's own convention, so callers never
// have to special-case the boxed-vs-unboxed cases. bodyBudget's row-budget
// math must count exactly these rendered rows — including the 2 border rows
// once boxed — or Update's cursor-follow scroll clamps against a taller
// viewport than View actually has room to show, stranding the list's last
// rows behind the border (same class of bug issue #1755 hit for the docked
// panels); both callers therefore go through this one helper rather than
// each computing the header's line count independently. Below
// boxBorderCols+1 columns wide, or with less height than the boxed header
// actually renders to, there's no room for a border at all — the header
// then renders unboxed rather than forcing a degenerate box or overrunning
// Height on an extremely short terminal (issue #1035 AC1/AC2's invariant).
// Height fitness is checked against the boxed render's own line count,
// rather than predicted from the unboxed content's newline count, because
// renderHeader doesn't pre-wrap its content to m.Width the way the docked
// list/sidebar's content is pre-clipped before boxing — a narrow terminal
// can make the box's own word-wrap add lines the unboxed count wouldn't
// predict. This is also what lets tests that never send a SizeChangedMsg
// (m.Width's zero value) exercise header content without caring about
// borders.
func renderBoxedHeader(m Model) string {
	header := renderHeader(m)
	if headerWidth := m.Width - boxBorderCols; headerWidth > 0 {
		if boxed := renderBoxedColumn(header, headerWidth, headerTitle, RoleDim) + "\n"; strings.Count(boxed, "\n") <= m.Height {
			return boxed
		}
	}
	return header
}

// viewBody renders everything View shows below/behind an open detail modal —
// the header, Section tabs, and either the docked sidebar layout or the
// plain single-list body — the same rendering the list-only path always
// used, now split out so View can composite the floating detail modal box
// over it instead of a fullscreen replacement (issue #1758).
func viewBody(m Model) string {
	if m.Sidebar != nil && (m.SidebarZoom || !sidebarFits(m)) {
		return renderSidebarFullscreen(*m.Sidebar, m.Height)
	}
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
		fmt.Fprintf(&b, "/%s  %s\n", m.Filter,
			renderFooterHints(ModeFilterEdit, []string{"enter", "esc"}, 0, false))
		reservedLines++
	}
	if m.Mode == ModeTerminateConfirm {
		fmt.Fprintf(&b, "terminate #%s? %s\n", m.TerminateConfirm.Number,
			renderFooterHints(ModeTerminateConfirm, []string{"y"}, 0, false))
		reservedLines++
	}
	if m.Mode == ModeQuitConfirm {
		fmt.Fprintf(&b, "%s\n", renderFooterHints(ModeQuitConfirm, []string{"d"}, 0, false))
		reservedLines++
	}
	if m.Mode == ModePick && m.HasHighlighted() {
		fmt.Fprintf(&b, "p_  %s\n", renderFooterHints(ModePick, []string{"a", "r"}, 0, false))
		reservedLines++
	}
	if m.QueueEnterNotice != "" {
		fmt.Fprintf(&b, "%s\n", m.QueueEnterNotice)
		reservedLines++
	}
	if m.Err != nil {
		// The refresh-error line renders after the body (below), but must
		// still be subtracted from budget up front or a long list plus an
		// error together overflow Height by one line (issue #1035 review
		// finding).
		reservedLines++
	}
	budget := m.Height - headerLines - reservedLines
	if budget < 0 {
		budget = 0
	}
	// Computed once, here, against m before any width narrowing below —
	// queueNarrowed(listModel) would compare listModel's already-narrowed
	// Width against sidebarFits' full-width threshold and misfire. Threaded
	// through explicitly rather than re-derived inside renderBody's callees,
	// so there is exactly one source of truth for "is this render compact"
	// instead of two predicates a future caller could drift out of sync
	// (issue #1752 review).
	compact := queueNarrowed(m)
	if m.Sidebar != nil {
		width := computeSidebarWidth(m.Width)
		listModel := m
		listModel.Width = m.Width - width - dockedBorderCols
		// bodyBudget(m) already subtracts boxBorderRows for the docked case
		// (mirrored here so View's own render and Update's scroll/cursor
		// clamps always agree on how many rows the bordered panels actually
		// have room for — issue #1755).
		panelBudget := bodyBudget(m)
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
// column widths — "number", "state", and "age" all have a narrow, bounded
// vocabulary (an issue number, one of eight PickState words, a formatAge
// string), so a fixed width keeps every row's title column starting in the
// same screen column without measuring content first (ADR 0030's "aligned
// ... table"). stateColWidth fits "terminated", the longest PickState word,
// plus its cursor-side padding.
const (
	numberColWidth = 7
	stateColWidth  = 11
	ageColWidth    = 7
)

// sectionTabsLines is the row budget the Section tabs line costs when it
// renders at all — see sectionTabsReserved (issue #1500).
const sectionTabsLines = 1

// sectionTabsReserved returns sectionTabsLines when the terminal has room
// left for the Section tabs line after headerLines (renderHeader's own
// line count), 0 otherwise — the tabs line's own collapse-when-short
// degradation, so an extremely short terminal never renders more than
// Height lines total (issue #1500). Shared by View's own budget calc and
// bodyBudget so the two can never diverge (issue #1035's invariant,
// extended to the tabs line).
func sectionTabsReserved(m Model, headerLines int) int {
	if m.Height <= headerLines {
		return 0
	}
	return sectionTabsLines
}

// roleForSection returns the Role a Section's own content styles with —
// pickSection's Section values map straight onto their same-named Role;
// SectionBacklog, which pickSection never returns, styles as RoleAccent
// instead (ADR 0031).
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
// appends after the five tabs when there's room for it — keymap's own H/L
// and 1-5 Footer text (issue #1789), not a literal of its own.
var sectionTabsHint = fmt.Sprintf(" [%s,%s]", footerHint(ModeList, "H"), footerHint(ModeList, "1"))

// renderSectionTabs renders the fixed row of five Section tabs above the
// body: each names its direct-jump number and Section, the four work tabs
// carry their row count, the active tab styles by its own Role and the rest
// dim, and a trailing hint spells out how to switch (ADR 0030). Kept compact
// (single-space separators, a short hint) so the common case — a handful of
// picks on an 80-column terminal — fits and shows styling; a pick count
// large enough to push past that is rare and degrades gracefully below.
// Measured and clipped as plain text before any styling is applied
// (clip-before-style, the same discipline every other row uses) — clipping
// already-styled text with the runewidth-based clip() would miscount ANSI
// escape bytes as display columns and risks truncating mid-sequence. The
// hint drops first on a narrow terminal; if even the bare tabs overflow,
// they clip with an ellipsis like any other row (issue #1500).
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
// footer hints (issue #1792) — filter, pick, and refresh, the list's own
// action verbs with no other on-screen affordance. Navigation (j/k, g/G,
// pgup/pgdown) and Section-jump (H/L, 1-5, already inline on the Section
// tabs row) are deliberately left out, matching the restraint the other
// three migrated footers already show toward their own modes' full binding
// set (e.g. the sidebar's scroll keys never get a footer entry either).
// Ordered filter/pick/refresh (not keymap's own r-before-p declaration
// order) — the read-then-act sequence an operator actually follows, not an
// accident to "fix" back into keymap order.
var listFooterKeys = []string{"/", "p", "r"}

// renderBody renders the active Section's own table under the header and
// Section tabs (ADR 0030) — the section-switched single list that replaces
// the two-column body of ADR 0025 — followed by ModeList's own pinned
// keystroke-hint footer (issue #1792), the one Console view the shared
// renderer (issue #1791) hadn't reached yet. budget is the row count left
// after the header, tabs, and any prompt lines — always a real, already-
// clamped-to-nonnegative figure from View, never the "unbounded" case (issue
// #1540; Viewport's own height==0 convention covers that for callers who
// want it). Only ModeList spends a row on this footer; the other Modes that
// still reach here (FilterEdit, TerminateConfirm, QuitConfirm, Pick) already
// show their own single-line prompt in that same reserved row instead
// (view.go's viewBody), so showing both would double up. tableBudget's
// "-listFooterLines" mirrors renderSidebarDocked's own
// "-sidebarDockedFooterLines" (view.go) — listContentBudget (view.go) keeps
// Update's cursor/scroll clamp and sectionPageSize in agreement with this
// same reservation.
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
// total, budgeted to itemBudget rows (the header's own row already spent) —
// renderBacklogSection and renderWorkSection's shared windowing plumbing, so
// the two can't drift out of sync (ADR 0030) and so both window through the
// same Viewport rather than re-implementing the slice math inline (issue
// #1540). A non-positive itemBudget writes no rows and no affordance,
// matching a terminal too short to show anything past the header — vp is
// never asked to represent that case (Viewport's SetHeight(0) means
// unbounded, not zero rows), so the guard lives here instead. vp's height is
// set directly rather than through SetHeight: SetHeight's clamp-on-shrink
// (issue #829's page-cap) is Update's job, already folded into the offset
// Model stores by the time a render reaches here — reapplying it against a
// freshly-constructed vp with no prior height would misfire as a shrink from
// unbounded and needlessly re-cap an offset pgup/pgdown deliberately leaves
// uncapped (issue #1060). sep, when non-empty, is written between (not after)
// consecutive rows — the compact/wrapped form's per-issue delimiter, "" for
// the classic single-line form (issue #1752).
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

// extrasBudget is the width reserved for a row's trailing, unaligned content
// — a work row's blocker/reason/heartbeat annotation, or a Backlog row's
// label list — generous enough for a realistic "(held by #41 (native))"
// badge or a two-label issue, clipped further only on an unusually narrow
// terminal. Reserving it up front (rather than letting the title column
// consume the whole remaining width) keeps a joined row's total display
// width at or under m.Width even after the trailing content is appended —
// exceeding it wraps the line in a real terminal and can split an assertion
// substring across the wrap (issue #1500).
const extrasBudget = 30

// backlogFixedWidth is a Backlog row's width outside the title and label
// columns: the cursor marker, the number cell, and the two literal
// separators plus brackets the row format spends (`"%s %s %s [%s]\n"`).
const backlogFixedWidth = 1 + 1 + numberColWidth + 1 + 2 + 1

// renderBacklogSection renders the Backlog Section: one line per visible
// issue (number, title, labels), cursor-marked, under a column-header row —
// ADR 0030's pick source, keeping its `/` label filter and #844's
// number/title/labels shape (state and age don't apply to a plain issue).
// An orphan-flagged row's live heartbeat rides in the same bracket as its
// labels, sharing labelsWidth's existing budget rather than carving out a
// new column (issue #1621).
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
		// "orphan" alongside its real labels — the only Backlog signal that
		// distinguishes it from a Dispatch this session launched, since
		// startup only ever detects it now, never adopts it on its own
		// (issue #1619). Its live heartbeat, read off the same on-disk pass
		// log a session-launched Dispatch's own Heartbeat comes from, joins
		// the same bracket (issue #1621).
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
	// Two spaces, not one, before "labels": each row's own label list sits
	// after a literal " [" (space + bracket), one column wider than a bare
	// space separator — matching it here keeps the header word aligned with
	// where the label text actually starts, not the bracket (issue #1500
	// review).
	headerText := fmt.Sprintf("  %s %s  labels", clip("#", numberColWidth, true), clip("title", titleWidth, true))
	if compact {
		// The classic header's aligned column words no longer describe the
		// compact row's own two-line shape — echo its own header-line format
		// instead of a stale "title ... labels" claim (issue #1752 review).
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
// literal single-space separators the row format spends
// (`"%s %s %s %s %s%s\n"`) — there is no separator between the age cell and
// the trailing extras, which sit flush against it.
const workFixedWidth = 1 + 1 + numberColWidth + 1 + 1 + stateColWidth + 1 + ageColWidth

// renderWorkSection renders whichever work Section is active: one
// pick-ordered line per Pick in that Section, cursor-marked, columned as
// number/title/state/age under a column-header row (ADR 0030) — the state
// cell styled by its own Role (ADR 0031). Held's blocker and Running's
// heartbeat, both #858/#647-era queue-row detail, still render as a trailing
// annotation after the fixed columns so neither signal is lost, just moved
// out of the aligned part of the row.
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
	// Every row in picks is, by sectionPicks' own construction, a PickState
	// pickSection maps onto m.ActiveSection — so its Role is the same for
	// every row, not something to recompute per row.
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
		// isn't named twice on one row (issue #755).
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
	headerText := fmt.Sprintf("  %s %s %s %s", clip("#", numberColWidth, true), clip("title", titleWidth, true), clip("state", stateColWidth, true), "age")
	if compact {
		// The classic header's aligned column words no longer describe the
		// compact row's own two-line shape — echo its own header-line format
		// instead of a stale "title ... state age" claim (issue #1752 review).
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

// compactQueueIndent is the left indent the compact/wrapped queue-row form's
// title line sits at, under its own header line (issue #1752).
const compactQueueIndent = "  "

// compactQueueSeparatorGlyph is the compact/wrapped form's per-issue
// delimiter rune — a faint horizontal rule so the two-line stacked entries
// stay visually distinct instead of running together (issue #1752).
const compactQueueSeparatorGlyph = "─"

// compactQueueSeparator renders one row's worth of the compact form's
// per-issue delimiter at width display columns, styled RoleDim — the console
// palette's muted role (ADR 0031) — so it reads as administrative chrome,
// not content (issue #1752).
func compactQueueSeparator(width int) string {
	if width < 1 {
		width = 1
	}
	return roleStyle(RoleDim).Render(strings.Repeat(compactQueueSeparatorGlyph, width)) + "\n"
}

// compactRowLines is the physical line count one compact/wrapped queue
// entry's own header+title block spends, not counting the separator
// renderTable inserts between (not after) entries (issue #1752).
const compactRowLines = 2

// compactColumnItemBudget is columnItemBudget's compact-form counterpart: it
// converts a Section's row budget (header row included) into how many
// compact entries fit, each spending compactRowLines lines plus one more for
// every entry but the first shown, for its separator from the previous entry
// — item count N solves N*compactRowLines + (N-1) <= available, i.e.
// N <= (available+1)/(compactRowLines+1) (issue #1752).
func compactColumnItemBudget(columnBudget int) int {
	available := columnBudget - 1 // header row
	if available <= 0 {
		return 0
	}
	return (available + 1) / (compactRowLines + 1)
}

// compactWorkRow renders one work-Section Pick in the compact form: a "#num
// · state · age" header line carrying the cursor marker and any trailing
// extras (blocker/reason/heartbeat), followed by the title — clip()ped, not
// wrapped, just given a whole line of its own rather than squeezed beside
// the state/age columns — the narrowed-queue alternative to
// renderWorkSection's classic single-line clip()ped row, so a squeezed queue
// column stops over-clipping the title down to a sliver (issue #1752).
// title is expected pre-sanitized (SanitizeControlSequences), matching the
// classic row's own discipline.
func compactWorkRow(width int, marker string, p Pick, title string, role Role, extras string) string {
	stateText := clip(p.State.String(), stateColWidth, false)
	// number (with its "#") and age reuse the classic form's own column
	// budgets as a defensive cap — real values (a short issue number,
	// formatAge's output) never approach it — rather than leaving them
	// unbounded like extras was before this clip (issue #1752 review).
	// clip("#"+p.Number, ...), not "#"+clip(p.Number, ...): matching the
	// classic row's own clip("#"+p.Number, numberColWidth, true) exactly,
	// so the cell's cap is numberColWidth total, not numberColWidth plus an
	// unclipped literal "#" (issue #1752 review).
	number := clip("#"+p.Number, numberColWidth, false)
	age := clip(p.Age, ageColWidth, false)
	// Measured plain, before roleStyle wraps stateText in ANSI escapes below
	// — the same clip-before-style discipline renderSectionTabs documents,
	// so extrasWidth is computed against display columns, not escape bytes.
	plainPrefix := fmt.Sprintf("%s %s · %s · %s", marker, number, stateText, age)
	extrasWidth := width - runewidth.StringWidth(plainPrefix)
	if extrasWidth < 0 {
		extrasWidth = 0
	}
	header := fmt.Sprintf("%s %s · %s · %s%s\n", marker, number, roleStyle(role).Render(stateText), age, clip(extras, extrasWidth, false))
	return header + compactQueueTitleLine(width, title)
}

// compactQueueTitleLine renders the compact/wrapped form's title line — an
// indent, then title given the whole remainder of width rather than
// squeezed beside the row's other columns — the piece compactWorkRow and
// compactBacklogRow share (issue #1752 review).
func compactQueueTitleLine(width int, title string) string {
	titleWidth := width - runewidth.StringWidth(compactQueueIndent)
	if titleWidth < 1 {
		titleWidth = 1
	}
	return compactQueueIndent + clip(title, titleWidth, false) + "\n"
}

// compactBacklogRow renders one Backlog issue in the compact form: a "#num
// [labels]" header line carrying the cursor marker, followed by the title —
// clip()ped, not wrapped, just given a whole line of its own rather than
// squeezed beside the number/labels columns — the narrowed-queue alternative
// to renderBacklogSection's classic single-line clip()ped row (issue #1752).
// title is expected pre-sanitized (SanitizeControlSequences), matching the
// classic row's own discipline; labels likewise.
func compactBacklogRow(width int, marker, number, title string, labels []string) string {
	// clip("#"+number, ...), not "#"+clip(number, ...): matching the classic
	// row's own clip("#"+iss.Number, numberColWidth, true) exactly, kept in
	// sync with labelsWidth's own reservation below (issue #1752 review).
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

// truncateWithEllipsis fits s into exactly width display columns by cutting
// it and marking the cut with a trailing "…" (issue #1779), shared by clip
// and padDisplay. runewidth.Truncate(s, width-1, "") can stop one column
// short of width-1 when a wide (2-column) rune straddles that boundary — its
// internal loop bails out before adding a rune that would push the running
// total over budget — so the result is re-measured and padded back to
// exactly width rather than trusted as-is (issue #1785).
func truncateWithEllipsis(s string, width int) string {
	if width <= 1 {
		return runewidth.Truncate(s, width, "")
	}
	cut := runewidth.Truncate(s, width-1, "") + "…"
	return cut + strings.Repeat(" ", width-runewidth.StringWidth(cut))
}

// clip fits s into width display columns (not runes — a wide CJK rune is 2
// columns, issue #859): truncated with a trailing ellipsis if s runs over
// (regardless of pad — the ellipsis case always lands on exactly width),
// space-padded out to width if pad is true and s is shorter, left as-is if
// pad is false and s already fits.
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

// clipLabels fits a label list into width display columns: unlike clip()'s
// ellipsis, an over-width label list drops whole labels from the tail and
// replaces them with a "+N" count of how many were dropped, so an operator
// scanning the Backlog can tell there's more without the label text itself
// getting mangled mid-word (issue #1631).
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

// bannerErrWidth bounds a single-line header error banner (rebuild-failed,
// orphan-adopt-failed) to one row's worth of text. RunNixBuild wraps the
// merged nix stdout+stderr (often many lines) into one error, so printing
// m.RebuildStatus.Err unbounded blew the header banner out to arbitrary length
// (issue #1131); the same bound applies to any other error banner sharing
// the row budget (issue #1218). Fixed rather than tied to m.Width — the
// other header lines are already unbounded strings, and this budget only
// needs to be "one reasonable terminal row," not exact.
const bannerErrWidth = 200

// clipBannerErr collapses an error's embedded newlines (RunNixBuild merges
// multi-line nix output into one error, issue #1131) to single spaces and
// clips the result to width, so a header error banner line stays one row
// regardless of how verbose the underlying error was.
func clipBannerErr(s string, width int) string {
	return clip(strings.Join(strings.Fields(s), " "), width, false)
}

// headerTitle is the Console's fixed wordmark, folded into the header
// panel's top border rule by renderBoxedHeader (issue #1798) rather than
// rendered as a separate interior banner — ADR 0025's original three-line
// "====\n  spindrift\n====" literal is gone; the border row it used to float
// above already exists on every terminal wide/tall enough to box the header
// at all.
const headerTitle = "spindrift"

// renderHeader renders the Console's full-width header: the status line
// (running/cap, waiting, held, settled, failed), and the stale-image,
// rebuilding-in-progress, rebuild-failed, orphan-adopt-failed,
// branch-switch-notice, and competing-dogfood alert lines. The six alerts
// render in that fixed order with no priority or dismissal logic — any
// subset can be true at once, and each renders unconditionally on its own
// line. Status counts are derived from Cap, Live, and the Picks slice's
// PickState tags rather than a new stored counter (issue #843, ADR 0025).
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
	// Live/Cap read zero (`running 0/0`) — unlike the old `cap:` line it
	// replaced, which was introduced by issue #653 (which gated it on
	// Cap > 0) and later removed by issue #843.
	// Session-at-a-glance context is meant to be visible unconditionally,
	// not to disappear when the queue happens to be empty (issue #843 AC5).
	// Each segment is styled by its own semantic role (ADR 0031), so content
	// survives styling as separate substrings rather than one contiguous
	// line (issue #1499).
	fmt.Fprintf(&b, "%s · %s · %s · %s · %s\n",
		roleStyle(RoleRunning).Render(fmt.Sprintf("running %d/%d", m.Live, m.Cap)),
		roleStyle(RoleDim).Render(fmt.Sprintf("waiting %d", waiting)),
		roleStyle(RoleHeld).Render(fmt.Sprintf("held %d", held)),
		roleStyle(RoleSettled).Render(fmt.Sprintf("settled %d", settled)),
		roleStyle(RoleFailed).Render(fmt.Sprintf("failed %d", failed)))
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
		// line's literal last character, with no styling reset appended
		// after it, or TestView_RebuildErr_Truncated's suffix check breaks.
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
	if m.DogfoodLive {
		b.WriteString(roleStyle(RoleDim).Render(glyphNotice + " notice: a live dogfood loop (.dogfood.pid) is competing for the same queue"))
		b.WriteString("\n")
	}
	return b.String()
}

// renderHelp renders the "?" overlay: every key the tea layer binds,
// replacing the backlog/queue rendering entirely while open (issue #784).
// The lines themselves come from keymap (issue #1789) — each Binding with
// non-empty Help contributes its own line(s), in keymap's declared order.
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

// bodyBudget returns the row budget left for the active Section's table
// after the header, Section tabs, and any active prompt/error lines — the
// same figure View computes before calling renderBody (issue #1035, ADR
// 0030). Update reuses it so cursor-follows-viewport (issue #1036) scrolls
// against the exact window View is about to render, rather than a second,
// potentially-diverging calculation.
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
	if m.Mode == ModePick && m.HasHighlighted() {
		reservedLines++
	}
	if m.QueueEnterNotice != "" {
		reservedLines++
	}
	if m.Err != nil {
		reservedLines++
	}
	budget := m.Height - headerLines - reservedLines
	if budget < 0 {
		budget = 0
	}
	if m.Sidebar != nil && sidebarFits(m) && !m.SidebarZoom {
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

// listContentBudget is bodyBudget(m) less listFooterLines whenever ModeList's
// own pinned footer (issue #1792) is about to consume a row — Update's
// cursor/scroll clamp (model.go) and sectionPageSize go through this instead
// of bodyBudget(m) directly, the same way model.go's sidebar clamp
// separately subtracts sidebarDockedFooterLines, so neither ever targets a
// taller page than renderBody's own "-listFooterLines" reservation actually
// leaves room to show (issue #1755's shared-budget invariant, extended to
// the list's footer). Unlike sidebarDockedFooterLines's docked-only
// reservation, this applies whenever m.Mode is ModeList regardless of
// whether a sidebar is docked beside the list — renderBody appends the same
// footer to the list column either way.
func listContentBudget(m Model) int {
	budget := bodyBudget(m)
	if m.Mode == ModeList {
		budget -= listFooterLines
		if budget < 0 {
			budget = 0
		}
	}
	return budget
}

// positionLabel returns a compact " (X-Y of N)" position indicator for a
// column's label, describing the rows vp actually renders at itemBudget of
// total — or "" when there is nothing to show a range for (an empty list, or
// a budget too small to render any row), so a column that renders no rows
// doesn't grow a misleading "(1-0 of 0)" label (issue #1037 AC3). vp is
// passed by value and left untouched by the caller's own copy; its height is
// set directly rather than through SetHeight, matching renderTable's own
// reasoning for skipping SetHeight's clamp-on-shrink here.
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

// sectionPageSize returns the number of rows one page jump (pgup/pgdown)
// moves the active Section's viewport by — the row count actually rendered
// at its current offset, not the raw item budget. A truncated window holds
// one row back for the "N more below" affordance, so paging by the raw
// budget would overshoot by one and skip the row right past the fold; paging
// by what's actually on screen lands exactly on the first row the operator
// hasn't seen yet, and stays correct across a terminal resize instead of a
// value fixed at startup (issue #1037 AC1/AC2, ADR 0030). Unlike the
// sidebar/rebuild-output panes' fixed fixedPaneScrollDelta, this is
// recomputed on every keypress.
func sectionPageSize(m Model) int {
	itemBudget := queueItemBudget(m, listContentBudget(m))
	if itemBudget <= 0 {
		return 0
	}
	total := sectionRowCount(m, m.ActiveSection)
	vp := Viewport{offset: m.Offset, height: itemBudget}
	shown, _ := vp.Window(total).Shown()
	return shown
}

// columnItemBudget converts a Section's row budget (header row included)
// into the row budget available for its item rows alone — the "-1 for the
// header" that renderBacklogSection and renderWorkSection get by calling
// columnItemBudget(budget) directly before passing the result on as a
// Viewport's item height. A non-positive column budget yields zero items,
// matching those functions' own budget<=0-renders-nothing early return.
func columnItemBudget(columnBudget int) int {
	if columnBudget <= 0 {
		return 0
	}
	return columnBudget - 1
}

// queueItemBudget is columnItemBudget's queueNarrowed-aware wrapper: callers
// that hold the full, pre-render Model (unlike renderWorkSection and
// renderBacklogSection, which already narrowed m.Width by the time they run)
// use this instead of columnItemBudget directly, so the cursor-follow
// (model.go) and page-size (sectionPageSize) math never assumes the classic
// one-line-per-item budget while the compact form is what actually renders
// (issue #1752).
func queueItemBudget(m Model, columnBudget int) int {
	if queueNarrowed(m) {
		return compactColumnItemBudget(columnBudget)
	}
	return columnItemBudget(columnBudget)
}

// windowSidebarLines returns s.Lines windowed through a Viewport at s.Offset,
// budget rows deep — so a render joins only what the viewport can show
// instead of the whole tail from Offset to the end of a (potentially
// multi-MB) transcript (issue #722, inherited from the retired
// windowLines/DrillInState). A non-positive budget yields nil rather than
// asking Viewport to represent it (SetHeight(0) means unbounded, not zero
// lines) — Viewport is never asked to window a real, non-positive budget. As
// recorded when this windowing landed against DrillInState, a View call
// against a 10MB+ transcript at Offset 0, Height 24
// (BenchmarkView_DrillInFullscreen_LargeTranscript, issue #1016) went from
// 3.88ms/op, 21.0MB/op, 7 allocs/op — the state right after the Lines cache
// landed but before this windowing, still joining offset-to-end every call,
// itself down from 4.47ms/op, 23.5MB/op, 9 allocs/op pre-cache — to 1.6µs/op,
// 3.39KB/op, 5 allocs/op (windowed). The alloc counts are the invariant;
// absolute ns/op and B/op vary by machine, Go version, and allocator
// behavior. Reproduce with `go test ./internal/console/... -run '^$' -bench
// BenchmarkView_DrillInFullscreen -benchmem` from cmd/launcher.
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
// footer) that renderSidebarFullscreen and Update's tail (via
// Viewport.SetHeight, in the fullscreen/zoomed branch) subtract from
// height — shared so the clamp's last-page cap always matches what
// renderSidebarFullscreen actually has room to show (issue #829, #1002,
// inherited from the retired drill-in pane). renderSidebarDocked no longer
// shares this budget — see sidebarDockedFooterLines.
const headerFooterLines = 2

// sidebarDockedFooterLines is the docked sidebar's own chrome budget
// (keystroke-hint footer only) that renderSidebarDocked and Update's tail
// (in the docked branch) subtract from bodyBudget(m) — narrower than
// headerFooterLines because the docked panel's label folds into its
// border title instead of spending an interior row (issue #1799);
// renderSidebarFullscreen still renders its label as an interior row, so
// it keeps budgeting the full headerFooterLines pair.
const sidebarDockedFooterLines = 1

// listFooterLines is the plain list body's own chrome budget (keystroke-hint
// footer only) that renderBody and bodyBudget/viewBody's reservedLines
// subtract for ModeList — the same one-row reservation
// sidebarDockedFooterLines already models for the docked sidebar's footer,
// applied to the main list view issue #1792 gave a pinned footer of its own.
const listFooterLines = 1

// sidebarErr returns the error the current view should surface: s.Err
// unconditionally (nothing loaded at all, e.g. no Driver), otherwise
// s.TranscriptErr only while ShowTranscript is true — a Transcript-only
// load failure must never blank out an independently-loaded, otherwise-good
// Activity feed (#1501 review finding).
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
// "transcript #N" once toggled to the Transcript, "(raw)" appended while
// ShowRaw — the sidebar analogue of renderDrillIn's transcript-only label,
// extended for the Activity/Transcript toggle (#1501). The Activity feed's
// label also carries a "[follow]"/"[paused]" tag — the operator's only
// render-level signal for whether the feed is live-tailing or detached after
// a scroll-up (issue #1502, ADR 0030); the Transcript is a one-shot load
// with nothing to follow, so the tag is meaningless there.
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
// columns, preserving blank lines (paragraph breaks) verbatim — the detail
// modal body's own plain-text renderer (issue #1632 notes there is no
// glamour renderer in the dependency tree, so this is hand-rolled rather
// than markdown-rendered). A single word wider than width is placed alone
// on its own (overflowing) line rather than broken mid-word.
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

// detailModalChromeLines is the ticket detail modal's fixed, non-scrollable
// row spend: the title line and the labels line (header, always exactly two
// lines — an empty Labels still spends its own blank line, matching an
// empty-label backlog row) plus the keystroke-hint footer. Shared by
// renderDetailModal and Update's own Offset clamp, the same "clamp always
// matches what the render actually has room to show" discipline
// headerFooterLines documents for the sidebar/rebuild-output panes (issue
// #1632).
const detailModalChromeLines = 3

// detailModalLines flattens s's body (word-wrapped to width) and its
// Blocked-by/Blocks sections into one scrollable line list — the content
// renderDetailModal windows through a single Viewport, computed once when
// DetailModalLoadedMsg lands rather than re-wrapped on every keystroke
// (mirrors sidebarLines' #722 caching). A section with nothing to list
// contributes no lines at all, rather than an empty header (issue #1632).
func detailModalLines(width int, s DetailModalState) []string {
	lines := wrapText(SanitizeControlSequences(s.Body), width)
	lines = append(lines, detailModalBlockerLines("Blocked by", s.BlockedBy)...)
	lines = append(lines, detailModalBlockerLines("Blocks", s.Blocks)...)
	return lines
}

// detailModalBlockerLines renders one of the detail modal's Blocked-by/
// Blocks sections as lines: a blank separator, a header naming it, then one
// line per BlockerRef — nil when refs is empty, so a ticket with nothing
// declared in that direction doesn't grow an empty header with nothing
// under it (issue #1632).
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
// s.Offset, budget rows deep — windowSidebarLines' detail-modal analogue
// (issue #1632).
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
// the terminal's own dimensions the floating detail modal box targets before
// the min/max clamps apply — the box scales with the terminal instead of
// shrinking by a fixed margin (issue #1759 AC), the same target-percent-then-
// clamp shape computeSidebarWidth already uses for the docked sidebar.
const (
	detailModalBoxWidthPercent  = 80
	detailModalBoxHeightPercent = 80
)

// detailModalBoxMinWidth and detailModalBoxMinHeight floor the floating box
// at a size where the border plus a line or two of body interior stays
// legible (issue #1759 AC) — detailModalFits gates the floating layout on
// the terminal itself being at least this large, so the clamp here never has
// to inflate the box past the terminal's own size.
const (
	detailModalBoxMinWidth  = 40
	detailModalBoxMinHeight = 10
)

// detailModalBoxMaxWidth and detailModalBoxMaxHeight cap the floating box at
// a comfortable reading size on a wide/tall terminal instead of stretching
// it corner to corner — "roughly centered at a sensible default size" (issue
// #1758 AC; width widened 84 -> 100 by issue #1796).
const (
	detailModalBoxMaxWidth  = 100
	detailModalBoxMaxHeight = 30
)

// detailModalFits reports whether m.Width and m.Height leave room for a
// floating detail modal box at least detailModalBoxMin{Width,Height} — the
// gate View and the modal's own width/height-dependent routing (the Lines
// wrap width, the scroll budget) both check before choosing the floating
// box over the small-terminal fullscreen fallback, so the two can never
// disagree about which one is showing (sidebarFits' detail-modal analogue,
// issue #1759).
func detailModalFits(m Model) bool {
	return m.Width >= detailModalBoxMinWidth && m.Height >= detailModalBoxMinHeight
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
// the fullscreen renderer's own detailModalChromeLines-based budget
// otherwise — detailModalWrapWidth's height analogue, gated by the same
// predicate (issue #1759). Uses detailModalLabelLinesCapped, not the bare
// detailModalLabelLines, so a ticket with enough labels to fill the box's
// whole content budget clamps against the same "+N more labels" row count
// renderDetailModalContent actually renders, not the uncapped wrap it never
// shows (issue #1778).
func detailModalScrollBudget(m Model) int {
	if !detailModalFits(m) {
		return m.Height - detailModalChromeLines
	}
	innerWidth, innerHeight := detailModalInnerSize(m.Width, m.Height)
	labelLines := detailModalLabelLinesCapped(m.DetailModal.Labels, innerWidth, innerHeight-detailModalFooterLines)
	return innerHeight - len(labelLines) - detailModalFooterLines
}

// detailModalBoxSize returns the floating detail modal box's outer width and
// height for a termWidth x termHeight terminal: detailModalBox{Width,Height}
// Percent of the terminal's own dimensions, clamped down to
// detailModalBoxMin{Width,Height} and up to detailModalBoxMax{Width,Height}
// (issue #1759 AC). Only meaningful when detailModalFits(m) is true — below
// that threshold the min clamp would inflate the box past the terminal's own
// size, which callers on the fullscreen fallback path never observe.
func detailModalBoxSize(termWidth, termHeight int) (width, height int) {
	width = termWidth * detailModalBoxWidthPercent / 100
	if width < detailModalBoxMinWidth {
		width = detailModalBoxMinWidth
	}
	if width > detailModalBoxMaxWidth {
		width = detailModalBoxMaxWidth
	}
	height = termHeight * detailModalBoxHeightPercent / 100
	if height < detailModalBoxMinHeight {
		height = detailModalBoxMinHeight
	}
	if height > detailModalBoxMaxHeight {
		height = detailModalBoxMaxHeight
	}
	return width, height
}

// detailModalBoxOrigin centers a boxWidth x boxHeight box within a
// termWidth x termHeight terminal, the (x, y) compositeOverlay places it at.
func detailModalBoxOrigin(termWidth, termHeight, boxWidth, boxHeight int) (x, y int) {
	return (termWidth - boxWidth) / 2, (termHeight - boxHeight) / 2
}

// detailModalInnerSize returns the floating detail modal box's interior
// width/height for a termWidth x termHeight terminal — the box outer size
// (detailModalBoxSize) minus the one-column/one-row border on every side.
// This is what the width-dependent modal machinery (the Lines word-wrap, the
// scroll budget) must key off instead of Model.Width/Model.Height (issue
// #1758), so a resize and the box's own render always agree on how wide the
// body was actually wrapped.
func detailModalInnerSize(termWidth, termHeight int) (width, height int) {
	boxWidth, boxHeight := detailModalBoxSize(termWidth, termHeight)
	width, height = boxWidth-2, boxHeight-2
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	return width, height
}

// padBaseForOverlay pads every line of s out to at least width display
// columns and appends blank width-wide lines until s has at least height
// lines. compositeLine only composites onto a base row whose display width
// already reaches the box's x origin — it leaves a too-short row untouched
// instead — and compositeOverlay only overwrites rows base already has. But
// viewBody's rendered rows stop at whatever content they actually have
// (renderBody doesn't pad a short list out to the row budget), so a base
// built for its own natural size must be padded to the terminal's full frame
// before a box lower on screen, or wider than a short row, can land on it
// (issue #1758).
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
// exactly its inner width, or the side border runes drift out of column
// with the rest of the box (issue #1758). An overflowing s is truncated with
// a trailing ellipsis, mirroring clip, so the cut is visible rather than
// silent (issue #1779). Measured with ansi.StringWidth rather than
// runewidth.StringWidth — the same swap padBaseForOverlay's own measurement
// already made — so a caller may hand it an already-styled row (the detail
// modal box's own dim-styled footer, issue #1791) without its ANSI escape
// bytes being miscounted as display columns — the same clip-before-style
// hazard sectionTabsHint's own comment documents. The truncate branch stays
// runewidth-based and is only ever safe to reach with plain content clipped
// to width beforehand, same as every other caller through clip() today.
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
// own body budget and Update's Offset clamp so the two never drift apart
// on how many rows the footer costs (issue #1772 review finding).
const detailModalFooterLines = 1

// detailModalLabelLines word-wraps a ticket's labels, joined as a single
// comma-separated string, to width display columns — shared by
// renderDetailModalContent (issue #1772: a labels line wider than the
// floating box's interior must wrap onto further interior rows instead of
// padDisplay truncating it mid-word) and Update's own Offset clamp, which
// must agree on how many interior rows the labels spend before it can
// budget the rest to the scrollable body.
func detailModalLabelLines(labels []string, width int) []string {
	sanitized := make([]string, len(labels))
	for i, l := range labels {
		sanitized[i] = SanitizeControlSequences(l)
	}
	return wrapText(strings.Join(sanitized, ", "), width)
}

// detailModalLabelLinesCapped wraps labels the same as detailModalLabelLines,
// but caps the result at maxLines: when the wrapped labels alone would
// exceed maxLines, it drops labels from the tail and replaces them with a
// "+N more labels" row, the multi-row analogue of the backlog row's
// clipLabels "+N" convention (issue #1631) — so a ticket with enough labels
// to fill the floating box's entire interior loses its footer/body budget to
// a visible indicator instead of renderDetailModalContent's tail-truncate
// silently dropping trailing label lines and/or the footer row (issue #1778,
// a gap left by #1772/#1780's wrap-instead-of-truncate fix). maxLines <= 0
// yields the bare "+N more labels" indicator alone.
func detailModalLabelLinesCapped(labels []string, width, maxLines int) []string {
	lines := detailModalLabelLines(labels, width)
	if len(labels) == 0 || len(lines) <= maxLines {
		return lines
	}
	for k := len(labels) - 1; k >= 0; k-- {
		trial := detailModalLabelLines(labels[:k], width)
		if len(trial)+1 <= maxLines {
			return append(trial, fmt.Sprintf("+%d more labels", len(labels)-k))
		}
	}
	return []string{fmt.Sprintf("+%d more labels", len(labels))}
}

// renderDetailModalContent renders the floating detail modal box's interior
// — the labels line, the loading/error/body-window content, and the
// scroll/close footer hint — as exactly innerHeight lines, word-wrapped and
// scrolled against innerWidth/innerHeight (the box interior, not the
// terminal, per issue #1758's width-dependent-machinery AC): the split half
// of the old renderDetailModal that stays width/height-parameterized rather
// than reading Model.Width/Model.Height directly.
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
	// appended below — so the footer (the one line contentBudget already set
	// aside for it) is never among the lines a too-long labels/loading/error
	// block pushes past the end (issue #1778). In the degenerate case where
	// labels alone already consume all of contentBudget, this is what makes
	// the loading/error line itself the one dropped here — the labels'
	// visible "+N more labels" indicator takes budget precedence over the
	// one-line status text, never the reverse.
	if len(lines) > contentBudget {
		lines = lines[:contentBudget]
	}
	lines = append(lines, renderFooterHints(ModeDetailModal, []string{"j", "esc"}, 0, false))
	for len(lines) < innerHeight {
		lines = append(lines, "")
	}
	if len(lines) > innerHeight {
		lines = lines[:innerHeight]
	}
	return lines
}

// renderDetailModalBox renders s as a bordered floating box exactly
// width x height display cells: the "#number title" set in the top border
// (AC1), the interior content renderDetailModalContent produces windowed to
// the box's interior, and every row padded to width so compositeOverlay
// fully occludes whatever list content sits behind it (issue #1758). Boxed
// via the shared renderBoxedColumn/renderTitledTopBorder helper rather than
// hand-rolled Unicode runes, so the border — titled top rule included — now
// degrades to ASCII under NO_COLOR/a dumb terminal like every other panel in
// the package (issue #1797).
func renderDetailModalBox(s DetailModalState, width, height int) string {
	if width < 4 || height < 3 {
		return ""
	}
	innerWidth := width - 2
	innerHeight := height - 2
	title := fmt.Sprintf("#%s %s", SanitizeControlSequences(s.Number), SanitizeControlSequences(s.Title))

	lines := renderDetailModalContent(s, innerWidth, innerHeight)
	// Each content line must be clipped to exactly innerWidth before it
	// reaches renderBoxedColumn: lipgloss's Width() only ever pads a line up
	// to width, never truncates one down, so an over-wide line (e.g. a
	// label wrapText left unbroken) would otherwise widen the whole box
	// instead of getting cut with an ellipsis (issue #1779/#1785's rule,
	// carried over from this function's old inline padDisplay call).
	for i, l := range lines {
		lines[i] = padDisplay(l, innerWidth)
	}
	return renderBoxedColumn(strings.Join(lines, "\n"), innerWidth, title, RoleDim)
}

// renderDetailModal renders a Backlog issue's fullscreen ticket detail
// modal: its number/title, its labels, and — once the async fetch lands — a
// word-wrapped plain-text body plus Blocked-by/Blocks sections, scrolled
// together through one Viewport (issue #1632). It opens the instant Enter
// fires, before that fetch resolves, so a "loading..." placeholder stands in
// for the body/blocker content until DetailModalLoadedMsg fills it in. View
// no longer calls this directly (issue #1758 floats renderDetailModalBox
// over the list instead) — kept callable for the small-terminal fallback
// ticket that AC promises will reuse it.
func renderDetailModal(s DetailModalState, height int) string {
	if height <= 0 {
		return ""
	}
	labels := make([]string, len(s.Labels))
	for i, l := range s.Labels {
		labels[i] = SanitizeControlSequences(l)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "#%s %s\n", s.Number, SanitizeControlSequences(s.Title))
	b.WriteString(strings.Join(labels, ", "))
	b.WriteString("\n")
	switch {
	case s.Loading:
		b.WriteString("loading...\n")
	case s.Err != nil:
		fmt.Fprintf(&b, "failed to load: %s\n", SanitizeControlSequences(s.Err.Error()))
	default:
		visible := strings.Join(windowDetailModalLines(s, height-detailModalChromeLines), "\n")
		b.WriteString(visible)
		if visible != "" && !strings.HasSuffix(visible, "\n") {
			b.WriteString("\n")
		}
	}
	fmt.Fprintf(&b, "%s\n", renderFooterHints(ModeDetailModal, []string{"j", "esc"}, 0, false))
	return b.String()
}

// blockerOpenGlyph and blockerClosedGlyph mark a BlockerRef's open/closed
// state at a glance, ahead of the spelled-out state word — the issue #1632
// example format ("✗ #1540 (native) open \"Waves core\"").
const (
	blockerOpenGlyph   = "✗"
	blockerClosedGlyph = "✓"
)

// formatBlockerRef renders one Blocked-by/Blocks entry: an open/closed
// glyph, the issue number, its dependency source (native vs body-parsed),
// its open/closed state spelled out, and its title — e.g.
// `✗ #1540 (native) open "Waves core"` (issue #1632 AC). Static text only,
// no drill-down navigation into the referenced issue's own detail this
// round.
func formatBlockerRef(r BlockerRef) string {
	glyph := blockerOpenGlyph
	if r.State == forge.IssueClosed || r.State == forge.IssueMerged {
		glyph = blockerClosedGlyph
	}
	state := strings.ToLower(string(r.State))
	if state == "" {
		// resolveBlockerRef's failure fallback (the ref was deleted, or its
		// own Issue fetch erred) leaves State/Title blank — render "unknown"
		// in both rather than a bare double space and an empty quoted
		// string (issue #1632 review finding).
		state = "unknown"
	}
	title := SanitizeControlSequences(r.Title)
	if title == "" {
		title = "unknown"
	}
	// forge.Ref centralizes the "#N (source)" annotation every other
	// blocker-diagnostic call site already shares — reusing it here keeps
	// this format from drifting out of sync with theirs (issue #1632
	// review finding).
	return fmt.Sprintf("%s %s %s %q", glyph, forge.Ref(r.Number, r.Source), state, title)
}

// renderSidebarFullscreen renders one Dispatch's live-tail sidebar full
// terminal width and height: the narrow-terminal fallback View reaches for
// when sidebarFits is false, and the shape the retired drill-in pane always
// rendered at before #1501 introduced the docked layout. A header naming the
// pick and current view, as much of the loaded content (the Activity feed by
// default, the Transcript once toggled) as height allows, and a keystroke
// hint. Err renders in place of content instead of a blank pane.
//
// The label, footer, and Err line are themselves budgeted against height
// (issue #1534, mirroring #1380's renderTranscriptColumn fix): at height 1,
// only the label renders and the footer or Err line is dropped, whichever
// would come next.
func renderSidebarFullscreen(s SidebarState, height int) string {
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

	visible := strings.Join(windowSidebarLines(s, height-headerFooterLines), "\n")
	b.WriteString(visible)
	if visible != "" && !strings.HasSuffix(visible, "\n") {
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "%s\n", renderFooterHints(ModeSidebar, []string{"t", "x", "z"}, 0, false))
	return b.String()
}

// renderFooterHints renders one mode's pinned keystroke-hint line: each
// key's Footer text from keymap (issue #1789), joined and dim-styled — the
// zoomed sidebar's own "·"-separated pinned-bottom-line shape, generalized
// (and newly dim-styled — none of the four bespoke footers below styled
// their hint line before) so they share one source instead of each
// hand-building its own hint string (issue #1791). compact switches to
// footerHintCompact and the docked sidebar's
// tighter "·" separator (view.go's renderSidebarDocked, the one footer
// tight enough to need it); width clips the joined line before styling —
// never after, or the naive runewidth-based clip() would miscount the
// styling's own ANSI escape bytes as display columns (the same hazard
// sectionTabsHint's clip-before-style comment already documents) — and 0
// or negative leaves it unclipped, matching every fullscreen caller's own
// unbounded footer today.
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
// match renderTable's own row-budget contract so the two columns' row
// counts agree before lipgloss.JoinHorizontal pads whichever one falls
// short. The label itself is not rendered here — the caller folds it into
// the panel's top border via renderBoxedColumn's title instead of an
// interior row (issue #1799), so budget only reserves
// sidebarDockedFooterLines for the keystroke-hint footer, not the
// label+footer pair renderSidebarFullscreen's own headerFooterLines still
// budgets — the old budget<=1 "just the label, nothing else fits" early
// return is gone with it: a budget of 1 now renders the footer alone,
// since the border title shows regardless of how little interior room is
// left.
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
	// Deliberately tighter than the fullscreen footer's " · " spacing (and
	// the rest of the module's own convention, e.g. renderHeader's segment
	// joins): four hints plus full " · " separators measure 43 columns,
	// one over sidebarWidth's 42-column budget, so the space after each
	// "·" is dropped to fit all four without clipping the last one.
	b.WriteString(renderFooterHints(ModeSidebar, []string{"t", "h", "x", "z"}, width, true))
	b.WriteString("\n")
	return b.String()
}

// renderRebuildOutputPane renders the last rebuild's captured nix output
// full-screen, from RebuildOutputOffset onward, plus a close-key hint —
// RebuildOutput's only consumer (issue #1128). Unlike the drill-in pane, it
// has no docked/floating mode: the output is a flat log, not a Dispatch's
// Transcript worth keeping alongside the backlog/queue.
func renderRebuildOutputPane(m Model) string {
	var b strings.Builder
	b.WriteString("rebuild output:\n")

	budget := m.Height - headerFooterLines
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
