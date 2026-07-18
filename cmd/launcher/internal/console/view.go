package console

import (
	"fmt"
	"strings"

	"github.com/mattn/go-runewidth"
)

// View renders m as the text the run loop writes to the terminal: the
// full-width header (banner, status line, stale/dogfood alerts), the
// two-column body (queueable backlog on the left, state-tagged work queue on
// the right — issue #844, ADR 0025), and any refresh error. An open drill-in
// (m.DrillIn != nil) renders the Transcript per its effective pane mode
// (issue #846, ADR 0025): docked (a third column beside the still-visible
// backlog/queue), floating (an overlay atop the two-column body), or
// fullscreen (replacing the backlog/queue rendering entirely) — the terminal
// falls back to fullscreen regardless of the stored mode when it's too
// narrow for three columns.
func View(m Model) string {
	if m.DrillIn != nil {
		return renderDrillInPane(m)
	}
	if m.ShowRebuildOutput {
		return renderRebuildOutputPane(m)
	}
	if m.ShowHelp {
		return renderHelp()
	}

	var b strings.Builder
	header := renderHeader(m)
	b.WriteString(header)
	reservedLines := 0
	if m.FilterEditing {
		fmt.Fprintf(&b, "/%s  [enter] apply · [esc] cancel\n", m.Filter)
		reservedLines++
	}
	if m.PendingTerminate != "" {
		fmt.Fprintf(&b, "terminate #%s? [y/N/q/ctrl+c]\n", m.PendingTerminate)
		reservedLines++
	}
	if m.PendingQuit {
		b.WriteString("quit with live Dispatches: drain (d, default) / terminate-all (t) / stay (s)?\n")
		reservedLines++
	}
	if m.PendingPick && m.HasHighlighted() {
		b.WriteString("p_\n")
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
	budget := m.Height - strings.Count(header, "\n") - reservedLines
	if budget < 0 {
		budget = 0
	}
	b.WriteString(renderBody(m, &budget))
	if m.Err != nil {
		fmt.Fprintf(&b, "refresh failed: %s\n", m.Err)
	}
	return b.String()
}

// minTwoColumnWidth is the terminal width below which the body stacks the
// backlog above the work queue instead of splitting them side by side —
// below it there isn't room for two readable columns, so splitting would
// wrap rows into an unreadable mess instead of degrading gracefully (issue
// #844, ADR 0025).
const minTwoColumnWidth = 60

// leftColumnFraction caps the backlog column at two fifths of m.Width — the
// work queue (state tag, blocker, heartbeat) tends to carry more text per
// row than the backlog, so it gets the larger share of a wide terminal.
const leftColumnFraction = 2.0 / 5.0

// splitStackedBudget splits budget between the stacked backlog and queue
// columns: one row goes to the blank separator between them (the "\n"
// joining the two stacked blocks in renderBody is itself a row, budgeted
// like any other body row, or the stack's true height runs one over the
// columns' own totals), and the rest splits as evenly as possible. Shared
// by renderBody and bodyColumnBudgets so their stacked-mode split can never
// diverge (issue #1052).
func splitStackedBudget(budget int) (backlog, queue int) {
	contentBudget := budget - 1
	if contentBudget < 0 {
		contentBudget = 0
	}
	half := contentBudget / 2
	return half, contentBudget - half
}

// renderBody renders the backlog and work-queue columns side by side,
// sized from m.Width — the two-column body under the header (issue #844,
// ADR 0025). Backlog keeps its label filter and cursor; the queue lists
// m.Picks in pick order, each row tagged with its PickState. The left
// column's width tracks the backlog's own longest line, capped at
// leftColumnFraction of m.Width; both columns clip any line that still
// overflows its share, so a joined row never exceeds m.Width regardless of
// how long a title, label list, or blocker badge runs (issue #844 AC6).
// Below minTwoColumnWidth the columns stack instead of splitting. budget is
// the row count left after the header and any prompt lines — both columns
// window their rows to it so neither can push the header off-screen (issue
// #1035). Side by side, each column gets the full budget since they share
// output lines; stacked, they'd each independently fit within budget but
// together overflow it, so the stacked case splits budget between them
// instead. A nil budget means unbounded — no windowing at all — for the
// docked and floating drill-in panes (issue #846, ADR 0025), which predate
// per-render body windowing and keep their existing unwindowed behavior
// (issue #1035 is scoped to the plain two-column body only; issue #1039
// replaced an earlier magic-constant sentinel with this nil semantics).
func renderBody(m Model, budget *int) string {
	if budget != nil && *budget <= 0 {
		return ""
	}
	if m.Width < minTwoColumnWidth {
		// At m.Width==0 — the Model zero value NewModel returns, before
		// Update ever processes a SizeChangedMsg and runs clampSize (model.go)
		// — clipLines below clips every stacked line to empty via clip's
		// width<=1 branch, so this pre-init render is all blank lines rather
		// than unclipped content. Benign: bubbletea sends a real size message
		// immediately, so it's a single-frame flash at worst.
		if budget == nil {
			backlog := clipLines(renderBacklogColumn(m, nil), m.Width)
			queue := clipLines(renderQueueColumn(m, nil, m.Width), m.Width)
			return backlog + "\n" + queue
		}
		backlogBudget, queueBudget := splitStackedBudget(*budget)
		if backlogBudget == 0 && queueBudget == 0 && (len(m.Visible()) > 0 || len(m.Picks) > 0) {
			// A budget this tight leaves no row for either column, so
			// renderBacklogColumn/renderQueueColumn would both early-return
			// "" and the stack's "\n" separator alone would render as a
			// bare blank line — indistinguishable from an actually empty
			// backlog and queue. Show a single elision marker instead so
			// the operator knows content exists but doesn't fit (issue
			// #1041) — only when there's something to elide, since a
			// genuinely empty backlog/queue has nothing hidden to flag.
			// Clipped like the other stacked rows so a pathologically
			// narrow terminal (m.Width==0) can't overflow it either.
			return clipLines("…\n", m.Width)
		}
		backlog := clipLines(renderBacklogColumn(m, &backlogBudget), m.Width)
		queue := clipLines(renderQueueColumn(m, &queueBudget, m.Width), m.Width)
		return backlog + "\n" + queue
	}
	backlog := renderBacklogColumn(m, budget)
	leftWidth := splitLeftWidth(backlog, m.Width)
	rightWidth := m.Width - leftWidth
	queue := renderQueueColumn(m, budget, rightWidth)
	return joinColumns(backlog, queue, leftWidth, rightWidth)
}

// clipLines clips each of s's lines to width, unpadded (issue #860) — the
// stacked body's counterpart to joinColumns' per-line clip(), needed because
// the stacked path has no column to pad against. A trailing newline on s is
// preserved so the caller can keep joining blocks with "\n" as before.
func clipLines(s string, width int) string {
	trailingNewline := strings.HasSuffix(s, "\n")
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = clip(l, width, false)
	}
	out := strings.Join(lines, "\n")
	if trailingNewline {
		out += "\n"
	}
	return out
}

// maxLineWidth returns the rune length of s's longest line, ignoring a
// trailing newline.
func maxLineWidth(s string) int {
	width := 0
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if n := len([]rune(l)); n > width {
			width = n
		}
	}
	return width
}

// splitLeftWidth returns the backlog column's width for a two-column body of
// the given effective width: backlog's own longest line, capped at
// leftColumnFraction of width — the split renderBody and renderDockedBody
// both need, factored out so the two layouts can't drift out of sync (issue
// #1001).
func splitLeftWidth(backlog string, width int) int {
	leftWidth := maxLineWidth(backlog)
	if maxLeft := int(float64(width) * leftColumnFraction); leftWidth > maxLeft {
		leftWidth = maxLeft
	}
	return leftWidth
}

// renderBacklogColumn renders the queueable backlog: one line per visible
// issue (number, title, labels), with the cursor row marked, under its own
// column label — windowed to budget rows (including the label) so a long
// backlog can't push the header off-screen; a truncated list ends with a
// lightweight "N more below" affordance instead of just stopping. The label
// is itself budgeted, not a floor on top of it — a non-positive budget
// renders nothing at all, so an extremely short terminal can't have the
// label alone push the header off-screen (issue #1035). A nil budget means
// unbounded — every row renders, unwindowed (issue #1039).
func renderBacklogColumn(m Model, budget *int) string {
	if budget != nil && *budget <= 0 {
		return ""
	}
	label := "backlog"
	if m.Focus == FocusBacklog {
		label += " [focus]"
	}
	total := len(m.Visible())
	label += positionLabel(m.BacklogOffset, budget, total)
	rows := make([]string, 0, total)
	for i, iss := range m.Visible() {
		marker := " "
		if m.Focus == FocusBacklog && i == m.Cursor {
			marker = ">"
		}
		title := SanitizeControlSequences(iss.Title)
		labels := make([]string, len(iss.Labels))
		for j, l := range iss.Labels {
			labels[j] = SanitizeControlSequences(l)
		}
		rows = append(rows, fmt.Sprintf("%s #%s  %s  [%s]\n", marker, iss.Number, title, strings.Join(labels, ", ")))
	}
	if budget == nil {
		var b strings.Builder
		fmt.Fprintf(&b, "%s:\n", label)
		writeWindowedRows(&b, rows, m.BacklogOffset, nil)
		return b.String()
	}
	return writeColumn(label, rows, m.BacklogOffset, *budget)
}

// renderQueueColumn renders the work queue: one pick-ordered line per Pick,
// tagged with its PickState — a held row names its blocker, a running row
// carries its heartbeat. Title normally sits right after the state tag —
// the natural reading order, title first as the operator's primary
// identifier (issue #1256). Only when that natural-order row would actually
// exceed rightWidth does the row fall back to #858's blocker-first order:
// BlockedBy/Reason/Heartbeat before Title, so joinColumns' tail-clip drops
// Title rather than the blocker signal an operator needs for pick/unpick
// decisions. rightWidth is the queue column's real character-width budget,
// measured with the same runewidth.StringWidth primitive clip() uses (issue
// #859), so a row of wide CJK runes doesn't take the natural order past its
// share of the terminal by an ASCII rune count that reads shorter than it
// renders. Windowed to budget rows (including the label) the same way
// renderBacklogColumn is, so a long picks queue can't push the header
// off-screen either (issue #1035). A nil budget means unbounded — every row
// renders, unwindowed (issue #1039).
func renderQueueColumn(m Model, budget *int, rightWidth int) string {
	if budget != nil && *budget <= 0 {
		return ""
	}
	label := "picks"
	if m.Focus == FocusQueue {
		label += " [focus]"
	}
	total := len(m.Picks)
	label += positionLabel(m.QueueOffset, budget, total)
	rows := make([]string, 0, total)
	for i, p := range m.Picks {
		marker := " "
		if m.Focus == FocusQueue && i == m.QueueCursor {
			marker = ">"
		}
		title := SanitizeControlSequences(p.Title)
		reason := SanitizeControlSequences(p.Reason)
		// A held pick's Reason (blockerFailedPrefix + "#N failed") names the
		// same blocker BlockedBy already does — skip it so a failed blocker
		// isn't named twice on one row (issue #755).
		showReason := reason != "" && !(p.BlockedBy != "" && strings.HasPrefix(reason, blockerFailedPrefix))

		lead := fmt.Sprintf("%s #%s  [%s]", marker, p.Number, p.State)
		var extras strings.Builder
		if p.BlockedBy != "" {
			fmt.Fprintf(&extras, "  (held by %s)", p.BlockedBy)
		}
		if showReason {
			fmt.Fprintf(&extras, "  (%s)", reason)
		}
		if p.Heartbeat != "" {
			fmt.Fprintf(&extras, "  %s", p.Heartbeat)
		}

		natural := fmt.Sprintf("%s  %s%s", lead, title, extras.String())
		row := natural
		if runewidth.StringWidth(natural) > rightWidth {
			row = fmt.Sprintf("%s%s  %s", lead, extras.String(), title)
		}
		rows = append(rows, row+"\n")
	}
	if budget == nil {
		var b strings.Builder
		fmt.Fprintf(&b, "%s:\n", label)
		writeWindowedRows(&b, rows, m.QueueOffset, nil)
		return b.String()
	}
	return writeColumn(label, rows, m.QueueOffset, *budget)
}

// joinColumns zips left and right line by line, clipping each side to its
// column width — left is padded out to leftWidth so the right column lines
// up in a consistent gutter, right is truncated if it overflows rightWidth —
// so a joined row never exceeds leftWidth+rightWidth regardless of how long
// either side's content runs (issue #844 AC6). The joined line is then
// right-trimmed, since a backlog-only row (right empty) would otherwise
// carry the left column's padding as trailing whitespace (issue #861).
func joinColumns(left, right string, leftWidth, rightWidth int) string {
	leftLines := strings.Split(strings.TrimRight(left, "\n"), "\n")
	rightLines := strings.Split(strings.TrimRight(right, "\n"), "\n")
	n := len(leftLines)
	if len(rightLines) > n {
		n = len(rightLines)
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		var l, r string
		if i < len(leftLines) {
			l = leftLines[i]
		}
		if i < len(rightLines) {
			r = rightLines[i]
		}
		fmt.Fprintf(&b, "%s\n", strings.TrimRight(clip(l, leftWidth, true)+clip(r, rightWidth, false), " "))
	}
	return b.String()
}

// clip fits s into width display columns (not runes — a wide CJK rune is 2
// columns, issue #859): truncated with a trailing ellipsis if s runs over,
// space-padded out to width if pad is true and s is shorter, left as-is if
// pad is false and s already fits.
func clip(s string, width int, pad bool) string {
	w := runewidth.StringWidth(s)
	switch {
	case w > width:
		if width <= 1 {
			return runewidth.Truncate(s, width, "")
		}
		return runewidth.Truncate(s, width-1, "") + "…"
	case pad:
		return s + strings.Repeat(" ", width-w)
	default:
		return s
	}
}

// bannerErrWidth bounds a single-line header error banner (rebuild-failed,
// orphan-recovery-failed) to one row's worth of text. RunNixBuild wraps the
// merged nix stdout+stderr (often many lines) into one error, so printing
// m.RebuildErr unbounded blew the header banner out to arbitrary length
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

// banner is the Console's fixed wordmark, printed at the top of the header
// whenever the terminal has room for it (issue #843, ADR 0025). It is
// hardcoded rather than figlet-rendered — the module carries no figlet
// dependency, and the art never varies.
const banner = `
========================================
  spindrift
========================================
`

// bannerHeight is the banner's rendered row count — the three lines left
// after renderHeader's TrimPrefix strips the leading blank line above. It is
// not the number of newlines in the raw banner literal (that count is one
// higher, for the blank line).
var bannerHeight = strings.Count(strings.TrimPrefix(banner, "\n"), "\n")

// bannerCollapseMargin is one extra row of headroom required, on top of
// bannerHeight, before the header shows the banner — so the collapse never
// leaves the banner crowding the backlog/queue against the terminal's last
// line on a borderline-tall terminal.
const bannerCollapseMargin = 1

// renderHeader renders the Console's full-width header: the fixed banner
// (when the terminal is tall enough to afford it), the status line
// (running/cap, waiting, held, settled, failed), and the stale-image,
// rebuilding-in-progress, rebuild-failed, orphan-recovery-failed,
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
	if m.Height >= bannerHeight+bannerCollapseMargin {
		b.WriteString(strings.TrimPrefix(banner, "\n"))
	}
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
	if m.Stale {
		b.WriteString(roleStyle(RoleHeld).Render(fmt.Sprintf("%s image stale: %s — new launches held; press [b] to rebuild", glyphWarning, m.StaleMessage)))
		b.WriteString("\n")
	}
	if m.Rebuilding {
		b.WriteString(roleStyle(RoleRunning).Render(glyphRebuilding + " rebuilding image..."))
		b.WriteString("\n")
	}
	if m.RebuildErr != "" {
		fmt.Fprintf(&b, "%s %s\n",
			roleStyle(RoleFailed).Render(glyphWarning+" rebuild failed:"),
			clipBannerErr(m.RebuildErr, bannerErrWidth))
	}
	if m.OrphanRecoveryErr != "" {
		fmt.Fprintf(&b, "!! orphan recovery failed: %s\n", clipBannerErr(m.OrphanRecoveryErr, bannerErrWidth))
	}
	if m.BranchSwitchNotice != "" {
		fmt.Fprintf(&b, "notice: %s\n", m.BranchSwitchNotice)
	}
	if m.DogfoodLive {
		b.WriteString("notice: a live dogfood loop (.dogfood.pid) is competing for the same queue\n")
	}
	return b.String()
}

// renderHelp renders the "?" overlay: every key the tea layer binds,
// replacing the backlog/queue rendering entirely while open (issue #784).
func renderHelp() string {
	return strings.Join([]string{
		"help",
		"  j / down    move cursor down",
		"  i / up      move cursor up",
		"  pgup/pgdown  jump a full page of the backlog/queue's live rendered",
		"              rows without moving the cursor (whichever column has",
		"              focus); the page size tracks terminal resizes",
		"  tab         switch focus between the backlog and work-queue columns",
		"  /           filter by label substring",
		"  enter       apply filter (while filter-editing); otherwise: pick the",
		"              highlighted backlog row (backlog focus), or drill into the",
		"              highlighted pick's transcript (queue focus, only when it has",
		"              one)",
		"  esc         cancel filter edit",
		"  t           toggle rendered <-> raw JSONL (while drilled in)",
		"  x / esc     close the transcript pane (while drilled in)",
		"  j/k, pgup/pgdown  scroll the transcript (while drilled in); its",
		fmt.Sprintf("              pgup/pgdown page jump is fixed at %d lines, unlike the", drillInPageScrollDelta),
		"              backlog/queue's live-viewport-derived one above",
		"  m           cycle the transcript pane docked/floating/fullscreen",
		"              (while drilled in)",
		"  r           refresh the backlog",
		"  p           pick the highlighted issue (launch button)",
		"  u           unpick the highlighted queued pick",
		"  pa          pick all ready (bulk pick-all-ready gesture)",
		"  k           terminate the highlighted live Dispatch (confirm y/N,",
		"              q/ctrl+c decline and quit)",
		"  +           raise the live parallelism cap",
		"  -           lower the live parallelism cap",
		"  b           rebuild the stale image in-session",
		"  o           open the rebuild output pane (once a rebuild has run);",
		"              j/k, pgup/pgdown scroll it, x/esc closes",
		"  q / ctrl+c  quit",
		"  ?           toggle this help",
		"",
	}, "\n")
}

// minThreeColumnWidth is the terminal width below which a docked or floating
// Transcript pane can't fit alongside the two-column body — effectivePaneMode
// falls back to PaneFullscreen regardless of the operator's selected mode
// (issue #846, ADR 0025 AC4).
const minThreeColumnWidth = 90

// transcriptColumnFraction is the docked Transcript column's share of the
// terminal width — smaller than either body column, since docking exists to
// keep the queue visible while reading, not to dominate the layout (issue
// #846, ADR 0025).
const transcriptColumnFraction = 2.0 / 5.0

// effectivePaneMode derives the pane mode View actually renders with: m's
// stored PaneMode, unless the terminal is too narrow for three columns, in
// which case it's PaneFullscreen regardless of the stored value — a pure,
// per-render derivation, never mutating Model (issue #846, ADR 0025 AC4).
func effectivePaneMode(m Model) TranscriptPaneMode {
	if m.Width < minThreeColumnWidth {
		return PaneFullscreen
	}
	return m.PaneMode
}

// transcriptHeight returns the height budget available to the drill-in
// Transcript column/pane for m's effective pane mode — m.Height directly in
// fullscreen (renderDrillIn has no outer header sharing the screen with it),
// or m.Height minus the outer renderHeader(m) line count in docked/floating,
// where renderDrillInPane prepends that header on top of the column. Shared
// with clampDrillInOffset so a scroll's clamped Offset always matches what
// the render path actually has room to show (issue #1014).
func transcriptHeight(m Model) int {
	if effectivePaneMode(m) == PaneFullscreen {
		return m.Height
	}
	return m.Height - strings.Count(renderHeader(m), "\n")
}

// renderDrillInPane renders the open DrillIn per its effective pane mode —
// docked, floating, or fullscreen (issue #846, ADR 0025).
func renderDrillInPane(m Model) string {
	switch effectivePaneMode(m) {
	case PaneDocked:
		header := renderHeader(m)
		return header + renderDockedBody(m, m.Height-strings.Count(header, "\n"))
	case PaneFloating:
		header := renderHeader(m)
		return header + renderFloatingBody(m, m.Height-strings.Count(header, "\n"))
	default:
		return renderDrillIn(*m.DrillIn, m.Height)
	}
}

// renderDockedBody renders the backlog, work-queue, and Transcript columns
// side by side — the docked pane mode's three-column body (issue #846, ADR
// 0025). The Transcript column takes transcriptColumnFraction of m.Width;
// the remainder splits between backlog and queue exactly as renderBody does
// for the two-column body. transcriptHeight is the Transcript column's
// height budget — see the transcriptHeight function (issue #1014).
//
// All three columns are joined on the same rows (joinColumns takes the max
// line count across each join), so backlog and queue must be windowed to
// transcriptHeight too, not left unbounded — otherwise a long backlog/queue
// wins that max and grows the docked body past transcriptHeight regardless
// of the Transcript column's own length. Docked mode only renders once
// m.Width >= minThreeColumnWidth (effectivePaneMode falls back to
// PaneFullscreen below that), well above minTwoColumnWidth, so backlog and
// queue always render side by side here and each gets the full
// transcriptHeight budget, matching renderBody's own side-by-side case
// (issue #1381).
//
// BacklogOffset and QueueOffset are shared Model fields, not per-pane state,
// so the docked pane's backlog and queue columns always render the same
// scroll window as the main view — the two can never diverge. This is
// intentional, not a bug: reaching the end of a long backlog while drilled
// in is more useful than always resetting to row 0 (issue #1055).
func renderDockedBody(m Model, transcriptHeight int) string {
	backlog := renderBacklogColumn(m, &transcriptHeight)
	transcript := renderTranscriptColumn(*m.DrillIn, transcriptHeight)

	transcriptWidth := int(float64(m.Width) * transcriptColumnFraction)
	bodyWidth := m.Width - transcriptWidth

	leftWidth := splitLeftWidth(backlog, bodyWidth)
	queueWidth := bodyWidth - leftWidth

	queue := renderQueueColumn(m, &transcriptHeight, queueWidth)
	body := joinColumns(backlog, queue, leftWidth, queueWidth)
	return joinColumns(body, transcript, bodyWidth, transcriptWidth)
}

// renderFloatingBody renders the two-column body with the Transcript
// overlaid atop its right side, for as many leading rows as the Transcript
// content needs — the floating pane mode (issue #846, ADR 0025). Rows past
// the overlay's height render the plain two-column body untouched, unlike
// renderDockedBody's every-row column split. transcriptHeight is the
// Transcript column's height budget — see the transcriptHeight function
// (issue #1014).
//
// The body sits underneath the overlay, not beside it, so it gets the same
// transcriptHeight budget as the Transcript column rather than an unbounded
// one — overlay only clips rows the Transcript actually covers and leaves
// any row past that untouched, so an unbounded body would grow past
// transcriptHeight on its own regardless of the Transcript's length. Passing
// a real budget also reuses renderBody's own side-by-side/stacked split
// (splitStackedBudget) instead of inventing a second one here (issue
// #1381).
//
// The underlying renderBody call is subject to the same shared-offset
// behavior documented on renderDockedBody: BacklogOffset/QueueOffset are
// Model fields, not per-pane, so the body underneath the floating overlay
// always scrolls in sync with the main view (issue #1055).
func renderFloatingBody(m Model, transcriptHeight int) string {
	body := renderBody(m, &transcriptHeight)
	transcript := renderTranscriptColumn(*m.DrillIn, transcriptHeight)
	floatWidth := int(float64(m.Width) * transcriptColumnFraction)
	return overlay(body, transcript, m.Width-floatWidth, floatWidth)
}

// overlay writes pane's lines over the right-most paneWidth runes of body's
// leading lines, one per pane line, leaving any body line beyond len(pane's
// lines) untouched — a fixed-footprint overlay rather than a full column
// join (issue #846, ADR 0025). When pane has more lines than body (a short
// backlog/queue under a taller transcript pane), the extra pane lines are
// appended as new rows with a blank left side rather than dropped — the
// pane's keystroke-hint footer must always reach the screen (issue #1002).
func overlay(body, pane string, leftWidth, paneWidth int) string {
	bodyLines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	paneLines := strings.Split(strings.TrimRight(pane, "\n"), "\n")
	for i, pl := range paneLines {
		left := ""
		if i < len(bodyLines) {
			left = bodyLines[i]
		}
		line := clip(left, leftWidth, true) + clip(pl, paneWidth, true)
		if i < len(bodyLines) {
			bodyLines[i] = line
		} else {
			bodyLines = append(bodyLines, line)
		}
	}
	return strings.Join(bodyLines, "\n") + "\n"
}

// writeWindowedRows writes rows[offset:], clipped to budget rows — the
// backlog/picks columns' body-windowing counterpart to windowLines' offset
// slicing (issue #1035, scrolled per offset since issue #1036). When more
// rows remain past offset than budget allows, one row is held back for a
// trailing "N more below" affordance line instead of just truncating
// silently, so the operator knows the list is clipped rather than complete.
// A non-positive budget writes nothing; an offset past the end of rows is
// treated as the end (nothing left to show). A nil budget means unbounded —
// every row from offset writes, with no "more below" affordance (issue
// #1039).
func writeWindowedRows(b *strings.Builder, rows []string, offset int, budget *int) {
	if offset < 0 {
		offset = 0
	}
	if offset > len(rows) {
		offset = len(rows)
	}
	remaining := rows[offset:]
	if budget == nil {
		for _, r := range remaining {
			b.WriteString(r)
		}
		return
	}
	bud := *budget
	if bud < 0 {
		bud = 0
	}
	if len(remaining) <= bud {
		for _, r := range remaining {
			b.WriteString(r)
		}
		return
	}
	visible := bud - 1
	if visible < 0 {
		visible = 0
	}
	for _, r := range remaining[:visible] {
		b.WriteString(r)
	}
	if bud > 0 {
		fmt.Fprintf(b, "… %d more below\n", len(remaining)-visible)
	}
}

// writeColumn renders a body column's label line followed by rows windowed
// to budget-1 (the label costs one row) — the guard/label/window plumbing
// renderBacklogColumn and renderQueueColumn shared inline before extraction
// (issue #1040). A non-positive budget renders nothing at all, matching
// their own budget<=0 early return (issue #1035); the row-building loops
// themselves stay in each caller since they differ per column.
func writeColumn(label string, rows []string, offset, budget int) string {
	if budget <= 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s:\n", label)
	itemBudget := columnItemBudget(budget)
	writeWindowedRows(&b, rows, offset, &itemBudget)
	return b.String()
}

// windowedRowCount returns how many of remaining rows writeWindowedRows
// actually renders as content for a given budget — remaining itself when it
// all fits, or one less than budget (the row held back for the "N more
// below" affordance) when it doesn't. Update reuses this to compute the
// focused column's viewport capacity at a given offset, so
// cursor-follows-viewport (issue #1036) advances/rewinds the offset exactly
// when the rendered window is about to stop showing the cursor's row.
func windowedRowCount(remaining, budget int) int {
	if budget < 0 {
		budget = 0
	}
	if remaining < 0 {
		remaining = 0
	}
	if remaining <= budget {
		return remaining
	}
	n := budget - 1
	if n < 0 {
		n = 0
	}
	return n
}

// bodyBudget returns the row budget left for the two-column body after the
// header and any active prompt/error lines — the same figure View computes
// before calling renderBody (issue #1035). Update reuses it so
// cursor-follows-viewport (issue #1036) scrolls against the exact window
// View is about to render, rather than a second, potentially-diverging
// calculation.
func bodyBudget(m Model) int {
	header := renderHeader(m)
	reservedLines := 0
	if m.FilterEditing {
		reservedLines++
	}
	if m.PendingTerminate != "" {
		reservedLines++
	}
	if m.PendingQuit {
		reservedLines++
	}
	if m.Err != nil {
		reservedLines++
	}
	budget := m.Height - strings.Count(header, "\n") - reservedLines
	if budget < 0 {
		budget = 0
	}
	return budget
}

// bodyColumnBudgets returns the row budget renderBody gives the backlog and
// queue columns for m's current Width/Height and prompt state — the
// stacked-mode split is shared with renderBody via splitStackedBudget, so
// the two can never diverge (issue #1052); a side-by-side terminal gives
// each column the full budget, since they share output lines. Update
// reuses this so cursor-follows-viewport (issue #1036) computes each
// column's viewport against the same budget View renders with.
func bodyColumnBudgets(m Model) (backlog, queue int) {
	budget := bodyBudget(m)
	if budget <= 0 {
		return 0, 0
	}
	if m.Width < minTwoColumnWidth {
		return splitStackedBudget(budget)
	}
	return budget, budget
}

// focusedBudget returns the focused column's row budget (label line
// included) — bodyColumnBudgets' queue half while the work queue has focus,
// its backlog half otherwise (issue #1062).
func focusedBudget(m Model) int {
	backlogBudget, queueBudget := bodyColumnBudgets(m)
	if m.Focus == FocusQueue {
		return queueBudget
	}
	return backlogBudget
}

// positionLabel returns a compact " (X-Y of N)" position indicator for a
// column's label, describing the rows writeWindowedRows actually renders at
// offset within columnBudget of total — or "" when there is nothing to show
// a range for (an empty list, or a budget too small to render any row), so a
// column that renders no rows doesn't grow a misleading "(1-0 of 0)" label
// (issue #1037 AC3). A nil columnBudget means unbounded — every row from
// offset is shown, matching writeWindowedRows' own nil handling (issue
// #1039).
func positionLabel(offset int, columnBudget *int, total int) string {
	if total == 0 {
		return ""
	}
	var shown int
	if columnBudget == nil {
		shown = total - offset
	} else {
		shown = visibleItemCount(offset, *columnBudget, total)
	}
	if shown <= 0 {
		return ""
	}
	return fmt.Sprintf(" (%d-%d of %d)", offset+1, offset+shown, total)
}

// focusedPageSize returns the number of rows one page jump (pgup/pgdown)
// moves the focused body column's viewport by — the row count actually
// rendered at its current offset (windowedRowCount, the same figure
// positionLabel and writeWindowedRows use), not the raw item budget. A
// truncated window holds one row back for the "N more below" affordance, so
// paging by the raw budget would overshoot by one and skip the row right
// past the fold; paging by what's actually on screen lands exactly on the
// first row the operator hasn't seen yet, and stays correct across a
// terminal resize instead of a value fixed at startup (issue #1037 AC1/AC2).
// Unlike the drill-in transcript's fixed drillInPageScrollDelta, this is
// recomputed on every keypress.
func focusedPageSize(m Model) int {
	return visibleItemCount(*focusedOffset(&m), focusedBudget(m), focusedTotal(m))
}

// columnItemBudget converts a column's row budget (label line included, as
// bodyColumnBudgets returns it) into the row budget available for its item
// rows alone — the "-1 for the label" that renderBacklogColumn and
// renderQueueColumn get by calling columnItemBudget(budget) directly before
// passing the result to writeWindowedRows. A non-positive column budget
// yields zero items, matching those functions' own budget<=0-renders-nothing
// early return.
func columnItemBudget(columnBudget int) int {
	if columnBudget <= 0 {
		return 0
	}
	return columnBudget - 1
}

// visibleItemCount returns how many of a column's item rows are actually
// visible at offset within columnBudget of total — windowedRowCount's
// remaining/budget shape with columnItemBudget's "-1 for the label" folded
// in, so positionLabel and focusedPageSize don't each repeat the
// windowedRowCount(total-offset, columnItemBudget(budget)) composition
// (issue #1061).
func visibleItemCount(offset, columnBudget, total int) int {
	return windowedRowCount(total-offset, columnItemBudget(columnBudget))
}

// followViewport returns offset adjusted so cursor stays within the window
// writeWindowedRows would render at itemBudget rows — rewinding one row at a
// time while cursor sits above offset, advancing one row at a time while
// cursor sits past the last row windowedRowCount would actually show,
// exactly the "moving the cursor down past the bottom visible row advances
// the offset by one... moving up past the top row rewinds it" behavior
// issue #1036 AC1 asks for. The result always stays in [0, total): the
// advance loop stops at total-1 rather than total so a non-positive
// itemBudget (windowedRowCount always 0, so the break condition never
// fires) can't push offset one past the last valid index (issue #1054).
func followViewport(offset, cursor, total, itemBudget int) int {
	for cursor < offset {
		offset--
	}
	for offset < total-1 {
		if cursor < offset+windowedRowCount(total-offset, itemBudget) {
			break
		}
		offset++
	}
	return offset
}

// windowLines returns d.Lines[offset:end], where end stops budget lines past
// offset (or at the end of d.Lines, whichever comes first) — so a render
// joins only what the viewport can show instead of the whole tail from
// Offset to the end of a (potentially multi-MB) transcript (issue #722). A
// non-positive budget yields an empty window rather than a negative slice.
// d.Offset is assumed already in [0, len(d.Lines)-1] — Update clamps it via
// clampDrillInOffset before any render call reaches here. As recorded when
// this windowing landed, a View call against a 10MB+ transcript at Offset
// 0, Height 24 (BenchmarkView_DrillInFullscreen_LargeTranscript, issue
// #1016) went from 3.88ms/op, 21.0MB/op, 7 allocs/op — the state right
// after the Lines cache above landed but before this windowing, still
// joining offset-to-end every call, itself down from 4.47ms/op, 23.5MB/op,
// 9 allocs/op pre-cache — to 1.6µs/op, 3.39KB/op, 5 allocs/op (windowed).
// The alloc counts are the invariant; absolute ns/op and B/op vary by
// machine, Go version, and allocator behavior. Reproduce with `go test
// ./internal/console/... -run '^$' -bench BenchmarkView_DrillInFullscreen
// -benchmem` from cmd/launcher.
func windowLines(d DrillInState, budget int) []string {
	offset := d.Offset
	end := offset + budget
	if end < offset {
		end = offset
	}
	if end > len(d.Lines) {
		end = len(d.Lines)
	}
	return d.Lines[offset:end]
}

// renderTranscriptColumn renders d as a labeled column: a header naming the
// pick and current form, as much of its loaded content from Offset onward as
// height allows, and a keystroke hint — the same content and footer
// renderDrillIn shows fullscreen, reused for the docked and floating pane
// modes (issue #846, ADR 0025; hint added #1002). The content+footer body
// below is currently duplicated with renderDrillIn; #1000 tracks extracting
// a shared helper.
//
// The label and footer are themselves budgeted against height (issue
// #1380): at height 1, only the label renders and the footer is dropped —
// the one case where the #1002 "footer always reaches the screen" invariant
// doesn't hold, because there isn't room for both lines.
func renderTranscriptColumn(d DrillInState, height int) string {
	if height <= 0 {
		return ""
	}

	var b strings.Builder
	if d.ShowRaw {
		fmt.Fprintf(&b, "transcript #%s (raw):\n", d.Number)
	} else {
		fmt.Fprintf(&b, "transcript #%s:\n", d.Number)
	}
	const labelLines = 1
	if height <= headerFooterLines-labelLines {
		return b.String()
	}
	if d.Err != nil {
		fmt.Fprintf(&b, "drill-in failed: %s\n", d.Err)
		return b.String()
	}

	visible := strings.Join(windowLines(d, height-headerFooterLines), "\n")
	b.WriteString(visible)
	if visible != "" && !strings.HasSuffix(visible, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("[t] toggle raw · [x] close\n")
	return b.String()
}

// headerFooterLines is the drill-in chrome budget (header + keystroke-hint
// footer) that renderDrillIn, renderTranscriptColumn, and clampDrillInOffset
// all subtract from height — shared so the clamp's last-page cap always
// matches what the renderers actually have room to show (issue #829,
// #1002).
const headerFooterLines = 2

// renderDrillIn renders one Dispatch's transcript view: a header naming the
// pick and current mode, as much of the loaded content (rendered by default,
// raw when ShowRaw) as height allows, and a keystroke hint. Err renders in
// place of content instead of a blank pane.
func renderDrillIn(d DrillInState, height int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "transcript #%s", d.Number)
	if d.ShowRaw {
		b.WriteString(" (raw)")
	}
	b.WriteString("\n")

	if d.Err != nil {
		fmt.Fprintf(&b, "drill-in failed: %s\n", d.Err)
		return b.String()
	}

	visible := strings.Join(windowLines(d, height-headerFooterLines), "\n")
	b.WriteString(visible)
	if visible != "" && !strings.HasSuffix(visible, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("[t] toggle raw · [x] close\n")
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
	if budget < 0 {
		budget = 0
	}
	lines := strings.Split(m.RebuildOutput, "\n")
	offset := m.RebuildOutputOffset
	end := offset + budget
	if end < offset {
		end = offset
	}
	if end > len(lines) {
		end = len(lines)
	}
	visible := strings.Join(lines[offset:end], "\n")
	b.WriteString(visible)
	if visible != "" && !strings.HasSuffix(visible, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("[x] close\n")
	return b.String()
}
