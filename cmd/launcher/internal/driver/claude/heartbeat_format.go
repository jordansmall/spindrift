package claude

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"spindrift.dev/launcher/internal/landdelta"
)

// sanitizeRole strips control characters, ANSI escape sequences, and
// newlines from s. The role is agent-controlled (it comes from the Task
// tool's subagent_type input) and every heartbeat line must stay single-line.
func sanitizeRole(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == 0x1b:
			i += size
			if i >= len(s) {
				continue
			}
			switch s[i] {
			case '[':
				i++
				for i < len(s) && !(s[i] >= 0x40 && s[i] <= 0x7e) {
					i++
				}
				if i < len(s) {
					i++
				}
			case ']':
				i++
				for i < len(s) && s[i] != 0x07 {
					if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
						i += 2
						break
					}
					i++
				}
				if i < len(s) && s[i] == 0x07 {
					i++
				}
			}
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			i += size
		default:
			b.WriteRune(r)
			i += size
		}
	}
	return b.String()
}

// FormatRoleHeader returns a switch-header line for the acting role, e.g.
// "#284 ── implementor · opus ──────────". A non-empty model follows the role.
func FormatRoleHeader(issue, role, model string) string {
	const targetWidth = 36
	const minTrail = 4
	label := sanitizeRole(role)
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
		fmt.Fprintf(&sb, " %s", sanitizeRole(role))
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
// implementor first so the line is never mistaken for implementor output.
func FormatCountLine(issue, role, phase string, counts map[string]int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "#%s", issue)
	if role != "" && role != ImplementorRole {
		fmt.Fprintf(&sb, " %s", sanitizeRole(role))
	}
	if phase != "" {
		fmt.Fprintf(&sb, " [%s]", phase)
	}
	fmt.Fprintf(&sb, " \xc2\xb7 %s", formatCounts(counts))
	return sb.String()
}

// FormatSpindriftOp returns a single status line for one orchestrator
// operation (issue #2027), marked with "○" so it does not read as
// implementor narration.
func FormatSpindriftOp(issue string, op SpindriftOp) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "#%s \xe2\x97\x8b ", issue)
	switch op.Op {
	case "pass_start":
		if op.Role != "" {
			fmt.Fprintf(&sb, "pass %d (%s) started", op.Pass, sanitizeRole(op.Role))
		} else {
			fmt.Fprintf(&sb, "pass %d started", op.Pass)
		}
	case "verdict":
		fmt.Fprintf(&sb, "verdict: %s", sanitizeRole(op.Verdict))
	case "pass_no_outcome":
		if op.Verdict != "" {
			fmt.Fprintf(&sb, "pass %d ended with no outcome (last verdict %s, %s)", op.Pass, sanitizeRole(op.Verdict), sanitizeRole(op.Reason))
		} else {
			fmt.Fprintf(&sb, "pass %d ended with no outcome (%s)", op.Pass, sanitizeRole(op.Reason))
		}
	case "decision":
		if op.Reason != "" {
			fmt.Fprintf(&sb, "%s: %s", sanitizeRole(op.Decision), sanitizeRole(op.Reason))
		} else {
			sb.WriteString(sanitizeRole(op.Decision))
		}
	case "pass_usage":
		if op.Role != "" {
			fmt.Fprintf(&sb, "pass %d (%s) usage: ", op.Pass, sanitizeRole(op.Role))
		} else {
			fmt.Fprintf(&sb, "pass %d usage: ", op.Pass)
		}
		// Usage is nil for a pass that crashed or emitted no usage events
		// (issue #3156 AC), so render zero totals rather than dereferencing.
		var u PassUsage
		if op.Usage != nil {
			u = *op.Usage
		}
		// Only a payload that claims main-loop-only output gets the caveat, so
		// this never understates a driver that reports whole-pass output. See
		// usage.Report.OutputIsMainLoopOnly.
		out := "out"
		if u.OutputIsMainLoopOnly {
			out = "out (main loop)"
		}
		fmt.Fprintf(&sb, "%d calls, %d in, %d %s, %d cache read, %d cache write",
			u.APICalls, u.UncachedInputTokens, u.OutputTokens, out, u.CacheReadInputTokens, u.CacheCreationInputTokens)
		if len(u.Agents) > 0 {
			// Render in the payload's given order (main loop first, then
			// costliest subagent per breakdownByAgentFile); do not re-sort.
			parts := make([]string, len(u.Agents))
			for i, a := range u.Agents {
				parts[i] = fmt.Sprintf("%s %d", sanitizeRole(a.Agent), a.TotalTokens())
			}
			fmt.Fprintf(&sb, " · %s", strings.Join(parts, ", "))
		}
	case "land_delta":
		// Delta is nil on a marshaled-but-degraded op, so render the zero
		// Delta rather than dereferencing. Summary() owns the counted, zero
		// and unknown wording (issue #3244) and the PR body renders the same
		// call, so never restate it here. The stand-in reason below keeps
		// Summary() from printing an empty "unknown ()".
		var d landdelta.Delta
		if op.Delta != nil {
			d = *op.Delta
		}
		if !d.Known && d.Reason == "" {
			d.Reason = "no delta reported"
		}
		sb.WriteString(sanitizeRole(d.Summary()))
	case "delta_review_trigger":
		// The Reason is the whole point of this op (issue #3246) and must show
		// even on the common "skip" path, so this mirrors the "decision" case
		// rather than the default arm's bare op name.
		if op.Reason != "" {
			fmt.Fprintf(&sb, "delta review %s: %s", sanitizeRole(op.Decision), sanitizeRole(op.Reason))
		} else {
			fmt.Fprintf(&sb, "delta review %s", sanitizeRole(op.Decision))
		}
	case "run_state_error":
		// dispositions_budget (issue #2550 AC9) and decisions_budget (issue
		// #2695) are loud, non-fatal tripwires, not a run-state disk failure,
		// so each gets its own wording.
		switch op.Phase {
		case "dispositions_budget":
			fmt.Fprintf(&sb, "dispositions budget: %s", sanitizeRole(op.Error))
		case "decisions_budget":
			fmt.Fprintf(&sb, "decisions budget: %s", sanitizeRole(op.Error))
		default:
			fmt.Fprintf(&sb, "run-state %s failed: %s", sanitizeRole(op.Phase), sanitizeRole(op.Error))
		}
	default:
		sb.WriteString(sanitizeRole(op.Op))
	}
	return sb.String()
}

// ModelFamily shortens a full model ID to its family label ("claude-opus-…"
// becomes "opus"), returning the id unchanged when no known family matches.
func ModelFamily(id string) string {
	if id == "" {
		return ""
	}
	for _, family := range []string{"haiku", "sonnet", "opus"} {
		if strings.Contains(id, family) {
			return family
		}
	}
	return id
}

func toolKind(name string) string {
	if isSubagentSpawnTool(name) {
		return "subagent"
	}
	switch name {
	case "Read":
		return "read"
	case "Edit", "Write", "NotebookEdit":
		return "edit"
	case "Grep", "Glob":
		return "grep"
	case "WebSearch", "WebFetch":
		return "search"
	default:
		return strings.ToLower(name)
	}
}

// formatCounts returns a comma-separated count string, e.g. "3 reads, 2 greps".
// Kinds are emitted in a fixed display order so output is deterministic.
func formatCounts(counts map[string]int) string {
	order := []string{"read", "edit", "grep", "search", "bash", "subagent"}
	seen := make(map[string]bool, len(order))
	var parts []string
	for _, kind := range order {
		if n := counts[kind]; n > 0 {
			seen[kind] = true
			parts = append(parts, fmt.Sprintf("%d %s", n, pluralKind(kind, n)))
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
		parts = append(parts, fmt.Sprintf("%d %s", n, pluralKind(kind, n)))
	}
	return strings.Join(parts, ", ")
}

func pluralKind(kind string, n int) string {
	if n == 1 {
		return kind
	}
	switch kind {
	case "search":
		return "searches"
	default:
		return kind + "s"
	}
}

// trimNarration returns the first sentence of text, capped at 120 characters.
// It only trims; the caller decides whether to emit subagent text
// (parent_tool_use_id != "").
func trimNarration(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if i := strings.IndexAny(text, ".!?\n"); i >= 0 {
		text = strings.TrimSpace(text[:i+1])
		if len(text) > 0 && text[len(text)-1] == '\n' {
			text = strings.TrimSpace(text[:len(text)-1])
		}
	}
	if len(text) > 120 {
		text = text[:117] + "..."
	}
	return text
}

// toolToPhase maps a tool name and its input to the current work phase. It is
// the single authoritative place for phase heuristics.
func toolToPhase(name string, input json.RawMessage) string {
	if isSubagentSpawnTool(name) {
		var ti TaskInput
		if len(input) > 0 {
			_ = json.Unmarshal(input, &ti)
		}
		switch strings.ToLower(ti.SubagentType) {
		case "reviewer", "review-axis":
			return "review"
		case "scout", "plan":
			return "plan"
		}
		return "explore"
	}
	switch name {
	case "Edit", "Write", "NotebookEdit":
		return "edit"
	case "Grep", "Glob", "WebSearch", "WebFetch":
		return "search"
	case "Bash":
		var m map[string]interface{}
		if len(input) > 0 {
			if err := json.Unmarshal(input, &m); err == nil {
				if cmd, ok := m["command"].(string); ok {
					if strings.Contains(cmd, "go test") {
						return "test"
					}
					if strings.Contains(cmd, "git commit") {
						return "commit"
					}
					if strings.Contains(cmd, "git ") || strings.Contains(cmd, "gh pr") {
						return "git"
					}
				}
			}
		}
		return "explore"
	default:
		return "explore"
	}
}
