package console

import (
	"fmt"
	"strings"
)

// View renders m as the text the run loop writes to the terminal: an
// optional dogfood-competition notice, the visible backlog (one line per
// issue: number, title, labels), and any refresh error. An open drill-in
// (m.DrillIn != nil) replaces the backlog/queue rendering entirely with the
// transcript view — the operator is looking at one Dispatch's work, not the
// list.
func View(m Model) string {
	if m.DrillIn != nil {
		return renderDrillIn(*m.DrillIn)
	}
	if m.ShowHelp {
		return renderHelp()
	}

	var b strings.Builder
	if m.FilterEditing {
		fmt.Fprintf(&b, "/%s  [enter] apply · [esc] cancel\n", m.Filter)
	}
	if m.PendingTerminate != "" {
		fmt.Fprintf(&b, "terminate #%s? [y/N]\n", m.PendingTerminate)
	}
	if m.PendingQuit {
		b.WriteString("quit with live Dispatches: drain (d, default) / terminate-all (t) / stay (s)?\n")
	}
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
	for i, iss := range m.Visible() {
		marker := " "
		if i == m.Cursor {
			marker = ">"
		}
		fmt.Fprintf(&b, "%s #%s  %s  [%s]\n", marker, iss.Number, iss.Title, strings.Join(iss.Labels, ", "))
	}
	if m.Err != nil {
		fmt.Fprintf(&b, "refresh failed: %s\n", m.Err)
	}
	if m.Cap > 0 {
		fmt.Fprintf(&b, "cap: %d/%d\n", m.Live, m.Cap)
	}
	if len(m.Picks) > 0 {
		b.WriteString("picks:\n")
		for _, p := range m.Picks {
			fmt.Fprintf(&b, "  #%s  %s  %s", p.Number, p.State, p.Title)
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
		"  /           filter by label substring",
		"  enter       apply filter",
		"  esc         cancel filter edit",
		"  d / enter   drill into the highlighted dispatch's transcript",
		"  t           toggle rendered <-> raw JSONL (while drilled in)",
		"  x / esc     close the transcript pane (while drilled in)",
		"  j/k, pgup/pgdown  scroll the transcript (while drilled in)",
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
