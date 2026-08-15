package markergate

import (
	"strings"
	"testing"
)

func TestRenderNudgePrompt_OutcomeAbsent(t *testing.T) {
	got := RenderNudgePrompt(NudgeConfig{Marker: MarkerOutcome})
	want := "The run ended without printing a SPINDRIFT_OUTCOME line. Finish the workflow: run any remaining checks/gates in the foreground, then print the required SPINDRIFT_OUTCOME line as your final message."
	if got != want {
		t.Fatalf("RenderNudgePrompt() =\n%q\nwant\n%q", got, want)
	}
}

func TestRenderNudgePrompt_OutcomeNearMiss(t *testing.T) {
	got := RenderNudgePrompt(NudgeConfig{
		Marker:       MarkerOutcome,
		NearMissLine: "SPINDRIFT_OUTCOME: done",
		Issue:        "7",
		Landing:      "agent/issue-7",
	})
	if !strings.Contains(got, "SPINDRIFT_OUTCOME: done") {
		t.Fatalf("expected near-miss line quoted, got %q", got)
	}
	if !strings.Contains(got, "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=") {
		t.Fatalf("expected substituted issue/landing example line, got %q", got)
	}
	if !strings.Contains(got, "ready, blocked, or ambiguous") {
		t.Fatalf("expected Oxford-comma status prose, got %q", got)
	}
	// The generic grammar-restatement sentence keeps literal placeholder
	// tokens, distinct from the substituted "For this run" sentence.
	if !strings.Contains(got, "SPINDRIFT_OUTCOME issue=<issue> landing=<landing-ref> status=<status> note=<short reason>") {
		t.Fatalf("expected literal placeholder grammar sentence, got %q", got)
	}
	if !strings.Contains(got, "status=<status> note=<short reason> -- only fill in status and note") {
		t.Fatalf("expected substituted line to keep status/note placeholders literal, got %q", got)
	}
}

func TestRenderNudgePrompt_PRIntent(t *testing.T) {
	got := RenderNudgePrompt(NudgeConfig{
		Marker:              MarkerPRIntent,
		Nonce:               "abc123",
		OriginalOutcomeLine: "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done",
	})
	want := "Your last message ended with a status=ready SPINDRIFT_OUTCOME line but printed no SPINDRIFT_PR_INTENT line, so the launcher has no draft PR to open. Print exactly one SPINDRIFT_PR_INTENT line, grammar: SPINDRIFT_PR_INTENT abc123 <base64-encoded title, a blank line, then the body>, built by joining the PR title, a blank line, and the PR body, then base64-encoding the result into one unbroken token with no embedded newlines or spaces. Then repeat this exact line as your final message: SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done"
	if got != want {
		t.Fatalf("RenderNudgePrompt() =\n%q\nwant\n%q", got, want)
	}
}

func TestResolve_PRIntentEmptySetsOpLine(t *testing.T) {
	got := Resolve(ResolveConfig{Attempts: 1})
	want := `{"type":"spindrift_op","spindrift_op":{"op":"decision","decision":"stop","reason":"read-only PR-intent nudge exhausted after 1 attempt; no marker line, handing off blocked"}}`
	if got.OpLine != want {
		t.Fatalf("OpLine = %q, want %q", got.OpLine, want)
	}
}

func TestResolve_PRIntentEmptySetsOpLine_AttemptsSubstituted(t *testing.T) {
	got := Resolve(ResolveConfig{Attempts: 3})
	want := `{"type":"spindrift_op","spindrift_op":{"op":"decision","decision":"stop","reason":"read-only PR-intent nudge exhausted after 3 attempt; no marker line, handing off blocked"}}`
	if got.OpLine != want {
		t.Fatalf("OpLine = %q, want %q", got.OpLine, want)
	}
}

func TestResolve_PRIntentPresentNoOpLine(t *testing.T) {
	got := Resolve(ResolveConfig{Attempts: 1, PRIntentLine: "SPINDRIFT_PR_INTENT abc123 dGVzdA=="})
	if got.OpLine != "" {
		t.Fatalf("expected empty OpLine, got %q", got.OpLine)
	}
}

func TestResolve_ShadowedNearMissRestoresOriginal(t *testing.T) {
	got := Resolve(ResolveConfig{
		PRIntentLine:        "",
		ResumedOutcomeLine:  "",
		ResumedNearMissLine: "SPINDRIFT_OUTCOME: oops",
		OriginalOutcomeLine: "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done",
	})
	if got.OutcomeLine != "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done" {
		t.Fatalf("OutcomeLine = %q, want original outcome line", got.OutcomeLine)
	}
}

func TestResolve_NoNearMissNoOutcomeLine(t *testing.T) {
	got := Resolve(ResolveConfig{
		ResumedOutcomeLine:  "",
		ResumedNearMissLine: "",
		OriginalOutcomeLine: "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done",
	})
	if got.OutcomeLine != "" {
		t.Fatalf("expected empty OutcomeLine, got %q", got.OutcomeLine)
	}
}

func TestResolve_GenuineResumedOutcomeNeverClobbered(t *testing.T) {
	got := Resolve(ResolveConfig{
		ResumedOutcomeLine:  "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked note=nope",
		ResumedNearMissLine: "SPINDRIFT_OUTCOME: garbled too",
		OriginalOutcomeLine: "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done",
	})
	if got.OutcomeLine != "" {
		t.Fatalf("expected empty OutcomeLine when resume supplied its own genuine outcome, got %q", got.OutcomeLine)
	}
}

func TestResolve_ForceExitZero(t *testing.T) {
	cases := []struct {
		name               string
		outcomeViaBackstop bool
		resumeExitCode     int
		want               bool
	}{
		{"backstop+nonzero", true, 1, true},
		{"backstop+zero", true, 0, false},
		{"no-backstop+nonzero", false, 1, false},
		{"no-backstop+zero", false, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Resolve(ResolveConfig{OutcomeViaBackstop: c.outcomeViaBackstop, ResumeExitCode: c.resumeExitCode})
			if got.ForceExitZero != c.want {
				t.Fatalf("ForceExitZero = %v, want %v", got.ForceExitZero, c.want)
			}
		})
	}
}

func TestStatusProse(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"a"}, "a"},
		{[]string{"a", "b"}, "a or b"},
		{[]string{"a", "b", "c"}, "a, b, or c"},
		{[]string{"a", "b", "c", "d"}, "a, b, c, or d"},
	}
	for _, c := range cases {
		if got := statusProse(c.in); got != c.want {
			t.Fatalf("statusProse(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
