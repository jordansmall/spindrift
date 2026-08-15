package main

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// nudgeOut decodes runMarkerGate's --phase nudge stdout JSON envelope.
type nudgeOut struct {
	Prompt string `json:"prompt"`
}

// resolveOut decodes runMarkerGate's --phase resolve stdout JSON envelope,
// mirroring markergate.Resolution's own json tags.
type resolveOut struct {
	OpLine        string `json:"op_line"`
	OutcomeLine   string `json:"outcome_line"`
	ForceExitZero bool   `json:"force_exit_zero"`
}

// TestRunMarkerGate_NudgeOutcomeGeneric verifies --phase nudge --marker
// outcome with no --near-miss-line renders the generic "marker absent"
// wording into the {"prompt":...} envelope.
func TestRunMarkerGate_NudgeOutcomeGeneric(t *testing.T) {
	var stdout bytes.Buffer
	rc := runMarkerGate([]string{"--phase", "nudge", "--marker", "outcome"}, &stdout)
	if rc != 0 {
		t.Fatalf("runMarkerGate exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	var out nudgeOut
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout not valid JSON: %v (stdout=%q)", err, stdout.String())
	}
	if !strings.Contains(out.Prompt, "The run ended without printing a SPINDRIFT_OUTCOME line") {
		t.Fatalf("expected generic outcome-absent wording, got %q", out.Prompt)
	}
}

// TestRunMarkerGate_NudgeOutcomeNearMiss verifies --phase nudge --marker
// outcome with --near-miss-line set quotes the offending line and
// substitutes --issue/--landing into the example line.
func TestRunMarkerGate_NudgeOutcomeNearMiss(t *testing.T) {
	var stdout bytes.Buffer
	rc := runMarkerGate([]string{
		"--phase", "nudge",
		"--marker", "outcome",
		"--near-miss-line", "SPINDRIFT_OUTCOME: done",
		"--issue", "7",
		"--landing", "agent/issue-7",
	}, &stdout)
	if rc != 0 {
		t.Fatalf("runMarkerGate exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	var out nudgeOut
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout not valid JSON: %v (stdout=%q)", err, stdout.String())
	}
	if !strings.Contains(out.Prompt, "SPINDRIFT_OUTCOME: done") {
		t.Fatalf("expected near-miss line quoted, got %q", out.Prompt)
	}
	if !strings.Contains(out.Prompt, "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=") {
		t.Fatalf("expected substituted issue/landing example line, got %q", out.Prompt)
	}
}

// TestRunMarkerGate_NudgePRIntent verifies --phase nudge --marker pr-intent
// embeds --nonce and --original-outcome-line into the rendered prompt.
func TestRunMarkerGate_NudgePRIntent(t *testing.T) {
	var stdout bytes.Buffer
	rc := runMarkerGate([]string{
		"--phase", "nudge",
		"--marker", "pr-intent",
		"--nonce", "abc123",
		"--original-outcome-line", "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done",
	}, &stdout)
	if rc != 0 {
		t.Fatalf("runMarkerGate exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	var out nudgeOut
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout not valid JSON: %v (stdout=%q)", err, stdout.String())
	}
	if !strings.Contains(out.Prompt, "abc123") {
		t.Fatalf("expected nonce embedded, got %q", out.Prompt)
	}
	if !strings.Contains(out.Prompt, "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done") {
		t.Fatalf("expected original outcome line embedded, got %q", out.Prompt)
	}
}

// TestRunMarkerGate_ResolvePRIntentEmptySetsOpLine verifies --phase resolve
// --marker pr-intent with an empty --pr-intent-line sets a well-formed
// op_line (itself parseable JSON) carrying the right attempt count.
func TestRunMarkerGate_ResolvePRIntentEmptySetsOpLine(t *testing.T) {
	var stdout bytes.Buffer
	rc := runMarkerGate([]string{
		"--phase", "resolve",
		"--marker", "pr-intent",
		"--attempts", "3",
	}, &stdout)
	if rc != 0 {
		t.Fatalf("runMarkerGate exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	var out resolveOut
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout not valid JSON: %v (stdout=%q)", err, stdout.String())
	}
	if out.OpLine == "" {
		t.Fatalf("expected non-empty op_line, got %q", stdout.String())
	}
	var opLineJSON map[string]any
	if err := json.Unmarshal([]byte(out.OpLine), &opLineJSON); err != nil {
		t.Fatalf("op_line is not itself well-formed JSON: %v (op_line=%q)", err, out.OpLine)
	}
	if !strings.Contains(out.OpLine, "after 3 attempt") {
		t.Fatalf("expected attempt count 3 in op_line, got %q", out.OpLine)
	}
}

// TestRunMarkerGate_ResolvePRIntentPresentNoOpLine verifies a non-empty
// --pr-intent-line leaves op_line empty/absent.
func TestRunMarkerGate_ResolvePRIntentPresentNoOpLine(t *testing.T) {
	var stdout bytes.Buffer
	rc := runMarkerGate([]string{
		"--phase", "resolve",
		"--marker", "pr-intent",
		"--pr-intent-line", "SPINDRIFT_PR_INTENT abc123 dGVzdA==",
	}, &stdout)
	if rc != 0 {
		t.Fatalf("runMarkerGate exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	var out resolveOut
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout not valid JSON: %v (stdout=%q)", err, stdout.String())
	}
	if out.OpLine != "" {
		t.Fatalf("expected empty op_line, got %q", out.OpLine)
	}
}

// TestRunMarkerGate_ResolveShadowedNearMissRestoresOriginal verifies an
// empty --resumed-outcome-line plus a set --resumed-near-miss-line restores
// --original-outcome-line into outcome_line.
func TestRunMarkerGate_ResolveShadowedNearMissRestoresOriginal(t *testing.T) {
	var stdout bytes.Buffer
	rc := runMarkerGate([]string{
		"--phase", "resolve",
		"--marker", "pr-intent",
		"--pr-intent-line", "SPINDRIFT_PR_INTENT abc123 dGVzdA==",
		"--resumed-near-miss-line", "SPINDRIFT_OUTCOME: oops",
		"--original-outcome-line", "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done",
	}, &stdout)
	if rc != 0 {
		t.Fatalf("runMarkerGate exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	var out resolveOut
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout not valid JSON: %v (stdout=%q)", err, stdout.String())
	}
	want := "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done"
	if out.OutcomeLine != want {
		t.Fatalf("outcome_line = %q, want %q", out.OutcomeLine, want)
	}
}

// TestRunMarkerGate_ResolveGenuineResumedOutcomeNeverClobbered verifies a
// non-empty --resumed-outcome-line leaves outcome_line empty/absent,
// regardless of --resumed-near-miss-line.
func TestRunMarkerGate_ResolveGenuineResumedOutcomeNeverClobbered(t *testing.T) {
	cases := []struct {
		name                string
		resumedNearMissLine string
	}{
		{"no near-miss", ""},
		{"with near-miss", "SPINDRIFT_OUTCOME: garbled too"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var stdout bytes.Buffer
			args := []string{
				"--phase", "resolve",
				"--marker", "pr-intent",
				"--pr-intent-line", "SPINDRIFT_PR_INTENT abc123 dGVzdA==",
				"--resumed-outcome-line", "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked note=nope",
				"--original-outcome-line", "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done",
			}
			if c.resumedNearMissLine != "" {
				args = append(args, "--resumed-near-miss-line", c.resumedNearMissLine)
			}
			rc := runMarkerGate(args, &stdout)
			if rc != 0 {
				t.Fatalf("runMarkerGate exit = %d, want 0 (stdout=%q)", rc, stdout.String())
			}

			var out resolveOut
			if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
				t.Fatalf("stdout not valid JSON: %v (stdout=%q)", err, stdout.String())
			}
			if out.OutcomeLine != "" {
				t.Fatalf("expected empty outcome_line, got %q", out.OutcomeLine)
			}
		})
	}
}

// TestRunMarkerGate_ResolveForceExitZero verifies force_exit_zero is true
// iff --outcome-via-backstop is set and --resume-exit-code is non-zero,
// covering every combination.
func TestRunMarkerGate_ResolveForceExitZero(t *testing.T) {
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
			var stdout bytes.Buffer
			args := []string{
				"--phase", "resolve",
				"--marker", "pr-intent",
				"--pr-intent-line", "SPINDRIFT_PR_INTENT abc123 dGVzdA==",
				"--resume-exit-code", strconv.Itoa(c.resumeExitCode),
			}
			if c.outcomeViaBackstop {
				args = append(args, "--outcome-via-backstop")
			}
			rc := runMarkerGate(args, &stdout)
			if rc != 0 {
				t.Fatalf("runMarkerGate exit = %d, want 0 (stdout=%q)", rc, stdout.String())
			}

			var out resolveOut
			if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
				t.Fatalf("stdout not valid JSON: %v (stdout=%q)", err, stdout.String())
			}
			if out.ForceExitZero != c.want {
				t.Fatalf("force_exit_zero = %v, want %v", out.ForceExitZero, c.want)
			}
		})
	}
}

// TestRunMarkerGate_ResolveOutcomeMarkerRejected verifies --phase resolve
// --marker outcome is an invalid combination (Resolve has no behavior for
// MarkerOutcome), exiting 1 with a clear stderr error rather than silently
// running an ill-defined resolution.
func TestRunMarkerGate_ResolveOutcomeMarkerRejected(t *testing.T) {
	var stdout bytes.Buffer
	rc := runMarkerGate([]string{"--phase", "resolve", "--marker", "outcome"}, &stdout)
	if rc == 0 {
		t.Fatalf("runMarkerGate exit = 0, want non-zero for -phase resolve -marker outcome")
	}
}

// TestRunMarkerGate_MissingRequiredFlagsReturnsNonZero verifies a missing
// -phase or -marker fails loudly (exit 1) instead of running against a
// zero-value Config.
func TestRunMarkerGate_MissingRequiredFlagsReturnsNonZero(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"missing phase", []string{"--marker", "outcome"}},
		{"missing marker", []string{"--phase", "nudge"}},
		{"missing both", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var stdout bytes.Buffer
			rc := runMarkerGate(c.args, &stdout)
			if rc == 0 {
				t.Fatalf("runMarkerGate exit = 0, want non-zero for %v", c.args)
			}
		})
	}
}

// TestIsMarkerGateInvocation verifies the marker-gate subcommand's dispatch
// guard: a bare "marker-gate" first arg selects it, while every other
// invocation shape falls through to a different path.
func TestIsMarkerGateInvocation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"marker-gate first arg", []string{"marker-gate", "--phase", "nudge"}, true},
		{"no args", nil, false},
		{"ordinary flag invocation", []string{"--driver", "claude"}, false},
		{"outcome-backstop", []string{"outcome-backstop"}, false},
	}
	for _, c := range cases {
		if got := isMarkerGateInvocation(c.args); got != c.want {
			t.Errorf("%s: isMarkerGateInvocation(%v) = %v, want %v", c.name, c.args, got, c.want)
		}
	}
}
