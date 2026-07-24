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
// done. Uses a github-shaped tracker (AsNoLandingRecorder): the comment is
// assumed already posted in-box, matching production github research.
func TestResearchSettle_Recommend(t *testing.T) {
	fc := newResearchFake("42")
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: "42", Landing: "https://github.com/owner/repo/issues/42#issuecomment-1", Status: "recommend", Note: "grounded in code"},
	}

	s := NewResearchSettle(fc.AsNoLandingRecorder())
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

	s := NewResearchSettle(fc.AsNoLandingRecorder())
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

	s := NewResearchSettle(fc.AsNoLandingRecorder())
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

	s := NewResearchSettle(fc.AsNoLandingRecorder())
	out := testutil.CaptureStdout(t, func() {
		s.Settle(dispatch.NewFake(), "42", 0, result)
	})

	if strings.Contains(out, "status=recommend") {
		t.Errorf("stdout must not contain a success-style status line on CompleteVerdict error, got %q", out)
	}
	if !strings.Contains(out, "status=verdict-apply-failed") {
		t.Errorf("stdout must contain the error-branch marker, got %q", out)
	}
	if !strings.Contains(out, "label API down") {
		t.Errorf("stdout must contain the underlying error text, got %q", out)
	}
}

// TestResearchSettle_CompleteVerdictError_MissingInProgress verifies the
// same verdict-apply-failed handling on the realistic error path — an issue
// that has already been double-settled and lost its InProgress label —
// rather than only via an injected CompleteVerdictErr (#967).
func TestResearchSettle_CompleteVerdictError_MissingInProgress(t *testing.T) {
	fc := forge.NewFake(researchLabels)
	fc.VerdictLabels = researchVerdictLabels
	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{"agent-research-recommend"}})
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: "42", Landing: "https://github.com/owner/repo/issues/42#issuecomment-1", Status: "recommend", Note: "grounded in code"},
	}

	s := NewResearchSettle(fc.AsNoLandingRecorder())
	out := testutil.CaptureStdout(t, func() {
		s.Settle(dispatch.NewFake(), "42", 0, result)
	})

	if strings.Contains(out, "status=recommend") {
		t.Errorf("stdout must not contain a success-style status line on CompleteVerdict error, got %q", out)
	}
	if !strings.Contains(out, "status=verdict-apply-failed") {
		t.Errorf("stdout must contain the error-branch marker, got %q", out)
	}
}

// TestResearchSettle_Local_PostsCommentBlockThenVerdict verifies that for a
// tracker implementing LandingRecorder (local's shape, ADR 0032, issue
// #1692), Settle posts the extracted SPINDRIFT_COMMENT block via
// Comment(num, ...) before applying the verdict label — the host-mediated
// write channel a local Dispatch's Box cannot post from in-box.
func TestResearchSettle_Local_PostsCommentBlockThenVerdict(t *testing.T) {
	fc := newResearchFake("42")
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: "42", Landing: "none", Status: "recommend", Note: "grounded in code"},
		Comment:      "**Verdict** — recommend\n\n<!-- spindrift-research -->",
		CommentFound: true,
	}

	s := NewResearchSettle(fc)
	s.Settle(dispatch.NewFake(), "42", 0, result)

	if len(fc.CommentCalls) != 1 {
		t.Fatalf("want 1 comment posted, got %d", len(fc.CommentCalls))
	}
	if fc.CommentCalls[0].Num != "42" || fc.CommentCalls[0].Body != result.Comment {
		t.Errorf("unexpected comment call: %+v", fc.CommentCalls[0])
	}
	if len(fc.CompleteVerdictCalls) != 1 || fc.CompleteVerdictCalls[0].Verdict != forge.Recommend {
		t.Fatalf("want 1 CompleteVerdict(Recommend) call, got %+v", fc.CompleteVerdictCalls)
	}
}

// TestResearchSettle_Local_MissingCommentBlockTreatedAsBlocked verifies that
// a local Dispatch whose outcome line parses to a verdict but carries no
// complete SPINDRIFT_COMMENT block is treated the same as a missing verdict
// outcome: no comment posted, no verdict applied, transitioned to Failed.
func TestResearchSettle_Local_MissingCommentBlockTreatedAsBlocked(t *testing.T) {
	fc := newResearchFake("42")
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: "42", Landing: "none", Status: "recommend", Note: "grounded in code"},
		CommentFound: false,
	}

	s := NewResearchSettle(fc)
	s.Settle(dispatch.NewFake(), "42", 0, result)

	if len(fc.CommentCalls) != 0 {
		t.Errorf("want no comment posted, got %+v", fc.CommentCalls)
	}
	if len(fc.CompleteVerdictCalls) != 0 {
		t.Errorf("want no verdict applied, got %+v", fc.CompleteVerdictCalls)
	}
	if len(fc.TransitionStateCalls) != 1 {
		t.Fatalf("want 1 TransitionState call, got %d", len(fc.TransitionStateCalls))
	}
	call := fc.TransitionStateCalls[0]
	if call.Num != "42" || call.From != forge.InProgress || call.To != forge.Failed {
		t.Errorf("unexpected transition: %+v", call)
	}
}

// TestResearchSettle_Local_EmptyCommentBlockTreatedAsBlocked verifies that a
// complete but empty SPINDRIFT_COMMENT block (BEGIN immediately followed by
// END) is treated the same as a missing block: no empty comment posted, no
// verdict applied, transitioned to Failed. Guards against forge.Comment(num,
// "") ever landing on the issue as a vacuous "comment."
func TestResearchSettle_Local_EmptyCommentBlockTreatedAsBlocked(t *testing.T) {
	fc := newResearchFake("42")
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: "42", Landing: "none", Status: "recommend", Note: "grounded in code"},
		Comment:      "",
		CommentFound: true,
	}

	s := NewResearchSettle(fc)
	s.Settle(dispatch.NewFake(), "42", 0, result)

	if len(fc.CommentCalls) != 0 {
		t.Errorf("want no comment posted, got %+v", fc.CommentCalls)
	}
	if len(fc.CompleteVerdictCalls) != 0 {
		t.Errorf("want no verdict applied, got %+v", fc.CompleteVerdictCalls)
	}
	if len(fc.TransitionStateCalls) != 1 {
		t.Fatalf("want 1 TransitionState call, got %d", len(fc.TransitionStateCalls))
	}
}

// TestResearchSettle_Github_NeverPostsComment verifies that a tracker that
// does not implement LandingRecorder (github/jira's shape) never has
// Comment called by settle — that tracker's Box already posted its verdict
// comment in-box via gh issue comment.
func TestResearchSettle_Github_NeverPostsComment(t *testing.T) {
	fc := newResearchFake("42")
	ghLike := fc.AsNoLandingRecorder()
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: "42", Landing: "https://github.com/owner/repo/issues/42#issuecomment-1", Status: "recommend", Note: "grounded in code"},
	}

	s := NewResearchSettle(ghLike)
	s.Settle(dispatch.NewFake(), "42", 0, result)

	if len(fc.CommentCalls) != 0 {
		t.Errorf("want no comment posted for a github-shaped tracker, got %+v", fc.CommentCalls)
	}
	if len(fc.CompleteVerdictCalls) != 1 || fc.CompleteVerdictCalls[0].Verdict != forge.Recommend {
		t.Fatalf("want 1 CompleteVerdict(Recommend) call, got %+v", fc.CompleteVerdictCalls)
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

// TestResearchSettle_GithubReadOnly_PostsCommentBlockThenVerdict verifies
// that a github-shaped tracker (AsNoLandingRecorder) under
// BOX_FORGE_AND_ISSUE_ACCESS=read-only gets the same host-mediated
// SPINDRIFT_COMMENT relay local already gets (issue #1917) — the gate is
// driven by the read-only mode passed to NewResearchSettle, not by the
// LandingRecorder type-assertion TestResearchSettle_Github_NeverPostsComment
// exercises for the read-write default.
func TestResearchSettle_GithubReadOnly_PostsCommentBlockThenVerdict(t *testing.T) {
	fc := newResearchFake("42")
	ghLike := fc.AsNoLandingRecorder()
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: "42", Landing: "https://github.com/owner/repo/issues/42#issuecomment-1", Status: "recommend", Note: "grounded in code"},
		Comment:      "**Verdict** — recommend\n\n<!-- spindrift-research -->",
		CommentFound: true,
	}

	s := NewResearchSettleReadOnly(ghLike)
	s.Settle(dispatch.NewFake(), "42", 0, result)

	if len(fc.CommentCalls) != 1 {
		t.Fatalf("want 1 comment posted, got %d", len(fc.CommentCalls))
	}
	if fc.CommentCalls[0].Num != "42" || fc.CommentCalls[0].Body != result.Comment {
		t.Errorf("unexpected comment call: %+v", fc.CommentCalls[0])
	}
	if len(fc.CompleteVerdictCalls) != 1 || fc.CompleteVerdictCalls[0].Verdict != forge.Recommend {
		t.Fatalf("want 1 CompleteVerdict(Recommend) call, got %+v", fc.CompleteVerdictCalls)
	}
}

// TestResearchSettle_GithubReadOnly_MissingCommentBlockTreatedAsBlocked
// mirrors TestResearchSettle_Local_MissingCommentBlockTreatedAsBlocked for a
// github-shaped tracker in read-only mode: no silent success on a missing
// SPINDRIFT_COMMENT block (issue #1917 acceptance criterion 4).
func TestResearchSettle_GithubReadOnly_MissingCommentBlockTreatedAsBlocked(t *testing.T) {
	fc := newResearchFake("42")
	ghLike := fc.AsNoLandingRecorder()
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: "42", Landing: "https://github.com/owner/repo/issues/42#issuecomment-1", Status: "recommend", Note: "grounded in code"},
		CommentFound: false,
	}

	s := NewResearchSettleReadOnly(ghLike)
	s.Settle(dispatch.NewFake(), "42", 0, result)

	if len(fc.CommentCalls) != 0 {
		t.Errorf("want no comment posted, got %+v", fc.CommentCalls)
	}
	if len(fc.CompleteVerdictCalls) != 0 {
		t.Errorf("want no verdict applied, got %+v", fc.CompleteVerdictCalls)
	}
	if len(fc.TransitionStateCalls) != 1 {
		t.Fatalf("want 1 TransitionState call, got %d", len(fc.TransitionStateCalls))
	}
	call := fc.TransitionStateCalls[0]
	if call.Num != "42" || call.From != forge.InProgress || call.To != forge.Failed {
		t.Errorf("unexpected transition: %+v", call)
	}
}
