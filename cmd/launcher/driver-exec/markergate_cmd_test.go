package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// nudgeOut decodes runMarkerGate's --phase nudge stdout JSON envelope.
type nudgeOut struct {
	Prompt      string `json:"prompt"`
	ShouldNudge bool   `json:"should_nudge"`
}

// writeMarkerLog writes lines to a fresh temp file under t.TempDir and
// returns its path, standing in for the raw Driver log that
// outcome.LastPRIntentInLog scans -- --log-path's flag value in these
// tests.
func writeMarkerLog(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "driver.log")
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeMarkerLog: %v", err)
	}
	return path
}

// resolveOut decodes runMarkerGate's --phase resolve stdout JSON envelope,
// mirroring markergate.Resolution's own json tags.
type resolveOut struct {
	OpLine        string `json:"op_line"`
	OutcomeLine   string `json:"outcome_line"`
	ForceExitZero bool   `json:"force_exit_zero"`
}

// TestRunMarkerGate_NudgeOutcomeGeneric verifies --phase nudge --marker
// outcome with no --log-path (so no self-report line is ever found) renders
// the generic "marker absent" wording into the {"prompt":...} envelope and
// sets should_nudge=true.
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
	if !out.ShouldNudge {
		t.Fatalf("expected should_nudge=true, got %v (stdout=%q)", out.ShouldNudge, stdout.String())
	}
}

// TestRunMarkerGate_NudgeOutcomeGenericNonexistentLogPath verifies --phase
// nudge --marker outcome with --log-path pointing at a path that does not
// exist behaves the same as omitting --log-path entirely: generic wording,
// should_nudge=true.
func TestRunMarkerGate_NudgeOutcomeGenericNonexistentLogPath(t *testing.T) {
	var stdout bytes.Buffer
	rc := runMarkerGate([]string{
		"--phase", "nudge",
		"--marker", "outcome",
		"--log-path", filepath.Join(t.TempDir(), "does-not-exist.log"),
	}, &stdout)
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
	if !out.ShouldNudge {
		t.Fatalf("expected should_nudge=true, got %v (stdout=%q)", out.ShouldNudge, stdout.String())
	}
}

// TestRunMarkerGate_NudgeOutcomeNearMiss verifies --phase nudge --marker
// outcome with --log-path pointing at a file whose leading-token line fails
// to parse quotes the offending line, substitutes --issue/--landing into the
// example line, and sets should_nudge=true.
func TestRunMarkerGate_NudgeOutcomeNearMiss(t *testing.T) {
	logPath := writeMarkerLog(t, "SPINDRIFT_OUTCOME: done")
	var stdout bytes.Buffer
	rc := runMarkerGate([]string{
		"--phase", "nudge",
		"--marker", "outcome",
		"--log-path", logPath,
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
	if !out.ShouldNudge {
		t.Fatalf("expected should_nudge=true, got %v (stdout=%q)", out.ShouldNudge, stdout.String())
	}
}

// TestRunMarkerGate_NudgeOutcomeShouldNudgeFalseWhenValid verifies --phase
// nudge --marker outcome with --log-path pointing at a file whose leading
// line is a fully-parsed, valid SPINDRIFT_OUTCOME line sets
// should_nudge=false.
func TestRunMarkerGate_NudgeOutcomeShouldNudgeFalseWhenValid(t *testing.T) {
	logPath := writeMarkerLog(t, "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done")
	var stdout bytes.Buffer
	rc := runMarkerGate([]string{
		"--phase", "nudge",
		"--marker", "outcome",
		"--log-path", logPath,
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
	if out.ShouldNudge {
		t.Fatalf("expected should_nudge=false, got %v (stdout=%q)", out.ShouldNudge, stdout.String())
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

// TestRunMarkerGate_NudgePRIntentShouldNudgeTrueWhenAbsent verifies
// --phase nudge --marker pr-intent sets should_nudge=true when
// --original-outcome-line parses as status=ready and --log-path carries no
// genuine, --nonce-verified SPINDRIFT_PR_INTENT line (log omitted here).
func TestRunMarkerGate_NudgePRIntentShouldNudgeTrueWhenAbsent(t *testing.T) {
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
	if !out.ShouldNudge {
		t.Fatalf("expected should_nudge=true, got %v (stdout=%q)", out.ShouldNudge, stdout.String())
	}
}

// TestRunMarkerGate_NudgePRIntentShouldNudgeFalseWhenPresent verifies
// should_nudge=false when --log-path already carries a genuine,
// --nonce-verified SPINDRIFT_PR_INTENT line.
func TestRunMarkerGate_NudgePRIntentShouldNudgeFalseWhenPresent(t *testing.T) {
	logPath := writeMarkerLog(t, "SPINDRIFT_PR_INTENT abc123 dGVzdA==")
	var stdout bytes.Buffer
	rc := runMarkerGate([]string{
		"--phase", "nudge",
		"--marker", "pr-intent",
		"--nonce", "abc123",
		"--log-path", logPath,
		"--original-outcome-line", "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done",
	}, &stdout)
	if rc != 0 {
		t.Fatalf("runMarkerGate exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	var out nudgeOut
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout not valid JSON: %v (stdout=%q)", err, stdout.String())
	}
	if out.ShouldNudge {
		t.Fatalf("expected should_nudge=false, got %v (stdout=%q)", out.ShouldNudge, stdout.String())
	}
}

// TestRunMarkerGate_ResolvePRIntentEmptySetsOpLine verifies --phase resolve
// --marker pr-intent with no --log-path (so no SPINDRIFT_PR_INTENT line is
// ever found) sets a well-formed op_line (itself parseable JSON) carrying
// the right attempt count.
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

// TestRunMarkerGate_ResolvePRIntentPresentNoOpLine verifies a --log-path
// carrying a genuine, --nonce-verified SPINDRIFT_PR_INTENT line leaves
// op_line empty/absent.
func TestRunMarkerGate_ResolvePRIntentPresentNoOpLine(t *testing.T) {
	logPath := writeMarkerLog(t, "SPINDRIFT_PR_INTENT abc123 dGVzdA==")
	var stdout bytes.Buffer
	rc := runMarkerGate([]string{
		"--phase", "resolve",
		"--marker", "pr-intent",
		"--log-path", logPath,
		"--nonce", "abc123",
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
// empty --resumed-outcome-line plus a --resumed-driver-text-log containing a
// near-miss SPINDRIFT_OUTCOME-shaped line restores --original-outcome-line
// into outcome_line.
func TestRunMarkerGate_ResolveShadowedNearMissRestoresOriginal(t *testing.T) {
	logPath := writeMarkerLog(t, "SPINDRIFT_PR_INTENT abc123 dGVzdA==")
	driverTextLogPath := writeMarkerLog(t, "SPINDRIFT_OUTCOME: oops")
	var stdout bytes.Buffer
	rc := runMarkerGate([]string{
		"--phase", "resolve",
		"--marker", "pr-intent",
		"--log-path", logPath,
		"--nonce", "abc123",
		"--resumed-driver-text-log", driverTextLogPath,
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
// regardless of whether --resumed-driver-text-log contains a near-miss line.
func TestRunMarkerGate_ResolveGenuineResumedOutcomeNeverClobbered(t *testing.T) {
	cases := []struct {
		name                  string
		driverTextLogContents string
	}{
		{"no near-miss", ""},
		{"with near-miss", "SPINDRIFT_OUTCOME: garbled too"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logPath := writeMarkerLog(t, "SPINDRIFT_PR_INTENT abc123 dGVzdA==")
			var stdout bytes.Buffer
			args := []string{
				"--phase", "resolve",
				"--marker", "pr-intent",
				"--log-path", logPath,
				"--nonce", "abc123",
				"--resumed-outcome-line", "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked note=nope",
				"--original-outcome-line", "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done",
			}
			if c.driverTextLogContents != "" {
				driverTextLogPath := writeMarkerLog(t, c.driverTextLogContents)
				args = append(args, "--resumed-driver-text-log", driverTextLogPath)
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
			logPath := writeMarkerLog(t, "SPINDRIFT_PR_INTENT abc123 dGVzdA==")
			var stdout bytes.Buffer
			args := []string{
				"--phase", "resolve",
				"--marker", "pr-intent",
				"--log-path", logPath,
				"--nonce", "abc123",
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
