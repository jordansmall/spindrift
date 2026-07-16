package settle

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/testutil"
)

// researchLabels/researchVerdictLabels mirror ADR 0022's fixed research
// label family so these tests don't restate the label strings.
var researchLabels = forge.ResearchDispatchLabels()
var researchVerdictLabels = forge.ResearchVerdictLabels()

func newResearchFake(num string) *forge.Fake {
	fc := forge.NewFake(researchLabels)
	fc.VerdictLabels = researchVerdictLabels
	fc.SetIssue(forge.Issue{Number: num, Labels: []string{"agent-research-in-progress"}})
	return fc
}

// TestResearchSettle_Recommend verifies that a "recommend" verdict applies
// CompleteVerdict(Recommend) and performs no other transition — the one-shot
// settle path (ADR 0022): parse the outcome line, apply the verdict label,
// done.
func TestResearchSettle_Recommend(t *testing.T) {
	fc := newResearchFake("42")
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: "42", Landing: "https://github.com/owner/repo/issues/42#issuecomment-1", Status: "recommend", Note: "grounded in code"},
	}

	s := NewResearchSettle(fc)
	s.Settle(dispatch.NewFake(), "42", 0, result)

	if len(fc.CompleteVerdictCalls) != 1 {
		t.Fatalf("want 1 CompleteVerdict call, got %d", len(fc.CompleteVerdictCalls))
	}
	call := fc.CompleteVerdictCalls[0]
	if call.Num != "42" || call.Verdict != forge.Recommend {
		t.Errorf("unexpected call: %+v", call)
	}
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("verdict path must not call TransitionState; got %+v", fc.TransitionStateCalls)
	}
}

// TestResearchSettle_Reject verifies the reject verdict lands as
// CompleteVerdict(Reject) — Complete, never Failed (ADR 0022: a concluded
// false positive is not a malfunction).
func TestResearchSettle_Reject(t *testing.T) {
	fc := newResearchFake("7")
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: "7", Landing: "https://github.com/owner/repo/issues/7#issuecomment-2", Status: "reject", Note: "duplicate of #3"},
	}

	s := NewResearchSettle(fc)
	s.Settle(dispatch.NewFake(), "7", 0, result)

	if len(fc.CompleteVerdictCalls) != 1 || fc.CompleteVerdictCalls[0].Verdict != forge.Reject {
		t.Fatalf("want 1 CompleteVerdict(Reject) call, got %+v", fc.CompleteVerdictCalls)
	}
}

// TestResearchSettle_Unclear verifies the unclear verdict lands as
// CompleteVerdict(Unclear).
func TestResearchSettle_Unclear(t *testing.T) {
	fc := newResearchFake("8")
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: "8", Landing: "https://github.com/owner/repo/issues/8#issuecomment-3", Status: "unclear", Note: "needs answers"},
	}

	s := NewResearchSettle(fc)
	s.Settle(dispatch.NewFake(), "8", 0, result)

	if len(fc.CompleteVerdictCalls) != 1 || fc.CompleteVerdictCalls[0].Verdict != forge.Unclear {
		t.Fatalf("want 1 CompleteVerdict(Unclear) call, got %+v", fc.CompleteVerdictCalls)
	}
}

// TestResearchSettle_CompleteVerdictError verifies that a CompleteVerdict
// failure prints only the error line — no success-shaped landing=…
// status=… line follows a failed label application (#699).
func TestResearchSettle_CompleteVerdictError(t *testing.T) {
	fc := newResearchFake("42")
	fc.CompleteVerdictErr = errors.New("label API down")
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: "42", Landing: "https://github.com/owner/repo/issues/42#issuecomment-1", Status: "recommend", Note: "grounded in code"},
	}

	s := NewResearchSettle(fc)
	out := testutil.CaptureStdout(t, func() {
		s.Settle(dispatch.NewFake(), "42", 0, result)
	})

	if strings.Contains(out, "status=recommend") {
		t.Errorf("stdout must not contain a success-style status line on CompleteVerdict error, got %q", out)
	}
}

// TestResearchSettle_Blocked verifies a "blocked" outcome status transitions
// InProgress -> Failed (agent-research-failed) rather than applying a
// verdict label.
func TestResearchSettle_Blocked(t *testing.T) {
	fc := newResearchFake("9")
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: "9", Landing: "https://github.com/owner/repo/issues/9#issuecomment-4", Status: "blocked", Note: "push rejected"},
	}

	s := NewResearchSettle(fc)
	s.Settle(dispatch.NewFake(), "9", 0, result)

	if len(fc.CompleteVerdictCalls) != 0 {
		t.Errorf("blocked must not apply a verdict label; got %+v", fc.CompleteVerdictCalls)
	}
	if len(fc.TransitionStateCalls) != 1 {
		t.Fatalf("want 1 TransitionState call, got %d", len(fc.TransitionStateCalls))
	}
	call := fc.TransitionStateCalls[0]
	if call.Num != "9" || call.From != forge.InProgress || call.To != forge.Failed {
		t.Errorf("unexpected transition: %+v", call)
	}
}

// TestResearchSettle_MissingOutcome verifies a box that exited zero but left
// no outcome line transitions InProgress -> Failed, same as a malformed
// line — one-shot settle has no retry/adopt path to fall back to.
func TestResearchSettle_MissingOutcome(t *testing.T) {
	fc := newResearchFake("11")
	result := dispatch.Result{Success: true}

	s := NewResearchSettle(fc)
	s.Settle(dispatch.NewFake(), "11", 0, result)

	if len(fc.TransitionStateCalls) != 1 {
		t.Fatalf("want 1 TransitionState call, got %d", len(fc.TransitionStateCalls))
	}
	call := fc.TransitionStateCalls[0]
	if call.Num != "11" || call.From != forge.InProgress || call.To != forge.Failed {
		t.Errorf("unexpected transition: %+v", call)
	}
}
