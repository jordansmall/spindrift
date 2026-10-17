package driverkit

import (
	"fmt"
	"sort"
	"strings"
)

// FormatRoleHeader returns a switch-header line for the acting role, e.g.
// "#284 ── implementor · opus ──────────". A non-empty model follows the role.
func FormatRoleHeader(issue, role, model string) string {
	const targetWidth = 36
	const minTrail = 4
	label := SanitizeLine(role)
	if model != "" {
		label = label + " \xc2\xb7 " + model
	}
	prefix := "#" + issue + " \xe2\x94\x80\xe2\x94\x80 " + label + " "
	trail := targetWidth - len([]rune(prefix))
	if trail < minTrail {
		trail = minTrail
	}
	return prefix + strings.Repeat("\xe2\x94\x80", trail)
}

// FormatHeartbeat returns a coarse status line for one running issue, e.g.
// "#42 scout [plan] · 3 turns". It names any role other than the implementor
// first so the line is never mistaken for implementor output.
func FormatHeartbeat(issue string, turns int, lastTool, role, phase string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "#%s", issue)
	if role != "" && role != ImplementorRole {
		fmt.Fprintf(&sb, " %s", SanitizeLine(role))
	}
	if phase != "" {
		fmt.Fprintf(&sb, " [%s]", phase)
	}
	if turns > 0 {
		plural := "s"
		if turns == 1 {
			plural = ""
		}
		fmt.Fprintf(&sb, " \xc2\xb7 %d turn%s", turns, plural)
	}
	if lastTool != "" {
		fmt.Fprintf(&sb, " \xc2\xb7 %s", lastTool)
	}
	return sb.String()
}

// FormatCountLine returns a count summary line for accumulated tool calls,
// e.g. "#42 scout [explore] · 1 read". It names any role other than the
// implementor first so the line is never mistaken for implementor output. It
// uses the neutral default vocabulary (every kind sorted, plural by "s"), so a
// driver's own count lines go through its HeartbeatTracking.FormatCountLine.
func FormatCountLine(issue, role, phase string, counts map[string]int) string {
	return HeartbeatTracking{}.FormatCountLine(issue, role, phase, counts)
}

// FormatCountLine returns a count summary line for accumulated tool calls,
// naming t's KindOrder kinds first and pluralizing through t's KindPlurals.
func (t HeartbeatTracking) FormatCountLine(issue, role, phase string, counts map[string]int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "#%s", issue)
	if role != "" && role != ImplementorRole {
		fmt.Fprintf(&sb, " %s", SanitizeLine(role))
	}
	if phase != "" {
		fmt.Fprintf(&sb, " [%s]", phase)
	}
	fmt.Fprintf(&sb, " \xc2\xb7 %s", t.formatCounts(counts))
	return sb.String()
}

// formatCounts returns a comma-separated count string, e.g. "3 reads, 2 greps".
// KindOrder kinds come first in that order, the rest sorted, so output is
// deterministic.
func (t HeartbeatTracking) formatCounts(counts map[string]int) string {
	seen := make(map[string]bool, len(t.KindOrder))
	var parts []string
	for _, kind := range t.KindOrder {
		if n := counts[kind]; n > 0 {
			seen[kind] = true
			parts = append(parts, fmt.Sprintf("%d %s", n, t.pluralKind(kind, n)))
		}
	}
	var extra []string
	for kind := range counts {
		if !seen[kind] && counts[kind] > 0 {
			extra = append(extra, kind)
		}
	}
	sort.Strings(extra)
	for _, kind := range extra {
		n := counts[kind]
		parts = append(parts, fmt.Sprintf("%d %s", n, t.pluralKind(kind, n)))
	}
	return strings.Join(parts, ", ")
}

func (t HeartbeatTracking) pluralKind(kind string, n int) string {
	if n == 1 {
		return kind
	}
	if p, ok := t.KindPlurals[kind]; ok {
		return p
	}
	return kind + "s"
}
