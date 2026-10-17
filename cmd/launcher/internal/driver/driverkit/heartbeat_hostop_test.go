package driverkit

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/landdelta"
	"spindrift.dev/launcher/internal/usage"
)

// A decision op renders its decision and reason together, and drops the
// trailing separator when the reason is empty (issue #2027).
func TestFormatSpindriftOpDecision(t *testing.T) {
	got := FormatSpindriftOp("7", hostop.SpindriftOp{Op: "decision", Decision: "stop", Reason: "max review rounds reached"})
	if !strings.Contains(got, "stop: max review rounds reached") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", got, "stop: max review rounds reached")
	}

	gotNoReason := FormatSpindriftOp("7", hostop.SpindriftOp{Op: "decision", Decision: "continue"})
	if !strings.Contains(gotNoReason, "continue") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", gotNoReason, "continue")
	}
	if strings.Contains(gotNoReason, ":") {
		t.Errorf("FormatSpindriftOp = %q, want no trailing separator when reason is empty", gotNoReason)
	}
}

// A usage-decision signal op renders its own reason rather than falling
// through to the default arm's accept rendering, which would drop the
// client's usage error entirely (issue #3867). It spells the line the same
// way the driver-exec verb's own printed line does -- "rejected (usage):" --
// so the host log and the agent-visible line never drift apart.
func TestFormatSpindriftOpSignalUsage(t *testing.T) {
	got := FormatSpindriftOp("7", hostop.SpindriftOp{Op: "signal", Kind: "pr-intent", Decision: "usage", Reason: "flag provided but not defined: -nonce"})
	want := "signal pr-intent rejected (usage): flag provided but not defined: -nonce"
	if !strings.Contains(got, want) {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", got, want)
	}
}

// A delta_review_trigger op renders its own Decision and Reason on both the
// fire and skip cases, rather than falling through to the default arm's bare
// op-name rendering (issue #3246).
func TestFormatSpindriftOpDeltaReviewTrigger(t *testing.T) {
	got := FormatSpindriftOp("7", hostop.SpindriftOp{Op: "delta_review_trigger", Decision: "fire", Reason: "land delta touches lines beyond the reviewer's findings: run.go:42"})
	if !strings.Contains(got, "fire: land delta touches lines beyond the reviewer's findings: run.go:42") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain the fire decision and reason", got)
	}

	gotSkip := FormatSpindriftOp("7", hostop.SpindriftOp{Op: "delta_review_trigger", Decision: "skip", Reason: "max slices reached"})
	if !strings.Contains(gotSkip, "skip: max slices reached") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain the skip decision and reason", gotSkip)
	}
}

// A run_state_error op renders its phase and error text (issue #2027).
func TestFormatSpindriftOpRunStateError(t *testing.T) {
	got := FormatSpindriftOp("7", hostop.SpindriftOp{Op: "run_state_error", Phase: "write", Error: "permission denied"})
	if !strings.Contains(got, "run-state write failed: permission denied") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", got, "run-state write failed: permission denied")
	}
}

// The "dispositions_budget" phase gets its own wording, not "run-state
// dispositions_budget failed": the tripwire is a loud but non-fatal budget
// notice, not a run-state read, write, or append failure (issue #2550 AC9).
func TestFormatSpindriftOpDispositionsBudget(t *testing.T) {
	got := FormatSpindriftOp("7", hostop.SpindriftOp{Op: "run_state_error", Phase: "dispositions_budget", Error: "round 1 mean 283.0/entry (ceiling 40), total 283 tokens (ceiling 400)"})
	if !strings.Contains(got, "dispositions budget: round 1 mean 283.0/entry (ceiling 40), total 283 tokens (ceiling 400)") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", got, "dispositions budget: round 1 mean 283.0/entry (ceiling 40), total 283 tokens (ceiling 400)")
	}
	if strings.Contains(got, "run-state dispositions_budget failed") {
		t.Errorf("FormatSpindriftOp = %q, must not render the budget tripwire as a run-state failure", got)
	}
}

// The implementor-side counterpart of TestFormatSpindriftOpDispositionsBudget:
// the "decisions_budget" phase gets its own wording, not "run-state
// decisions_budget failed" (issue #2695).
func TestFormatSpindriftOpDecisionsBudget(t *testing.T) {
	got := FormatSpindriftOp("7", hostop.SpindriftOp{Op: "run_state_error", Phase: "decisions_budget", Error: "round 1 mean 283.0/entry (ceiling 50), total 283 tokens (ceiling 400)"})
	if !strings.Contains(got, "decisions budget: round 1 mean 283.0/entry (ceiling 50), total 283 tokens (ceiling 400)") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", got, "decisions budget: round 1 mean 283.0/entry (ceiling 50), total 283 tokens (ceiling 400)")
	}
	if strings.Contains(got, "run-state decisions_budget failed") {
		t.Errorf("FormatSpindriftOp = %q, must not render the budget tripwire as a run-state failure", got)
	}
}

// A pass_no_outcome op names the last verdict seen inline, so an operator can
// tell a mid-turn cutoff after a BLOCK apart from one with no verdict at all
// (issue #2036).
func TestFormatSpindriftOpPassNoOutcome(t *testing.T) {
	got := FormatSpindriftOp("42", hostop.SpindriftOp{Op: "pass_no_outcome", Pass: 3, Verdict: "BLOCK", Reason: "exit 0"})
	if !strings.Contains(got, "pass 3 ended with no outcome") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", got, "pass 3 ended with no outcome")
	}
	if !strings.Contains(got, "BLOCK") {
		t.Errorf("FormatSpindriftOp = %q, want the last verdict named inline", got)
	}

	gotNoVerdict := FormatSpindriftOp("42", hostop.SpindriftOp{Op: "pass_no_outcome", Pass: 1, Reason: "exit 137"})
	if !strings.Contains(gotNoVerdict, "pass 1 ended with no outcome") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", gotNoVerdict, "pass 1 ended with no outcome")
	}
	if strings.Contains(gotNoVerdict, "last verdict") {
		t.Errorf("FormatSpindriftOp = %q, want no misleading 'last verdict' text when none was ever seen", gotNoVerdict)
	}
}

// A control character, newline, CSI/OSC escape, bidi control, or line separator
// in a decision's reason or a run_state_error's error text must not break the
// single-line row (issue #2027 AC: "Operation rows are sanitized to a single
// line").
func TestFormatSpindriftOpSanitizesDynamicFields(t *testing.T) {
	got := FormatSpindriftOp("42", hostop.SpindriftOp{Op: "run_state_error", Phase: "read", Error: "bad\x1b[2J\nfake-row"})
	if strings.Contains(got, "\n") {
		t.Errorf("FormatSpindriftOp = %q, want no embedded newline", got)
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("FormatSpindriftOp = %q, want no embedded escape sequence", got)
	}

	gotNoOutcome := FormatSpindriftOp("42", hostop.SpindriftOp{Op: "pass_no_outcome", Pass: 1, Verdict: "bad\x1b[2J\nfake-row", Reason: "bad\x1b[2J\nfake-row"})
	if strings.Contains(gotNoOutcome, "\n") {
		t.Errorf("FormatSpindriftOp = %q, want no embedded newline", gotNoOutcome)
	}
	if strings.Contains(gotNoOutcome, "\x1b") {
		t.Errorf("FormatSpindriftOp = %q, want no embedded escape sequence", gotNoOutcome)
	}

	gotBidi := FormatSpindriftOp("42", hostop.SpindriftOp{Op: "pass_no_outcome", Pass: 1, Verdict: "ok\u202eevil", Reason: "a\u2066b\u2028c"})
	const wantBidi = "#42 ○ pass 1 ended with no outcome (last verdict okevil, abc)"
	if gotBidi != wantBidi {
		t.Errorf("FormatSpindriftOp = %q, want %q", gotBidi, wantBidi)
	}
}

// A pass_start op renders as one status row carrying the issue tag and pass
// number (issue #2027).
func TestFormatSpindriftOpPassStart(t *testing.T) {
	got := FormatSpindriftOp("42", hostop.SpindriftOp{Op: "pass_start", Pass: 2})
	if !strings.HasPrefix(got, "#42 ") {
		t.Errorf("FormatSpindriftOp = %q, want it to start with issue tag %q", got, "#42 ")
	}
	if !strings.Contains(got, "pass 2 started") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", got, "pass 2 started")
	}
}

// A pass_start op names the pass's role (issue #2037) inline when Role is set,
// so #2027's telemetry can tell a code-owned review pass apart from an
// implement or fix pass. Unlike a legacy single-pass run, either may
// legitimately end with no SPINDRIFT_OUTCOME of its own.
func TestFormatSpindriftOpPassStartWithRole(t *testing.T) {
	got := FormatSpindriftOp("42", hostop.SpindriftOp{Op: "pass_start", Pass: 2, Role: "review"})
	if !strings.Contains(got, "pass 2 (review) started") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", got, "pass 2 (review) started")
	}
}

// A pass_usage op renders its pass number, role, and aggregate totals as one
// status row (issue #3156).
func TestFormatSpindriftOpPassUsage(t *testing.T) {
	got := FormatSpindriftOp("42", hostop.SpindriftOp{
		Op:   "pass_usage",
		Pass: 2,
		Role: "review",
		Usage: &hostop.PassUsage{
			APICalls:                 932,
			UncachedInputTokens:      1200,
			OutputTokens:             240000,
			CacheReadInputTokens:     65000000,
			CacheCreationInputTokens: 1200000,
			OutputIsMainLoopOnly:     true,
		},
	})
	if !strings.HasPrefix(got, "#42 ") {
		t.Errorf("FormatSpindriftOp = %q, want it to start with issue tag %q", got, "#42 ")
	}
	if !strings.Contains(got, "pass 2 (review) usage") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", got, "pass 2 (review) usage")
	}
	if !strings.Contains(got, "932") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain the API call count %d", got, 932)
	}
	if !strings.Contains(got, "65000000") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain the cache-read total %d", got, 65000000)
	}
	if !strings.Contains(got, "1200000") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain the cache-write total %d", got, 1200000)
	}
	if !strings.Contains(got, "240000") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain the output-token total %d", got, 240000)
	}
	if !strings.Contains(got, "out (main loop)") {
		t.Errorf("FormatSpindriftOp = %q, want the out column marked main-loop-only (issue #3213)", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("FormatSpindriftOp = %q, want a single line", got)
	}
}

// The "(main loop)" caveat comes from the payload's own flag, not a hardcoded
// string: a driver whose report carries whole-pass output (opencode) renders
// an unqualified out column, or the caveat would understate real data.
func TestFormatSpindriftOpPassUsagePlainOutWhenNotMainLoopOnly(t *testing.T) {
	got := FormatSpindriftOp("42", hostop.SpindriftOp{
		Op:   "pass_usage",
		Pass: 2,
		Usage: &hostop.PassUsage{
			APICalls:     7,
			OutputTokens: 4242,
		},
	})
	if !strings.Contains(got, "4242 out,") {
		t.Errorf("FormatSpindriftOp = %q, want an unqualified %q column", got, "4242 out,")
	}
	if strings.Contains(got, "main loop") {
		t.Errorf("FormatSpindriftOp = %q, want no main-loop-only caveat when the payload does not assert it", got)
	}
}

// The per-agent tail renders in the payload's given order, which
// UsageByAgent sets as main loop first then costliest subagent first,
// rather than re-sorting it.
func TestFormatSpindriftOpPassUsageAgentTail(t *testing.T) {
	got := FormatSpindriftOp("42", hostop.SpindriftOp{
		Op:   "pass_usage",
		Pass: 2,
		Usage: &hostop.PassUsage{
			Agents: []usage.AgentUsage{
				{Agent: usage.MainLoopAgent, UncachedInputTokens: 210},
				{Agent: "worker", UncachedInputTokens: 480},
				{Agent: "scout", UncachedInputTokens: 242},
			},
		},
	})
	iMain := strings.Index(got, "main")
	iWorker := strings.Index(got, "worker")
	iScout := strings.Index(got, "scout")
	if iMain == -1 || iWorker == -1 || iScout == -1 {
		t.Fatalf("FormatSpindriftOp = %q, want all three agent labels present", got)
	}
	if !(iMain < iWorker && iWorker < iScout) {
		t.Errorf("FormatSpindriftOp = %q, want agents rendered in given order main, worker, scout", got)
	}
}

// A pass_usage op with a nil Usage still renders a single line instead of
// panicking: the case of a pass that crashes or produces no usage events
// reaching the render path.
func TestFormatSpindriftOpPassUsageNilUsage(t *testing.T) {
	got := FormatSpindriftOp("42", hostop.SpindriftOp{Op: "pass_usage", Pass: 1})
	if !strings.Contains(got, "pass 1 usage") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", got, "pass 1 usage")
	}
	if strings.Contains(got, "\n") {
		t.Errorf("FormatSpindriftOp = %q, want a single line", got)
	}
}

// An agent name carrying a control character or newline is sanitized like
// every other dynamic field (issue #2027 AC): agent labels come from a
// subagent_type field in the Box's own stream, which is untrusted content.
func TestFormatSpindriftOpPassUsageSanitizesAgentNames(t *testing.T) {
	got := FormatSpindriftOp("42", hostop.SpindriftOp{
		Op:   "pass_usage",
		Pass: 1,
		Usage: &hostop.PassUsage{
			Agents: []usage.AgentUsage{
				{Agent: "bad\x1b[2J\nfake-row"},
			},
		},
	})
	if strings.Contains(got, "\n") {
		t.Errorf("FormatSpindriftOp = %q, want no embedded newline", got)
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("FormatSpindriftOp = %q, want no embedded escape sequence", got)
	}
}

// A land_delta op renders its counted, zero, and unknown cases as three
// distinct lines: zero is never simply omitted, and an unknown delta names its
// own Reason (issue #3244).
func TestFormatSpindriftOpLandDelta(t *testing.T) {
	counted := FormatSpindriftOp("42", hostop.SpindriftOp{
		Op:   "land_delta",
		Pass: 5,
		Delta: &landdelta.Delta{
			Known:      true,
			Files:      2,
			Insertions: 41,
			Deletions:  3,
		},
	})
	want := "#42 \xe2\x97\x8b " + (landdelta.Delta{Known: true, Files: 2, Insertions: 41, Deletions: 3}).Summary()
	if counted != want {
		t.Errorf("FormatSpindriftOp = %q, want %q (must match landdelta.Delta.Summary() -- issue #3244)", counted, want)
	}

	zero := FormatSpindriftOp("42", hostop.SpindriftOp{
		Op:    "land_delta",
		Pass:  5,
		Delta: &landdelta.Delta{Known: true},
	})
	if !strings.Contains(zero, "none") {
		t.Errorf("FormatSpindriftOp = %q, want the zero delta stated explicitly", zero)
	}

	unknown := FormatSpindriftOp("42", hostop.SpindriftOp{
		Op:    "land_delta",
		Pass:  5,
		Delta: &landdelta.Delta{Known: false, Reason: "no reviewed-commit anchor"},
	})
	if !strings.Contains(unknown, "unknown (no reviewed-commit anchor)") {
		t.Errorf("FormatSpindriftOp = %q, want the unknown delta to name its own Reason", unknown)
	}
}

// A land_delta op with a nil Delta degrades to a single line instead of
// panicking, mirroring TestFormatSpindriftOpPassUsageNilUsage.
func TestFormatSpindriftOpLandDeltaNilDelta(t *testing.T) {
	got := FormatSpindriftOp("42", hostop.SpindriftOp{Op: "land_delta", Pass: 5})
	if !strings.Contains(got, "unknown") {
		t.Errorf("FormatSpindriftOp = %q, want a nil Delta to degrade to the unknown case", got)
	}
	if strings.Contains(got, "()") {
		t.Errorf("FormatSpindriftOp = %q, want a stand-in reason, not an empty parenthesis", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("FormatSpindriftOp = %q, want a single line", got)
	}
}

// A Box-influenced Reason, such as one naming an unresolved base ref, is
// sanitized like every other dynamic field (issue #2027 AC).
func TestFormatSpindriftOpLandDeltaSanitizesReason(t *testing.T) {
	got := FormatSpindriftOp("42", hostop.SpindriftOp{
		Op:    "land_delta",
		Pass:  5,
		Delta: &landdelta.Delta{Known: false, Reason: "bad\x1b[2J\nfake-row"},
	})
	if strings.Contains(got, "\n") {
		t.Errorf("FormatSpindriftOp = %q, want no embedded newline", got)
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("FormatSpindriftOp = %q, want no embedded escape sequence", got)
	}
}

// A signal op renders its kind, byte count and content hash on accept, and its
// kind plus the reject reason on reject (issue #3724).
func TestFormatSpindriftOpSignal(t *testing.T) {
	got := FormatSpindriftOp("7", hostop.SpindriftOp{
		Op:       "signal",
		Kind:     "comment",
		Size:     412,
		Hash:     "sha256:abc123",
		Decision: "accept",
	})
	if !strings.Contains(got, "signal comment accepted \xc2\xb7 412 bytes \xc2\xb7 sha256:abc123") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain the accepted kind, size and hash", got)
	}

	gotReject := FormatSpindriftOp("7", hostop.SpindriftOp{
		Op:       "signal",
		Kind:     "comment",
		Size:     70000,
		Hash:     "sha256:def456",
		Decision: "reject",
		Reason:   "body exceeds the 65536-byte limit",
	})
	if !strings.Contains(gotReject, "signal comment rejected: body exceeds the 65536-byte limit") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain the rejected kind and reason", gotReject)
	}
	if strings.Contains(gotReject, "\n") {
		t.Errorf("FormatSpindriftOp = %q, want no embedded newline", gotReject)
	}

	gotRead := FormatSpindriftOp("7", hostop.SpindriftOp{
		Op:       "signal",
		Kind:     "status",
		Decision: "read",
	})
	if !strings.Contains(gotRead, "signal status read") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain the read disposition", gotRead)
	}
	if strings.Contains(gotRead, "bytes") || strings.Contains(gotRead, "sha256:") {
		t.Errorf("FormatSpindriftOp = %q, want no byte count or hash on a read", gotRead)
	}
}
