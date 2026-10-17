package driverkit

import (
	"fmt"
	"strings"

	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/landdelta"
)

// FormatSpindriftOp returns a single status line for one orchestrator
// operation (issue #2027), marked with "○" so it does not read as
// implementor narration.
func FormatSpindriftOp(issue string, op hostop.SpindriftOp) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "#%s \xe2\x97\x8b ", issue)
	switch op.Op {
	case hostop.OpPassStart:
		if op.Role != "" {
			fmt.Fprintf(&sb, "pass %d (%s) started", op.Pass, SanitizeLine(op.Role))
		} else {
			fmt.Fprintf(&sb, "pass %d started", op.Pass)
		}
	case hostop.OpVerdict:
		fmt.Fprintf(&sb, "verdict: %s", SanitizeLine(op.Verdict))
	case hostop.OpPassNoOutcome:
		if op.Verdict != "" {
			fmt.Fprintf(&sb, "pass %d ended with no outcome (last verdict %s, %s)", op.Pass, SanitizeLine(op.Verdict), SanitizeLine(op.Reason))
		} else {
			fmt.Fprintf(&sb, "pass %d ended with no outcome (%s)", op.Pass, SanitizeLine(op.Reason))
		}
	case hostop.OpDecision:
		if op.Reason != "" {
			fmt.Fprintf(&sb, "%s: %s", SanitizeLine(op.Decision), SanitizeLine(op.Reason))
		} else {
			sb.WriteString(SanitizeLine(op.Decision))
		}
	case hostop.OpPassUsage:
		if op.Role != "" {
			fmt.Fprintf(&sb, "pass %d (%s) usage: ", op.Pass, SanitizeLine(op.Role))
		} else {
			fmt.Fprintf(&sb, "pass %d usage: ", op.Pass)
		}
		// Usage is nil for a pass that crashed or emitted no usage events
		// (issue #3156 AC), so render zero totals rather than dereferencing.
		var u hostop.PassUsage
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
				parts[i] = fmt.Sprintf("%s %d", SanitizeLine(a.Agent), a.TotalTokens())
			}
			fmt.Fprintf(&sb, " · %s", strings.Join(parts, ", "))
		}
	case hostop.OpLandDelta:
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
		sb.WriteString(SanitizeLine(d.Summary()))
	case hostop.OpSignal:
		// A reject's Reason is the whole point of the line, so it stands in
		// for the size/hash detail an accept carries (issue #3724).
		switch op.Decision {
		case "reject":
			fmt.Fprintf(&sb, "signal %s rejected: %s", SanitizeLine(op.Kind), SanitizeLine(op.Reason))
		case "read":
			fmt.Fprintf(&sb, "signal %s read", SanitizeLine(op.Kind))
		case "usage":
			fmt.Fprintf(&sb, "signal %s rejected (usage): %s", SanitizeLine(op.Kind), SanitizeLine(op.Reason))
		default:
			fmt.Fprintf(&sb, "signal %s accepted \xc2\xb7 %d bytes \xc2\xb7 %s", SanitizeLine(op.Kind), op.Size, SanitizeLine(op.Hash))
		}
	case hostop.OpDeltaReviewTrigger:
		// The Reason is the whole point of this op (issue #3246) and must show
		// even on the common "skip" path, so this mirrors the "decision" case
		// rather than the default arm's bare op name.
		if op.Reason != "" {
			fmt.Fprintf(&sb, "delta review %s: %s", SanitizeLine(op.Decision), SanitizeLine(op.Reason))
		} else {
			fmt.Fprintf(&sb, "delta review %s", SanitizeLine(op.Decision))
		}
	case hostop.OpRunStateError:
		// dispositions_budget (issue #2550 AC9) and decisions_budget (issue
		// #2695) are loud, non-fatal tripwires, not a run-state disk failure,
		// so each gets its own wording.
		switch op.Phase {
		case "dispositions_budget":
			fmt.Fprintf(&sb, "dispositions budget: %s", SanitizeLine(op.Error))
		case "decisions_budget":
			fmt.Fprintf(&sb, "decisions budget: %s", SanitizeLine(op.Error))
		default:
			fmt.Fprintf(&sb, "run-state %s failed: %s", SanitizeLine(op.Phase), SanitizeLine(op.Error))
		}
	default:
		sb.WriteString(SanitizeLine(op.Op))
	}
	return sb.String()
}
