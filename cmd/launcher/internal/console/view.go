package console

import (
	"fmt"
	"strings"
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
	if m.ShowHelp {
		return renderHelp()
	}

	var b strings.Builder
	b.WriteString(renderHeader(m))
	if m.FilterEditing {
		fmt.Fprintf(&b, "/%s  [enter] apply · [esc] cancel\n", m.Filter)
	}
	if m.PendingTerminate != "" {
		fmt.Fprintf(&b, "terminate #%s? [y/N]\n", m.PendingTerminate)
	}
	if m.PendingQuit {
		b.WriteString("quit with live Dispatches: drain (d, default) / terminate-all (t) / stay (s)?\n")
	}
	b.WriteString(renderBody(m))
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

// renderBody renders the backlog and work-queue columns side by side,
// sized from m.Width — the two-column body under the header (issue #844,
// ADR 0025). Backlog keeps its label filter and cursor; the queue lists
// m.Picks in pick order, each row tagged with its PickState. The left
// column's width tracks the backlog's own longest line, capped at
// leftColumnFraction of m.Width; both columns clip any line that still
// overflows its share, so a joined row never exceeds m.Width regardless of
// how long a title, label list, or blocker badge runs (issue #844 AC6).
// Below minTwoColumnWidth the columns stack instead of splitting.
func renderBody(m Model) string {
	backlog := renderBacklogColumn(m)
	queue := renderQueueColumn(m)
	if m.Width < minTwoColumnWidth {
		return backlog + "\n" + queue
	}
	leftWidth := maxLineWidth(backlog)
	if maxLeft := int(float64(m.Width) * leftColumnFraction); leftWidth > maxLeft {
		leftWidth = maxLeft
	}
	rightWidth := m.Width - leftWidth
	return joinColumns(backlog, queue, leftWidth, rightWidth)
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

// renderBacklogColumn renders the queueable backlog: one line per visible
// issue (number, title, labels), with the cursor row marked — unchanged from
// the prior flat rendering, just under its own column label.
func renderBacklogColumn(m Model) string {
	var b strings.Builder
	if m.Focus == FocusBacklog {
		b.WriteString("backlog [focus]:\n")
	} else {
		b.WriteString("backlog:\n")
	}
	for i, iss := range m.Visible() {
		marker := " "
		if m.Focus == FocusBacklog && i == m.Cursor {
			marker = ">"
		}
		fmt.Fprintf(&b, "%s #%s  %s  [%s]\n", marker, iss.Number, iss.Title, strings.Join(iss.Labels, ", "))
	}
	return b.String()
}

// renderQueueColumn renders the work queue: one pick-ordered line per Pick,
// tagged with its PickState — a held row names its blocker, a running row
// carries its heartbeat.
func renderQueueColumn(m Model) string {
	var b strings.Builder
	if m.Focus == FocusQueue {
		b.WriteString("picks [focus]:\n")
	} else {
		b.WriteString("picks:\n")
	}
	for i, p := range m.Picks {
		marker := " "
		if m.Focus == FocusQueue && i == m.QueueCursor {
			marker = ">"
		}
		fmt.Fprintf(&b, "%s #%s  [%s]  %s", marker, p.Number, p.State, p.Title)
		if p.BlockedBy != "" {
			fmt.Fprintf(&b, "  (held by %s)", p.BlockedBy)
		}
		if p.Reason != "" {
			fmt.Fprintf(&b, "  (%s)", p.Reason)
		}
		if p.Heartbeat != "" {
			fmt.Fprintf(&b, "  %s", p.Heartbeat)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// joinColumns zips left and right line by line, clipping each side to its
// column width — left is padded out to leftWidth so the right column lines
// up in a consistent gutter, right is truncated if it overflows rightWidth —
// so a joined row never exceeds leftWidth+rightWidth regardless of how long
// either side's content runs (issue #844 AC6).
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
		fmt.Fprintf(&b, "%s%s\n", clip(l, leftWidth, true), clip(r, rightWidth, false))
	}
	return b.String()
}

// clip fits s into width runes: truncated with a trailing ellipsis if s runs
// over, space-padded out to width if pad is true and s is shorter, left
// as-is if pad is false and s already fits.
func clip(s string, width int, pad bool) string {
	r := []rune(s)
	switch {
	case len(r) > width:
		if width <= 1 {
			return string(r[:width])
		}
		return string(r[:width-1]) + "…"
	case pad:
		return s + strings.Repeat(" ", width-len(r))
	default:
		return s
	}
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

// bannerHeight is the banner's row count (including its leading blank line)
// — the header collapses the banner away once Height drops below it, so the
// banner never pushes the backlog/queue off-screen on a short terminal.
var bannerHeight = strings.Count(banner, "\n")

// renderHeader renders the Console's full-width header: the fixed banner
// (when the terminal is tall enough to afford it), the status line
// (running/cap, waiting, held, settled), and the stale-image/competing-
// dogfood alert lines. Status counts are derived from Cap, Live, and the
// Picks slice's PickState tags rather than a new stored counter (issue
// #843, ADR 0025).
func renderHeader(m Model) string {
	var waiting, held, settled int
	for _, p := range m.Picks {
		switch p.State {
		case PickQueued:
			waiting++
		case PickHeld:
			held++
		case PickSettled:
			settled++
		}
	}

	var b strings.Builder
	if m.Height >= bannerHeight {
		b.WriteString(strings.TrimPrefix(banner, "\n"))
	}
	fmt.Fprintf(&b, "running %d/%d · waiting %d · held %d · settled %d\n", m.Live, m.Cap, waiting, held, settled)
	if m.Stale {
		fmt.Fprintf(&b, "!! image stale: %s — new launches held; press [b] to rebuild\n", m.StaleMessage)
	}
	if m.Rebuilding {
		b.WriteString("==> rebuilding image...\n")
	}
	if m.RebuildErr != "" {
		fmt.Fprintf(&b, "!! rebuild failed: %s\n", m.RebuildErr)
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
		"  up          move cursor up",
		"  tab         switch focus between the backlog and work-queue columns",
		"  /           filter by label substring",
		"  enter       apply filter (while filter-editing); otherwise: pick the",
		"              highlighted backlog row (backlog focus), or drill into the",
		"              highlighted pick's transcript (queue focus, only when it has",
		"              one)",
		"  esc         cancel filter edit",
		"  t           toggle rendered <-> raw JSONL (while drilled in)",
		"  x / esc     close the transcript pane (while drilled in)",
		"  j/k, pgup/pgdown  scroll the transcript (while drilled in)",
		"  m           cycle the transcript pane docked/floating/fullscreen",
		"              (while drilled in)",
		"  r           refresh the backlog",
		"  p           pick the highlighted issue (launch button)",
		"  u           unpick the highlighted queued pick",
		"  pa          pick all ready (bulk pick-all-ready gesture)",
		"  k           terminate the highlighted live Dispatch (confirm y/N)",
		"  +           raise the live parallelism cap",
		"  -           lower the live parallelism cap",
		"  b           rebuild the stale image in-session",
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

// renderDrillInPane renders the open DrillIn per its effective pane mode —
// docked, floating, or fullscreen (issue #846, ADR 0025).
func renderDrillInPane(m Model) string {
	switch effectivePaneMode(m) {
	case PaneDocked:
		return renderHeader(m) + renderDockedBody(m)
	case PaneFloating:
		return renderHeader(m) + renderFloatingBody(m)
	default:
		return renderDrillIn(*m.DrillIn)
	}
}

// renderDockedBody renders the backlog, work-queue, and Transcript columns
// side by side — the docked pane mode's three-column body (issue #846, ADR
// 0025). The Transcript column takes transcriptColumnFraction of m.Width;
// the remainder splits between backlog and queue exactly as renderBody does
// for the two-column body.
func renderDockedBody(m Model) string {
	backlog := renderBacklogColumn(m)
	queue := renderQueueColumn(m)
	transcript := renderTranscriptColumn(*m.DrillIn)

	transcriptWidth := int(float64(m.Width) * transcriptColumnFraction)
	bodyWidth := m.Width - transcriptWidth

	leftWidth := maxLineWidth(backlog)
	if maxLeft := int(float64(bodyWidth) * leftColumnFraction); leftWidth > maxLeft {
		leftWidth = maxLeft
	}
	queueWidth := bodyWidth - leftWidth

	body := joinColumns(backlog, queue, leftWidth, queueWidth)
	return joinColumns(body, transcript, bodyWidth, transcriptWidth)
}

// renderFloatingBody renders the two-column body with the Transcript
// overlaid atop its right side, for as many leading rows as the Transcript
// content needs — the floating pane mode (issue #846, ADR 0025). Rows past
// the overlay's height render the plain two-column body untouched, unlike
// renderDockedBody's every-row column split.
func renderFloatingBody(m Model) string {
	body := renderBody(m)
	transcript := renderTranscriptColumn(*m.DrillIn)
	floatWidth := int(float64(m.Width) * transcriptColumnFraction)
	return overlay(body, transcript, m.Width-floatWidth, floatWidth)
}

// overlay writes pane's lines over the right-most paneWidth runes of body's
// leading lines, one per pane line, leaving any body line beyond len(pane's
// lines) untouched — a fixed-footprint overlay rather than a full column
// join (issue #846, ADR 0025).
func overlay(body, pane string, leftWidth, paneWidth int) string {
	bodyLines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	paneLines := strings.Split(strings.TrimRight(pane, "\n"), "\n")
	for i, pl := range paneLines {
		if i >= len(bodyLines) {
			break
		}
		bodyLines[i] = clip(bodyLines[i], leftWidth, true) + clip(pl, paneWidth, true)
	}
	return strings.Join(bodyLines, "\n") + "\n"
}

// renderTranscriptColumn renders d as a labeled column: a header naming the
// pick and current form, then its loaded content from Offset onward — the
// same content renderDrillIn shows fullscreen, reused for the docked and
// floating pane modes (issue #846, ADR 0025).
func renderTranscriptColumn(d DrillInState) string {
	var b strings.Builder
	if d.ShowRaw {
		fmt.Fprintf(&b, "transcript #%s (raw):\n", d.Number)
	} else {
		fmt.Fprintf(&b, "transcript #%s:\n", d.Number)
	}
	if d.Err != nil {
		fmt.Fprintf(&b, "drill-in failed: %s\n", d.Err)
		return b.String()
	}

	content := d.Rendered
	if d.ShowRaw {
		content = d.Raw
	}
	lines := strings.Split(content, "\n")
	offset := d.Offset
	if offset > len(lines) {
		offset = len(lines)
	}
	b.WriteString(strings.Join(lines[offset:], "\n"))
	b.WriteString("\n")
	return b.String()
}

// renderDrillIn renders one Dispatch's transcript view: a header naming the
// pick and current mode, the loaded content (rendered by default, raw when
// ShowRaw), and a keystroke hint. Err renders in place of content instead of
// a blank pane.
func renderDrillIn(d DrillInState) string {
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

	content := d.Rendered
	if d.ShowRaw {
		content = d.Raw
	}
	lines := strings.Split(content, "\n")
	offset := d.Offset
	if offset > len(lines) {
		offset = len(lines)
	}
	visible := strings.Join(lines[offset:], "\n")
	b.WriteString(visible)
	if visible != "" && !strings.HasSuffix(visible, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("[t] toggle raw · [x] close\n")
	return b.String()
}
