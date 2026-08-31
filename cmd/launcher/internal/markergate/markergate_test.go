package markergate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/outcome"
)

// writeLog writes contents to a temp log file and returns its path, for
// tests driving PR-intent presence/absence through the real log-scanning
// path rather than a stand-in string field.
func writeLog(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "driver.log")
	if contents != "" {
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatalf("writeLog: %v", err)
		}
	}
	return path
}

func TestRenderNudgePrompt_OutcomeAbsent(t *testing.T) {
	got := RenderNudgePrompt(NudgeConfig{Marker: MarkerOutcome})
	want := fmt.Sprintf(
		"The run ended without printing a %s line. Finish the workflow: run any remaining checks/gates in the foreground, then print the required %s line as your final message.",
		outcome.Token, outcome.Token,
	)
	if got != want {
		t.Fatalf("RenderNudgePrompt() =\n%q\nwant\n%q", got, want)
	}
}

func TestRenderNudgePrompt_OutcomeNearMiss(t *testing.T) {
	logPath := writeLog(t, outcome.Token+": done\n")
	got := RenderNudgePrompt(NudgeConfig{
		Marker:  MarkerOutcome,
		LogPath: logPath,
		Issue:   "7",
		Landing: "agent/issue-7",
	})
	if !strings.Contains(got, outcome.Token+": done") {
		t.Fatalf("expected near-miss line quoted, got %q", got)
	}
	if !strings.Contains(got, outcome.Token+" issue=7 landing=agent/issue-7 status=") {
		t.Fatalf("expected substituted issue/landing example line, got %q", got)
	}
	if !strings.Contains(got, "ready, blocked, or ambiguous") {
		t.Fatalf("expected Oxford-comma status prose, got %q", got)
	}
	// The generic grammar-restatement sentence keeps literal placeholder
	// tokens, distinct from the substituted "For this run" sentence.
	if !strings.Contains(got, outcome.Token+" issue=<issue> landing=<landing-ref> status=<status> note=<short reason>") {
		t.Fatalf("expected literal placeholder grammar sentence, got %q", got)
	}
	if !strings.Contains(got, "status=<status> note=<short reason> -- only fill in status and note") {
		t.Fatalf("expected substituted line to keep status/note placeholders literal, got %q", got)
	}
}

func TestRenderNudgePrompt_PRIntent(t *testing.T) {
	originalOutcomeLine := outcome.Token + " issue=7 landing=agent/issue-7 status=ready note=done"
	got := RenderNudgePrompt(NudgeConfig{
		Marker:              MarkerPRIntent,
		Nonce:               "abc123",
		OriginalOutcomeLine: originalOutcomeLine,
	})
	want := fmt.Sprintf(
		"Your last message ended with a status=ready %s line but printed no %s line, so the launcher has no draft PR to open. Print exactly one %s line, grammar: %s abc123 <base64-encoded title, a blank line, then the body>, built by joining the PR title, a blank line, and the PR body, then base64-encoding the result into one unbroken token with no embedded newlines or spaces. Then repeat this exact line as your final message: %s",
		outcome.Token, outcome.PRIntentToken, outcome.PRIntentToken, outcome.PRIntentToken, originalOutcomeLine,
	)
	if got != want {
		t.Fatalf("RenderNudgePrompt() =\n%q\nwant\n%q", got, want)
	}
}

func TestShouldNudgeOutcome_NoLogFile(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "missing.log")
	got := ShouldNudgeOutcome(NudgeConfig{LogPath: logPath})
	if !got {
		t.Fatalf("ShouldNudgeOutcome() = false, want true (log file does not exist)")
	}
}

func TestShouldNudgeOutcome_NearMissLine(t *testing.T) {
	logPath := writeLog(t, outcome.Token+": SUCCESS\n")
	got := ShouldNudgeOutcome(NudgeConfig{LogPath: logPath})
	if !got {
		t.Fatalf("ShouldNudgeOutcome() = false, want true (near-miss leading line, not fully parsed)")
	}
}

func TestShouldNudgeOutcome_FullyValidLine(t *testing.T) {
	logPath := writeLog(t, outcome.Token+" issue=7 landing=agent/issue-7 status=ready note=done\n")
	got := ShouldNudgeOutcome(NudgeConfig{LogPath: logPath})
	if got {
		t.Fatalf("ShouldNudgeOutcome() = true, want false (fully-parsed genuine outcome line already present)")
	}
}

// TestShouldNudgeOutcome_FieldedEmptyLandingDoesNotNudge pins review-round-2
// bug 1: the deleted bash gate only checked field-marker presence
// (landing=/status=, any value including empty), not outcome.Parse's full
// grammar validity, which rejects an empty landing as ErrNearMiss. A
// status=ready line with an empty landing value satisfied the deleted
// bash's fielded test and must not nudge here either.
func TestShouldNudgeOutcome_FieldedEmptyLandingDoesNotNudge(t *testing.T) {
	logPath := writeLog(t, outcome.Token+" issue=7 landing= status=ready note=done\n")
	got := ShouldNudgeOutcome(NudgeConfig{LogPath: logPath})
	if got {
		t.Fatalf("ShouldNudgeOutcome() = true, want false (fielded line present, even with empty landing)")
	}
}

// TestShouldNudgeOutcome_FieldedLineFollowedByLaterNonFieldedLineDoesNotNudge
// pins review-round-2 bug 2 (ordering): the deleted bash filtered to fielded
// lines FIRST, then took the last of those, so a later token-leading line
// carrying neither field marker (e.g. a bare "SPINDRIFT_OUTCOME: all set"
// paraphrase) never shadowed an earlier genuine fielded line. The prior Go
// implementation instead took the unconditional last token-leading line,
// which would wrongly flip this case to nudge.
func TestShouldNudgeOutcome_FieldedLineFollowedByLaterNonFieldedLineDoesNotNudge(t *testing.T) {
	logPath := writeLog(t, outcome.Token+" issue=7 landing=agent/issue-7 status=ready note=done\n"+outcome.Token+": all set\n")
	got := ShouldNudgeOutcome(NudgeConfig{LogPath: logPath})
	if got {
		t.Fatalf("ShouldNudgeOutcome() = true, want false (fielded line present before a later non-fielded token-leading line)")
	}
}

func TestShouldNudgePRIntent_ReadyNoPRIntentLine(t *testing.T) {
	logPath := writeLog(t, "")
	got := ShouldNudgePRIntent(NudgeConfig{
		Nonce:               "abc123",
		OriginalOutcomeLine: outcome.Token + " issue=7 landing=agent/issue-7 status=ready note=done",
		LogPath:             logPath,
	})
	if !got {
		t.Fatalf("ShouldNudgePRIntent() = false, want true (ready status, no PR-intent line)")
	}
}

func TestShouldNudgePRIntent_ReadyWithGenuinePRIntentLine(t *testing.T) {
	logPath := writeLog(t, outcome.PRIntentToken+" abc123 dGVzdA==\n")
	got := ShouldNudgePRIntent(NudgeConfig{
		Nonce:               "abc123",
		OriginalOutcomeLine: outcome.Token + " issue=7 landing=agent/issue-7 status=ready note=done",
		LogPath:             logPath,
	})
	if got {
		t.Fatalf("ShouldNudgePRIntent() = true, want false (genuine PR-intent line already present)")
	}
}

func TestShouldNudgePRIntent_NonReadyStatus(t *testing.T) {
	logPath := writeLog(t, "")
	got := ShouldNudgePRIntent(NudgeConfig{
		Nonce:               "abc123",
		OriginalOutcomeLine: outcome.Token + " issue=7 landing=agent/issue-7 status=blocked note=nope",
		LogPath:             logPath,
	})
	if got {
		t.Fatalf("ShouldNudgePRIntent() = true, want false (status is not ready)")
	}
}

// TestShouldNudgePRIntent_ReadyEmptyLanding pins review finding A: a
// status=ready outcome line with an empty landing field used to nudge under
// the deleted bash logic, but outcome.Parse rejects an empty landing as
// ErrNearMiss -- ShouldNudgePRIntent must use outcome.ReadyBeforeNote's
// looser substring test instead, so this case must still nudge.
func TestShouldNudgePRIntent_ReadyEmptyLanding(t *testing.T) {
	logPath := writeLog(t, "")
	got := ShouldNudgePRIntent(NudgeConfig{
		Nonce:               "abc123",
		OriginalOutcomeLine: outcome.Token + " issue=7 landing= status=ready note=done",
		LogPath:             logPath,
	})
	if !got {
		t.Fatalf("ShouldNudgePRIntent() = false, want true (status=ready before note, even with empty landing)")
	}
}

// TestShouldNudgePRIntent_StatusMentionOnlyInNote pins review finding B: a
// line with no real status= field, only a "status=ready" mention inside
// free-text note, must not nudge -- outcome.Parse's field extraction scans
// the whole line including note text and would wrongly nudge here.
func TestShouldNudgePRIntent_StatusMentionOnlyInNote(t *testing.T) {
	logPath := writeLog(t, "")
	got := ShouldNudgePRIntent(NudgeConfig{
		Nonce:               "abc123",
		OriginalOutcomeLine: outcome.Token + " issue=7 landing=x note=I set status=ready earlier",
		LogPath:             logPath,
	})
	if got {
		t.Fatalf("ShouldNudgePRIntent() = true, want false (status=ready appears only inside note text)")
	}
}

// TestShouldNudgePRIntent_MalformedPRIntentPayloadStillNudges pins the
// tightened presence rule (review round 3): a line leading with the
// PR-intent token and a matching nonce, but a payload that fails the
// strict base64 decode, no longer counts as present the way the deleted
// bash gate's looser "token + nonce + two fields" check did. It must nudge
// again rather than treat the malformed attempt as satisfying the gate.
func TestShouldNudgePRIntent_MalformedPRIntentPayloadStillNudges(t *testing.T) {
	logPath := writeLog(t, outcome.PRIntentToken+" abc123 not-valid-base64!!!\n")
	got := ShouldNudgePRIntent(NudgeConfig{
		Nonce:               "abc123",
		OriginalOutcomeLine: outcome.Token + " issue=7 landing=agent/issue-7 status=ready note=done",
		LogPath:             logPath,
	})
	if !got {
		t.Fatalf("ShouldNudgePRIntent() = false, want true (malformed base64 payload does not satisfy presence)")
	}
}

func TestShouldNudgePRIntent_MalformedOriginalOutcomeLine(t *testing.T) {
	logPath := writeLog(t, "")
	got := ShouldNudgePRIntent(NudgeConfig{
		Nonce:               "abc123",
		OriginalOutcomeLine: "",
		LogPath:             logPath,
	})
	if got {
		t.Fatalf("ShouldNudgePRIntent() = true, want false (empty/malformed OriginalOutcomeLine)")
	}
}

func TestResolve_PRIntentEmptySetsOpLine(t *testing.T) {
	got := Resolve(ResolveConfig{Attempts: 1})
	want := "{\"type\":\"spindrift_op\",\"spindrift_op\":{\"op\":\"decision\",\"decision\":\"stop\",\"reason\":\"read-only PR-intent nudge exhausted after 1 attempt; no marker line, handing off blocked\"}}\n"
	if got.OpLine != want {
		t.Fatalf("OpLine = %q, want %q", got.OpLine, want)
	}
}

func TestResolve_PRIntentEmptySetsOpLine_AttemptsSubstituted(t *testing.T) {
	got := Resolve(ResolveConfig{Attempts: 3})
	want := "{\"type\":\"spindrift_op\",\"spindrift_op\":{\"op\":\"decision\",\"decision\":\"stop\",\"reason\":\"read-only PR-intent nudge exhausted after 3 attempt; no marker line, handing off blocked\"}}\n"
	if got.OpLine != want {
		t.Fatalf("OpLine = %q, want %q", got.OpLine, want)
	}
}

// TestResolve_MalformedPRIntentPayloadEmitsOpLine mirrors
// TestShouldNudgePRIntent_MalformedPRIntentPayloadStillNudges for the
// resolve phase: a malformed payload must give up (OpLine set) rather than
// be mistaken for a genuine PR-intent line.
func TestResolve_MalformedPRIntentPayloadEmitsOpLine(t *testing.T) {
	logPath := writeLog(t, outcome.PRIntentToken+" abc123 not-valid-base64!!!\n")
	got := Resolve(ResolveConfig{Attempts: 1, LogPath: logPath, Nonce: "abc123"})
	if got.OpLine == "" {
		t.Fatalf("expected non-empty OpLine (malformed base64 payload does not satisfy presence)")
	}
}

func TestResolve_PRIntentPresentNoOpLine(t *testing.T) {
	logPath := writeLog(t, outcome.PRIntentToken+" abc123 dGVzdA==\n")
	got := Resolve(ResolveConfig{Attempts: 1, LogPath: logPath, Nonce: "abc123"})
	if got.OpLine != "" {
		t.Fatalf("expected empty OpLine, got %q", got.OpLine)
	}
}

func TestResolve_ShadowedNearMissRestoresOriginal(t *testing.T) {
	logPath := writeLog(t, outcome.Token+": oops\n")
	got := Resolve(ResolveConfig{
		ResumedOutcomeLine:       "",
		ResumedDriverTextLogPath: logPath,
		OriginalOutcomeLine:      "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done",
	})
	if got.OutcomeLine != "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done" {
		t.Fatalf("OutcomeLine = %q, want original outcome line", got.OutcomeLine)
	}
}

func TestResolve_NoNearMissNoOutcomeLine(t *testing.T) {
	logPath := writeLog(t, "")
	got := Resolve(ResolveConfig{
		ResumedOutcomeLine:       "",
		ResumedDriverTextLogPath: logPath,
		OriginalOutcomeLine:      "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done",
	})
	if got.OutcomeLine != "" {
		t.Fatalf("expected empty OutcomeLine, got %q", got.OutcomeLine)
	}
}

func TestResolve_GenuineResumedOutcomeNeverClobbered(t *testing.T) {
	logPath := writeLog(t, outcome.Token+": garbled too\n")
	got := Resolve(ResolveConfig{
		ResumedOutcomeLine:       "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked note=nope",
		ResumedDriverTextLogPath: logPath,
		OriginalOutcomeLine:      "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done",
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
