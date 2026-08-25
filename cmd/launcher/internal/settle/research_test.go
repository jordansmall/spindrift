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
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "42", Landing: "https://github.com/owner/repo/issues/42#issuecomment-1", Status: "recommend", Note: "grounded in code"},
		},
	}

	s := NewResearchSettle(fc.AsNoLandingRecorder(), researchVerdictLabels)
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
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "7", Landing: "https://github.com/owner/repo/issues/7#issuecomment-2", Status: "reject", Note: "duplicate of #3"},
		},
	}

	s := NewResearchSettle(fc.AsNoLandingRecorder(), researchVerdictLabels)
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
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "8", Landing: "https://github.com/owner/repo/issues/8#issuecomment-3", Status: "unclear", Note: "needs answers"},
		},
	}

	s := NewResearchSettle(fc.AsNoLandingRecorder(), researchVerdictLabels)
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
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "42", Landing: "https://github.com/owner/repo/issues/42#issuecomment-1", Status: "recommend", Note: "grounded in code"},
		},
	}

	s := NewResearchSettle(fc.AsNoLandingRecorder(), researchVerdictLabels)
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
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "42", Landing: "https://github.com/owner/repo/issues/42#issuecomment-1", Status: "recommend", Note: "grounded in code"},
		},
	}

	s := NewResearchSettle(fc.AsNoLandingRecorder(), researchVerdictLabels)
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
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "42", Landing: "none", Status: "recommend", Note: "grounded in code"},
		},
		Comment:      "**Verdict** — recommend\n\n<!-- spindrift-research -->",
		CommentFound: true,
	}

	s := NewResearchSettle(fc, researchVerdictLabels)
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
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "42", Landing: "none", Status: "recommend", Note: "grounded in code"},
		},
		CommentFound: false,
	}

	s := NewResearchSettle(fc, researchVerdictLabels)
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
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "42", Landing: "none", Status: "recommend", Note: "grounded in code"},
		},
		Comment:      "",
		CommentFound: true,
	}

	s := NewResearchSettle(fc, researchVerdictLabels)
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
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "42", Landing: "https://github.com/owner/repo/issues/42#issuecomment-1", Status: "recommend", Note: "grounded in code"},
		},
	}

	s := NewResearchSettle(ghLike, researchVerdictLabels)
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
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "9", Landing: "https://github.com/owner/repo/issues/9#issuecomment-4", Status: "blocked", Note: "push rejected"},
		},
	}

	s := NewResearchSettle(fc, researchVerdictLabels)
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

	s := NewResearchSettle(fc, researchVerdictLabels)
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
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "42", Landing: "https://github.com/owner/repo/issues/42#issuecomment-1", Status: "recommend", Note: "grounded in code"},
		},
		Comment:      "**Verdict** — recommend\n\n<!-- spindrift-research -->",
		CommentFound: true,
	}

	s := NewResearchSettleReadOnly(ghLike, researchVerdictLabels)
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
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "42", Landing: "https://github.com/owner/repo/issues/42#issuecomment-1", Status: "recommend", Note: "grounded in code"},
		},
		CommentFound: false,
	}

	s := NewResearchSettleReadOnly(ghLike, researchVerdictLabels)
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

// TestResearchSettle_GithubReadWrite_FilesIntentsAndLinksVerdictComment
// verifies the core new behavior (issue #2592): a relayed comment is honored
// in read-write mode too, not only local/read-only, and any relayed issue
// intents are filed before the comment is posted, so the posted comment can
// link the freshly filed issue's own URL.
func TestResearchSettle_GithubReadWrite_FilesIntentsAndLinksVerdictComment(t *testing.T) {
	fc := newResearchFake("42")
	ghLike := fc.AsIssueFiler()
	fc.PostIssueURL = "https://github.com/owner/repo/issues/501"
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "42", Landing: "https://github.com/owner/repo/issues/42#issuecomment-1", Status: "recommend", Note: "grounded in code"},
		},
		Comment:           "**Verdict** — recommend\n\n<!-- spindrift-research -->",
		CommentFound:      true,
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"fix(x): bug found during research","body":"repro steps"}`,
		},
	}

	s := NewResearchSettle(ghLike, researchVerdictLabels)
	s.Settle(dispatch.NewFake(), "42", 0, result)

	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("want 1 PostIssue call, got %d: %+v", len(fc.PostIssueCalls), fc.PostIssueCalls)
	}
	if len(fc.PostIssueCalls[0].Labels) != 1 || fc.PostIssueCalls[0].Labels[0] != "agent-research-finding" {
		t.Errorf("PostIssueCalls[0].Labels = %v, want [agent-research-finding]", fc.PostIssueCalls[0].Labels)
	}
	if !strings.Contains(fc.PostIssueCalls[0].Body, "Filed from research on #42") {
		t.Errorf("PostIssueCalls[0].Body = %q, want it to contain the backlink", fc.PostIssueCalls[0].Body)
	}

	if len(fc.CommentCalls) != 1 {
		t.Fatalf("want 1 comment posted, got %d", len(fc.CommentCalls))
	}
	body := fc.CommentCalls[0].Body
	if !strings.Contains(body, result.Comment) {
		t.Errorf("comment body = %q, want it to contain the original verdict comment", body)
	}
	if !strings.Contains(body, "## Filed issues") {
		t.Errorf("comment body = %q, want a Filed issues section", body)
	}
	if !strings.Contains(body, fc.PostIssueURL) {
		t.Errorf("comment body = %q, want it to link the filed issue's URL", body)
	}

	if len(fc.CompleteVerdictCalls) != 1 {
		t.Fatalf("want 1 CompleteVerdict call, got %d", len(fc.CompleteVerdictCalls))
	}
}

// TestResearchSettle_Local_FilesIntentsAndLinksVerdictComment verifies the
// same file-then-comment-then-label behavior as
// TestResearchSettle_GithubReadWrite_FilesIntentsAndLinksVerdictComment
// holds on the local branch too (r.landing != nil, issue #2592): a relayed
// SPINDRIFT_COMMENT is posted host-side with any relayed issue intents filed
// first, so the posted comment can link the freshly filed issue's own URL,
// even though local's own PostIssue/RecordLanding live on the same
// tracker.
func TestResearchSettle_Local_FilesIntentsAndLinksVerdictComment(t *testing.T) {
	fc := newResearchFake("42")
	localLike := fc.AsLocalIssueFiler()
	fc.PostIssueURL = "https://github.com/owner/repo/issues/501"
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "42", Landing: "https://github.com/owner/repo/issues/42#issuecomment-1", Status: "recommend", Note: "grounded in code"},
		},
		Comment:           "**Verdict** — recommend\n\n<!-- spindrift-research -->",
		CommentFound:      true,
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"fix(x): bug found during research","body":"repro steps"}`,
		},
	}

	s := NewResearchSettle(localLike, researchVerdictLabels)
	s.Settle(dispatch.NewFake(), "42", 0, result)

	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("want 1 PostIssue call, got %d: %+v", len(fc.PostIssueCalls), fc.PostIssueCalls)
	}
	if !strings.Contains(fc.PostIssueCalls[0].Body, "Filed from research on #42") {
		t.Errorf("PostIssueCalls[0].Body = %q, want it to contain the backlink", fc.PostIssueCalls[0].Body)
	}

	if len(fc.CommentCalls) != 1 {
		t.Fatalf("want 1 comment posted, got %d", len(fc.CommentCalls))
	}
	body := fc.CommentCalls[0].Body
	if !strings.Contains(body, result.Comment) {
		t.Errorf("comment body = %q, want it to contain the original verdict comment", body)
	}
	if !strings.Contains(body, "## Filed issues") {
		t.Errorf("comment body = %q, want a Filed issues section", body)
	}
	if !strings.Contains(body, fc.PostIssueURL) {
		t.Errorf("comment body = %q, want it to link the filed issue's URL", body)
	}

	if len(fc.CompleteVerdictCalls) != 1 {
		t.Fatalf("want 1 CompleteVerdict call, got %d", len(fc.CompleteVerdictCalls))
	}
}

// TestResearchSettle_FilingFailureDegradesInlineInComment verifies a filing
// failure never blocks the run: the failed intent degrades to an inline
// bullet in the posted comment (no link, since there is no URL) and the
// verdict label is still applied.
func TestResearchSettle_FilingFailureDegradesInlineInComment(t *testing.T) {
	fc := newResearchFake("42")
	fc.PostIssueErr = errors.New("create failed")
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "42", Landing: "https://github.com/owner/repo/issues/42#issuecomment-1", Status: "recommend", Note: "grounded in code"},
		},
		Comment:           "**Verdict** — recommend\n\n<!-- spindrift-research -->",
		CommentFound:      true,
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"fix(x): bug found during research","body":"detailed repro steps"}`,
		},
	}

	s := NewResearchSettle(fc.AsIssueFiler(), researchVerdictLabels)
	s.Settle(dispatch.NewFake(), "42", 0, result)

	if len(fc.CommentCalls) != 1 {
		t.Fatalf("want 1 comment posted, got %d", len(fc.CommentCalls))
	}
	body := fc.CommentCalls[0].Body
	if !strings.Contains(body, "fix(x): bug found during research") {
		t.Errorf("comment body = %q, want the failed intent's title", body)
	}
	if !strings.Contains(body, "filing failed") {
		t.Errorf("comment body = %q, want a filing-failed marker", body)
	}
	if !strings.Contains(body, "detailed repro steps") {
		t.Errorf("comment body = %q, want the failed intent's own summary", body)
	}
	if strings.Contains(body, "](https://") {
		t.Errorf("comment body = %q, want no linked URL for the failed intent", body)
	}
	if len(fc.CompleteVerdictCalls) != 1 {
		t.Fatalf("want 1 CompleteVerdict call despite the filing failure, got %d", len(fc.CompleteVerdictCalls))
	}
}

// TestResearchSettle_Local_CommentPostFailure_NeverAppliesVerdictLabel pins
// the file->comment->label ordering's comment-fails-blocks-label leg: a
// failed comment post must never be followed by CompleteVerdict.
func TestResearchSettle_Local_CommentPostFailure_NeverAppliesVerdictLabel(t *testing.T) {
	fc := newResearchFake("42")
	fc.CommentErr = errors.New("comment API down")
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "42", Landing: "none", Status: "recommend", Note: "grounded in code"},
		},
		Comment:      "**Verdict** — recommend\n\n<!-- spindrift-research -->",
		CommentFound: true,
	}

	s := NewResearchSettle(fc, researchVerdictLabels)
	s.Settle(dispatch.NewFake(), "42", 0, result)

	if len(fc.CommentCalls) != 1 {
		t.Fatalf("want 1 comment attempted, got %d", len(fc.CommentCalls))
	}
	if len(fc.CompleteVerdictCalls) != 0 {
		t.Errorf("want no verdict applied after a failed comment post, got %+v", fc.CompleteVerdictCalls)
	}
}

// TestResearchSettle_Local_NoIntentsNoCommentSection is a regression/no-op
// guard: when nothing was filed, the posted comment body is byte-for-byte
// the relayed verdict comment — no "## Filed issues" section appended.
func TestResearchSettle_Local_NoIntentsNoCommentSection(t *testing.T) {
	fc := newResearchFake("42")
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "42", Landing: "none", Status: "recommend", Note: "grounded in code"},
		},
		Comment:      "**Verdict** — recommend\n\n<!-- spindrift-research -->",
		CommentFound: true,
	}

	s := NewResearchSettle(fc, researchVerdictLabels)
	s.Settle(dispatch.NewFake(), "42", 0, result)

	if len(fc.CommentCalls) != 1 {
		t.Fatalf("want 1 comment posted, got %d", len(fc.CommentCalls))
	}
	if fc.CommentCalls[0].Body != result.Comment {
		t.Errorf("comment body = %q, want it unchanged when nothing was filed", fc.CommentCalls[0].Body)
	}
	if len(fc.CompleteVerdictCalls) != 1 {
		t.Fatalf("want 1 CompleteVerdict call, got %d", len(fc.CompleteVerdictCalls))
	}
}

// TestResearchSettle_GithubReadWrite_EmptyRelayedCommentIgnored verifies
// that a decodable-but-empty relayed comment (CommentFound=true,
// Comment=="") in read-write github mode does not fail the run: a verdict
// was already parsed above, so this must apply CompleteVerdict, not
// TransitionState-to-Failed — read-write mode never reached the
// found-but-empty check before ADR 0041 wired relayed filing/comment
// through this same branch, and empty must not regress into a newly
// reachable agent-research-failed (ADR 0041: agent-research-failed keeps
// meaning "no verdict was produced").
func TestResearchSettle_GithubReadWrite_EmptyRelayedCommentIgnored(t *testing.T) {
	fc := newResearchFake("42")
	ghLike := fc.AsNoLandingRecorder()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "42", Landing: "https://github.com/owner/repo/issues/42#issuecomment-1", Status: "recommend", Note: "grounded in code"},
		},
		Comment:      "",
		CommentFound: true,
	}

	s := NewResearchSettle(ghLike, researchVerdictLabels)
	s.Settle(dispatch.NewFake(), "42", 0, result)

	if len(fc.CommentCalls) != 0 {
		t.Errorf("want no comment posted for an empty relayed comment, got %+v", fc.CommentCalls)
	}
	if len(fc.CompleteVerdictCalls) != 1 || fc.CompleteVerdictCalls[0].Verdict != forge.Recommend {
		t.Fatalf("want 1 CompleteVerdict(Recommend) call despite the empty relayed comment, got %+v", fc.CompleteVerdictCalls)
	}
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("want no TransitionState call, got %+v", fc.TransitionStateCalls)
	}
}

// TestResearchSettle_CustomVerdictSet verifies that Settle validates the
// posted outcome's Status against the verdict set threaded into the
// constructor (ADR 0022, issue #2201's RESEARCH_VERDICTS override) rather
// than the compiled default: a custom "approve" token applies
// CompleteVerdict(Verdict("approve")).
func TestResearchSettle_CustomVerdictSet(t *testing.T) {
	custom := forge.NewVerdictLabels(
		forge.VerdictLabel{Verdict: "approve", Label: "agent-research-approve", Description: "x"},
		forge.VerdictLabel{Verdict: "skip", Label: "agent-research-skip", Description: "y"},
	)
	fc := forge.NewFake(researchLabels)
	fc.VerdictLabels = custom
	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{"agent-research-in-progress"}})
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "42", Landing: "https://github.com/owner/repo/issues/42#issuecomment-1", Status: "approve", Note: "looks good"},
		},
	}

	s := NewResearchSettle(fc.AsNoLandingRecorder(), custom)
	s.Settle(dispatch.NewFake(), "42", 0, result)

	if len(fc.CompleteVerdictCalls) != 1 {
		t.Fatalf("want 1 CompleteVerdict call, got %d", len(fc.CompleteVerdictCalls))
	}
	verdictCall := fc.CompleteVerdictCalls[0]
	if verdictCall.Num != "42" || verdictCall.Verdict != forge.Verdict("approve") {
		t.Errorf("unexpected call: %+v", verdictCall)
	}
}

// TestResearchSettle_CustomVerdictSet_DefaultTokenNotRecognized verifies the
// inverse: a compiled-default verdict token ("recommend") that is NOT part
// of the configured custom set fails to parse, taking the invalid-verdict
// path — no CompleteVerdict, transitioned to Failed — proving Settle
// validates against the configured set, not the hardcoded default.
func TestResearchSettle_CustomVerdictSet_DefaultTokenNotRecognized(t *testing.T) {
	custom := forge.NewVerdictLabels(
		forge.VerdictLabel{Verdict: "approve", Label: "agent-research-approve", Description: "x"},
		forge.VerdictLabel{Verdict: "skip", Label: "agent-research-skip", Description: "y"},
	)
	fc := forge.NewFake(researchLabels)
	fc.VerdictLabels = custom
	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{"agent-research-in-progress"}})
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "42", Landing: "https://github.com/owner/repo/issues/42#issuecomment-1", Status: "recommend", Note: "grounded in code"},
		},
	}

	s := NewResearchSettle(fc.AsNoLandingRecorder(), custom)
	s.Settle(dispatch.NewFake(), "42", 0, result)

	if len(fc.CompleteVerdictCalls) != 0 {
		t.Errorf("want no CompleteVerdict call for a token outside the configured set, got %+v", fc.CompleteVerdictCalls)
	}
	if len(fc.TransitionStateCalls) != 1 {
		t.Fatalf("want 1 TransitionState call, got %d", len(fc.TransitionStateCalls))
	}
	transitionCall := fc.TransitionStateCalls[0]
	if transitionCall.Num != "42" || transitionCall.From != forge.InProgress || transitionCall.To != forge.Failed {
		t.Errorf("unexpected transition: %+v", transitionCall)
	}
}

// TestBuildFiledIssuesSection_FailedBodyTruncatedToFirstLine verifies that a
// failed intent's inline bullet renders only the first line of its
// (potentially multi-line) body — an unescaped multi-line body would break
// out of the Markdown list item and inject arbitrary Markdown into the
// posted verdict comment.
func TestBuildFiledIssuesSection_FailedBodyTruncatedToFirstLine(t *testing.T) {
	filed := []filedIntent{
		{Title: "fix(x): bug", Failed: true, Body: "first line of repro\n\n## Heading\n```code fence```"},
	}

	got := buildFiledIssuesSection(filed)

	if !strings.Contains(got, "first line of repro") {
		t.Errorf("section = %q, want it to contain the body's first line", got)
	}
	if strings.Contains(got, "## Heading") || strings.Contains(got, "```code fence```") {
		t.Errorf("section = %q, want later body lines truncated away", got)
	}
}

// TestBuildFiledIssuesSection_TitleWithBracketEscaped verifies that a title
// containing `]` (agent-chosen, untrusted text) renders escaped rather than
// breaking the surrounding Markdown link/bullet syntax, for both a
// successful (linked) and a failed (inline) entry.
func TestBuildFiledIssuesSection_TitleWithBracketEscaped(t *testing.T) {
	filed := []filedIntent{
		{Title: "fix(x): [bad] title", URL: "https://github.com/owner/repo/issues/501"},
		{Title: "fix(y): [bad] title", Failed: true, Body: "repro"},
	}

	got := buildFiledIssuesSection(filed)

	if !strings.Contains(got, `\[bad\]`) {
		t.Errorf("section = %q, want the bracketed title escaped", got)
	}
	if strings.Contains(got, "[bad]") {
		t.Errorf("section = %q, want no unescaped bracketed title", got)
	}
}

// TestBuildFiledIssuesSection_NonHTTPURLDegradesToPlainBullet verifies that
// a non-http(s) URL (the local tracker's PostIssue returns "local:<slug>",
// not a URL) renders as a plain "title — url" bullet rather than a broken
// [title](local:slug) Markdown link.
func TestBuildFiledIssuesSection_NonHTTPURLDegradesToPlainBullet(t *testing.T) {
	filed := []filedIntent{
		{Title: "fix(x): bug", URL: "local:some-slug"},
	}

	got := buildFiledIssuesSection(filed)

	if strings.Contains(got, "](local:some-slug)") {
		t.Errorf("section = %q, want no Markdown link around the non-http URL", got)
	}
	if !strings.Contains(got, "local:some-slug") {
		t.Errorf("section = %q, want the local identifier still surfaced", got)
	}
}
