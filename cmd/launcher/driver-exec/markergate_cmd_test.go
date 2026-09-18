package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/testutil"
)

// nudgeOut decodes runMarkerGate's --phase nudge stdout JSON envelope.
type nudgeOut struct {
	Prompt      string `json:"prompt"`
	ShouldNudge bool   `json:"should_nudge"`
}

// writeMarkerLog writes a stand-in for the raw Driver log that
// outcome.LastPRIntentInLog scans, and returns the path these tests pass as
// --log-path.
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

// Omitting --log-path means no self-report line is ever found, so the gate
// must fall back to the generic wording.
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

// A --log-path that does not exist must behave exactly like omitting the flag,
// not like an error.
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

// The fixture line carries a colon after the marker token, so it leads with
// SPINDRIFT_OUTCOME but fails to parse. The gate must quote it back and
// substitute --issue/--landing into the example line.
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

// The test omits --log-path deliberately: a status=ready outcome with no
// nonce-verified SPINDRIFT_PR_INTENT line behind it still has to nudge.
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

// Omitting --log-path means no SPINDRIFT_PR_INTENT line is ever found. The
// op_line the gate then emits is a JSON string nested inside the envelope, so
// the test decodes it a second time.
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

// A near-miss line in the resumed driver log must not shadow the original
// outcome: with --resumed-outcome-line empty, the gate restores
// --original-outcome-line instead.
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

// A non-empty --resumed-outcome-line wins outright, so both cases below,
// near-miss present or not, must leave outcome_line empty.
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

// Resolve has no behavior for MarkerOutcome, so the combination must fail
// loudly rather than run an ill-defined resolution.
func TestRunMarkerGate_ResolveOutcomeMarkerRejected(t *testing.T) {
	var stdout bytes.Buffer
	rc := runMarkerGate([]string{"--phase", "resolve", "--marker", "outcome"}, &stdout)
	if rc == 0 {
		t.Fatalf("runMarkerGate exit = 0, want non-zero for -phase resolve -marker outcome")
	}
}

// A missing -phase or -marker must fail rather than run against a zero-value
// Config.
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

// A nonce that does not match is a spoof or corruption, not an absent marker,
// so the gate reports it on stderr. It still exits 0 and nudges, because the
// scan result cannot be trusted either way.
func TestRunMarkerGate_NudgePRIntentScanErrorReachesStderr(t *testing.T) {
	logPath := writeMarkerLog(t, "SPINDRIFT_PR_INTENT wrongnonce dGVzdA==")
	var stdout bytes.Buffer
	var rc int
	stderr := testutil.CaptureStderr(t, func() {
		rc = runMarkerGate([]string{
			"--phase", "nudge",
			"--marker", "pr-intent",
			"--nonce", "abc123",
			"--log-path", logPath,
			"--original-outcome-line", "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done",
		}, &stdout)
	})
	if rc != 0 {
		t.Fatalf("runMarkerGate exit = %d, want 0 (stdout=%q, stderr=%q)", rc, stdout.String(), stderr)
	}
	if !strings.Contains(stderr, logPath) {
		t.Fatalf("expected stderr to name scanned path %q, got %q", logPath, stderr)
	}
	if !strings.Contains(stderr, "SPINDRIFT_PR_INTENT") {
		t.Fatalf("expected stderr to name the PR-intent marker, got %q", stderr)
	}

	var out nudgeOut
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout not valid JSON: %v (stdout=%q)", err, stdout.String())
	}
	if !out.ShouldNudge {
		t.Fatalf("expected should_nudge=true (fail-safe), got %v (stdout=%q)", out.ShouldNudge, stdout.String())
	}
}

// Paired control for TestRunMarkerGate_NudgePRIntentScanErrorReachesStderr. A
// log with no PR-intent marker is a clean absent read, not a scan error, so it
// stays silent on stderr even though it nudges the same way.
func TestRunMarkerGate_NudgePRIntentAbsentMarkerNoStderr(t *testing.T) {
	logPath := writeMarkerLog(t, "some unrelated driver output")
	var stdout bytes.Buffer
	var rc int
	stderr := testutil.CaptureStderr(t, func() {
		rc = runMarkerGate([]string{
			"--phase", "nudge",
			"--marker", "pr-intent",
			"--nonce", "abc123",
			"--log-path", logPath,
			"--original-outcome-line", "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done",
		}, &stdout)
	})
	if rc != 0 {
		t.Fatalf("runMarkerGate exit = %d, want 0 (stdout=%q, stderr=%q)", rc, stdout.String(), stderr)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr for an absent marker, got %q", stderr)
	}

	var out nudgeOut
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout not valid JSON: %v (stdout=%q)", err, stdout.String())
	}
	if !out.ShouldNudge {
		t.Fatalf("expected should_nudge=true, got %v (stdout=%q)", out.ShouldNudge, stdout.String())
	}
}

// The resolve phase reports the same nonce-mismatch scan error as the nudge
// phase, and still exits 0 with a decodable envelope.
func TestRunMarkerGate_ResolvePRIntentScanErrorReachesStderr(t *testing.T) {
	logPath := writeMarkerLog(t, "SPINDRIFT_PR_INTENT wrongnonce dGVzdA==")
	var stdout bytes.Buffer
	var rc int
	stderr := testutil.CaptureStderr(t, func() {
		rc = runMarkerGate([]string{
			"--phase", "resolve",
			"--marker", "pr-intent",
			"--nonce", "abc123",
			"--log-path", logPath,
		}, &stdout)
	})
	if rc != 0 {
		t.Fatalf("runMarkerGate exit = %d, want 0 (stdout=%q, stderr=%q)", rc, stdout.String(), stderr)
	}
	if !strings.Contains(stderr, logPath) {
		t.Fatalf("expected stderr to name scanned path %q, got %q", logPath, stderr)
	}

	var out resolveOut
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout not valid JSON: %v (stdout=%q)", err, stdout.String())
	}
}

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
