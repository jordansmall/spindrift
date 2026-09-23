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

// The vars below mirror ADR 0022's fixed research label family so the tests
// never restate the label strings.
var researchLabels = forge.ResearchDispatchLabels()
var researchVerdictLabels = forge.ResearchVerdictLabels()

func newResearchFake(num string) *forge.Fake {
	fc := forge.NewFake(researchLabels)
	fc.VerdictLabels = researchVerdictLabels
	fc.SetIssue(forge.Issue{Number: num, Labels: []string{"agent-research-in-progress"}})
	return fc
}

// A verdict causes no other transition: ADR 0022's one-shot settle path parses
// the outcome line, applies the label, and stops. The github-shaped tracker
// (AsNoLandingRecorder) assumes the Box already posted the comment in-box.
//
// This is also the research path's no-intents filed= tally fixture (issue
// #3608): a run that never filed anything still prints "filed=ok:0,failed:0,skipped:0",
// so it reads as "reached filing" rather than leaving no trace at all.
func TestResearchSettle_Recommend(t *testing.T) {
	fc := newResearchFake("42")
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "42", Landing: "https://github.com/owner/repo/issues/42#issuecomment-1", Status: "recommend", Note: "grounded in code"},
		},
	}

	s := NewResearchSettle(fc.AsNoLandingRecorder(), researchVerdictLabels, false)
	stdout := captureStdout(t, func() {
		s.Settle(dispatch.NewFake(), "42", 0, result)
	})

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
	if !strings.Contains(stdout, "    #42  filed=ok:0,failed:0,skipped:0\n") {
		t.Errorf("stdout = %q, want it to contain the zero filed= tally", stdout)
	}
}

// Reject completes and never fails (ADR 0022: a concluded false positive is
// not a malfunction).
func TestResearchSettle_Reject(t *testing.T) {
	fc := newResearchFake("7")
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "7", Landing: "https://github.com/owner/repo/issues/7#issuecomment-2", Status: "reject", Note: "duplicate of #3"},
		},
	}

	s := NewResearchSettle(fc.AsNoLandingRecorder(), researchVerdictLabels, false)
	s.Settle(dispatch.NewFake(), "7", 0, result)

	if len(fc.CompleteVerdictCalls) != 1 || fc.CompleteVerdictCalls[0].Verdict != forge.Reject {
		t.Fatalf("want 1 CompleteVerdict(Reject) call, got %+v", fc.CompleteVerdictCalls)
	}
}

func TestResearchSettle_Unclear(t *testing.T) {
	fc := newResearchFake("8")
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "8", Landing: "https://github.com/owner/repo/issues/8#issuecomment-3", Status: "unclear", Note: "needs answers"},
		},
	}

	s := NewResearchSettle(fc.AsNoLandingRecorder(), researchVerdictLabels, false)
	s.Settle(dispatch.NewFake(), "8", 0, result)

	if len(fc.CompleteVerdictCalls) != 1 || fc.CompleteVerdictCalls[0].Verdict != forge.Unclear {
		t.Fatalf("want 1 CompleteVerdict(Unclear) call, got %+v", fc.CompleteVerdictCalls)
	}
}

// A failed label application prints only the error line, never a
// success-shaped landing/status line after it (#699).
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

	s := NewResearchSettle(fc.AsNoLandingRecorder(), researchVerdictLabels, false)
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

// This pins the same verdict-apply-failed handling on the error path a real run
// hits, an issue already double-settled and missing its InProgress label, rather
// than only through an injected CompleteVerdictErr (#967).
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

	s := NewResearchSettle(fc.AsNoLandingRecorder(), researchVerdictLabels, false)
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

// For a tracker implementing LandingRecorder (local's shape, ADR 0032, issue
// #1692), Settle posts the SPINDRIFT_COMMENT block before applying the verdict
// label. A local Dispatch's Box cannot post that comment itself, so the host
// writes it.
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

	s := NewResearchSettle(fc, researchVerdictLabels, false)
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

// Settle treats a parsed verdict with no SPINDRIFT_COMMENT block the same as a
// missing verdict outcome, because nothing else would post the comment.
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

	s := NewResearchSettle(fc, researchVerdictLabels, false)
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

// Pins the existing "no verdict comment block" note (issue #3670): a
// zero-value CommentRejected means the Box never emitted a comment line at
// all, distinct from the malformed-payload case below.
func TestResearchSettle_Local_MissingCommentBlock_NotePinned(t *testing.T) {
	fc := newResearchFake("42")
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "42", Landing: "none", Status: "recommend", Note: "grounded in code"},
		},
		CommentFound: false,
	}

	s := NewResearchSettle(fc, researchVerdictLabels, false)
	out := testutil.CaptureStdout(t, func() {
		s.Settle(dispatch.NewFake(), "42", 0, result)
	})

	const want = "note=no verdict comment block"
	if !strings.Contains(out, want) {
		t.Errorf("stdout = %q, want substring %q", out, want)
	}
	if len(fc.TransitionStateCalls) != 1 {
		t.Fatalf("want 1 TransitionState call, got %d", len(fc.TransitionStateCalls))
	}
	call := fc.TransitionStateCalls[0]
	if call.Num != "42" || call.From != forge.InProgress || call.To != forge.Failed {
		t.Errorf("unexpected transition: %+v", call)
	}
}

// A comment line that arrived but failed to decode — the Box's Bash output
// cap truncating a base64 payload mid-stream — gets a failure note naming the
// cause and payload length, not the generic "no verdict comment block" that
// sends a human looking for a comment that was in fact sent (issue #3670).
func TestResearchSettle_Local_MalformedCommentBlock_NoteNamesCause(t *testing.T) {
	fc := newResearchFake("42")
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "42", Landing: "none", Status: "recommend", Note: "grounded in code"},
		},
		CommentFound:    false,
		CommentRejected: outcome.Rejections{Malformed: 1, LongestPayload: 8154},
	}

	s := NewResearchSettle(fc, researchVerdictLabels, false)
	out := testutil.CaptureStdout(t, func() {
		s.Settle(dispatch.NewFake(), "42", 0, result)
	})

	const want = "note=verdict comment block found but unreadable: malformed payload, longest base64 run 8154 chars (a payload longer than the Box's Bash output cap arrives truncated)"
	if !strings.Contains(out, want) {
		t.Errorf("stdout = %q, want substring %q", out, want)
	}
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

// A complete but empty SPINDRIFT_COMMENT block counts as missing, so that
// forge.Comment(num, "") never lands an empty comment on the issue.
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

	s := NewResearchSettle(fc, researchVerdictLabels, false)
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

// Settle never calls Comment on a tracker that does not implement
// LandingRecorder (github/jira's shape), because that Box already posted the
// verdict comment in-box.
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

	s := NewResearchSettle(ghLike, researchVerdictLabels, false)
	s.Settle(dispatch.NewFake(), "42", 0, result)

	if len(fc.CommentCalls) != 0 {
		t.Errorf("want no comment posted for a github-shaped tracker, got %+v", fc.CommentCalls)
	}
	if len(fc.CompleteVerdictCalls) != 1 || fc.CompleteVerdictCalls[0].Verdict != forge.Recommend {
		t.Fatalf("want 1 CompleteVerdict(Recommend) call, got %+v", fc.CompleteVerdictCalls)
	}
}

// Issue #2593: a github tracker, read-write, with the Filer provisioned forces
// researchForceRelay, so the Box relays the comment instead of posting it. That
// combination (landing == nil, readOnly == false) used to fall through the
// verdict-loss guard and apply CompleteVerdict with no comment ever posted.
func TestResearchSettle_GithubReadWriteFilerEnabled_MissingCommentBlockTreatedAsBlocked(t *testing.T) {
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

	s := NewResearchSettle(ghLike, researchVerdictLabels, true)
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

func TestResearchSettle_Blocked(t *testing.T) {
	fc := newResearchFake("9")
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "9", Landing: "https://github.com/owner/repo/issues/9#issuecomment-4", Status: "blocked", Note: "push rejected"},
		},
	}

	s := NewResearchSettle(fc, researchVerdictLabels, false)
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

// A box that exited zero but left no outcome line fails like a malformed one,
// because one-shot settle has no retry or adopt path to fall back to.
func TestResearchSettle_MissingOutcome(t *testing.T) {
	fc := newResearchFake("11")
	result := dispatch.Result{Success: true}

	s := NewResearchSettle(fc, researchVerdictLabels, false)
	s.Settle(dispatch.NewFake(), "11", 0, result)

	if len(fc.TransitionStateCalls) != 1 {
		t.Fatalf("want 1 TransitionState call, got %d", len(fc.TransitionStateCalls))
	}
	call := fc.TransitionStateCalls[0]
	if call.Num != "11" || call.From != forge.InProgress || call.To != forge.Failed {
		t.Errorf("unexpected transition: %+v", call)
	}
}

// A github-shaped tracker under BOX_FORGE_AND_ISSUE_ACCESS=read-only gets the
// same host-mediated comment relay local gets (issue #1917). The read-only mode
// passed to the constructor drives the gate, not the LandingRecorder type
// assertion TestResearchSettle_Github_NeverPostsComment covers.
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

	s := NewResearchSettleReadOnly(ghLike, researchVerdictLabels, false)
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

// Settle must not silently succeed on a missing SPINDRIFT_COMMENT block for a
// github-shaped tracker in read-only mode (issue #1917 acceptance criterion 4).
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

	s := NewResearchSettleReadOnly(ghLike, researchVerdictLabels, false)
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

// Issue #2592: read-write mode honors a relayed comment too, not just
// local/read-only, and files relayed issue intents before posting the comment
// so the comment can link the freshly filed issue's URL.
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

	s := NewResearchSettle(ghLike, researchVerdictLabels, false)
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

// The same file-then-comment-then-label order holds on the local branch
// (r.landing != nil, issue #2592), even though local's PostIssue and
// RecordLanding live on the same tracker.
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

	s := NewResearchSettle(localLike, researchVerdictLabels, false)
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

// A filing failure never blocks the run: the failed intent degrades to an
// inline bullet with no link, and Settle still applies the verdict label.
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

	s := NewResearchSettle(fc.AsIssueFiler(), researchVerdictLabels, false)
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

// This pins the comment-blocks-label leg of the file, comment, label order:
// Settle must never call CompleteVerdict after a failed comment post.
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

	s := NewResearchSettle(fc, researchVerdictLabels, false)
	s.Settle(dispatch.NewFake(), "42", 0, result)

	if len(fc.CommentCalls) != 1 {
		t.Fatalf("want 1 comment attempted, got %d", len(fc.CommentCalls))
	}
	if len(fc.CompleteVerdictCalls) != 0 {
		t.Errorf("want no verdict applied after a failed comment post, got %+v", fc.CompleteVerdictCalls)
	}
}

// When nothing was filed, the posted body stays byte-for-byte the relayed
// verdict comment, with no Filed issues section appended.
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

	s := NewResearchSettle(fc, researchVerdictLabels, false)
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

// Settle still applies the already-parsed verdict when a relayed comment in
// read-write github mode is empty. Read-write never reached the found-but-empty
// check before ADR 0041 routed relayed filing and comments through this branch, and
// agent-research-failed must keep meaning "no verdict was produced".
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

	s := NewResearchSettle(ghLike, researchVerdictLabels, false)
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

// Settle validates Status against the verdict set passed to the constructor
// (ADR 0022, issue #2201's RESEARCH_VERDICTS override), not the compiled
// default.
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

	s := NewResearchSettle(fc.AsNoLandingRecorder(), custom, false)
	s.Settle(dispatch.NewFake(), "42", 0, result)

	if len(fc.CompleteVerdictCalls) != 1 {
		t.Fatalf("want 1 CompleteVerdict call, got %d", len(fc.CompleteVerdictCalls))
	}
	verdictCall := fc.CompleteVerdictCalls[0]
	if verdictCall.Num != "42" || verdictCall.Verdict != forge.Verdict("approve") {
		t.Errorf("unexpected call: %+v", verdictCall)
	}
}

// The inverse: a compiled-default token outside the configured set takes the
// invalid-verdict path, which proves Settle validates against the configured
// set rather than the hardcoded default.
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

	s := NewResearchSettle(fc.AsNoLandingRecorder(), custom, false)
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

// A failed intent's bullet renders only the body's first line. An unescaped
// multi-line body would break out of the Markdown list item and inject
// arbitrary Markdown into the posted verdict comment.
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

// A title is agent-chosen, untrusted text, so a bracket in it renders escaped
// instead of breaking the surrounding Markdown link. The fixture holds both a
// linked and a failed entry because they render through different paths.
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

// The local tracker's PostIssue returns "local:<slug>" rather than a URL, so a
// non-http identifier renders as a plain bullet instead of a broken Markdown
// link.
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

// The research path's filed= tally line counts two successfully filed
// intents as ok:2,failed:0,skipped:0 (issue #3608), same shape as the work path's
// tally.
func TestResearchSettle_ReportsFiledTally_TwoOK(t *testing.T) {
	fc := newResearchFake("42")
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
			`{"title":"first bug","body":"first body"}`,
			`{"title":"second bug","body":"second body"}`,
		},
	}

	s := NewResearchSettle(fc.AsIssueFiler(), researchVerdictLabels, false)
	stdout := captureStdout(t, func() {
		s.Settle(dispatch.NewFake(), "42", 0, result)
	})

	if !strings.Contains(stdout, "    #42  filed=ok:2,failed:0,skipped:0\n") {
		t.Errorf("stdout = %q, want it to contain the all-ok filed= tally", stdout)
	}
}

// The research path's filed= tally line counts two failed PostIssue calls as
// ok:0,failed:2,skipped:0 (issue #3608): the tally must print even though both
// filings failed and the run still goes on to post the comment and apply
// the verdict label.
func TestResearchSettle_ReportsFiledTally_TwoFailed(t *testing.T) {
	fc := newResearchFake("42")
	fc.PostIssueErr = errFake
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
			`{"title":"first bug","body":"first body"}`,
			`{"title":"second bug","body":"second body"}`,
		},
	}

	s := NewResearchSettle(fc.AsIssueFiler(), researchVerdictLabels, false)
	stdout := captureStdout(t, func() {
		s.Settle(dispatch.NewFake(), "42", 0, result)
	})

	if !strings.Contains(stdout, "    #42  filed=ok:0,failed:2,skipped:0\n") {
		t.Errorf("stdout = %q, want it to contain the all-failed filed= tally", stdout)
	}
}

// End to end: every issue-intent in the run dedups against the open
// backlog, so the posted verdict comment must still carry a Skipped
// (deduplicated) section naming the matched issue -- otherwise an all-dedup
// research run reads as a bare verdict with no trace that anything was even
// found (issue #3811).
func TestResearchSettle_AllDedupedAgainstBacklog_CommentCarriesSkippedSection(t *testing.T) {
	fc := newResearchFake("42")
	fc.SetIssue(forge.Issue{
		Number: "501",
		Title:  "An earlier finding of the same bug",
		Body:   "earlier body\n\n<!-- spindrift-dedup: race in settle -->",
		Labels: []string{"agent-research-finding"},
	})
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
			`{"title":"a retry of the same finding","body":"new body","dedupTerms":["Race in Settle"]}`,
		},
	}

	s := NewResearchSettle(fc.AsIssueFiler(), researchVerdictLabels, false)
	s.Settle(dispatch.NewFake(), "42", 0, result)

	if len(fc.PostIssueCalls) != 0 {
		t.Fatalf("PostIssueCalls = %+v, want none (the sole intent dedups)", fc.PostIssueCalls)
	}
	if len(fc.CommentCalls) != 1 {
		t.Fatalf("want 1 comment posted, got %d", len(fc.CommentCalls))
	}
	body := fc.CommentCalls[0].Body
	if !strings.Contains(body, "## Skipped (deduplicated)") {
		t.Errorf("comment body = %q, want a Skipped (deduplicated) section", body)
	}
	if !strings.Contains(body, "#501") {
		t.Errorf("comment body = %q, want it to name the matched backlog issue", body)
	}
	if strings.Contains(body, "## Filed issues") {
		t.Errorf("comment body = %q, want no Filed issues section (nothing was filed)", body)
	}

	if len(fc.CompleteVerdictCalls) != 1 {
		t.Fatalf("want 1 CompleteVerdict call, got %d", len(fc.CompleteVerdictCalls))
	}
}

// Mixed end to end: one intent files clean, a second dedups against the open
// backlog. The posted comment must carry both sections, not just the one the
// simpler all-filed or all-skipped fixtures exercise (issue #3811).
func TestResearchSettle_MixedFiledAndDeduped_CommentCarriesBothSections(t *testing.T) {
	fc := newResearchFake("42")
	fc.SetIssue(forge.Issue{
		Number: "501",
		Title:  "An earlier finding of the same bug",
		Body:   "earlier body\n\n<!-- spindrift-dedup: race in settle -->",
		Labels: []string{"agent-research-finding"},
	})
	fc.PostIssueURL = "https://github.com/owner/repo/issues/900"
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
			`{"title":"a genuinely new finding","body":"new body","dedupTerms":["a wholly different site"]}`,
			`{"title":"a retry of the same finding","body":"other body","dedupTerms":["Race in Settle"]}`,
		},
	}

	s := NewResearchSettle(fc.AsIssueFiler(), researchVerdictLabels, false)
	s.Settle(dispatch.NewFake(), "42", 0, result)

	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("PostIssueCalls = %+v, want exactly 1 (the second dedups)", fc.PostIssueCalls)
	}
	if len(fc.CommentCalls) != 1 {
		t.Fatalf("want 1 comment posted, got %d", len(fc.CommentCalls))
	}
	body := fc.CommentCalls[0].Body
	if !strings.Contains(body, "## Filed issues") || !strings.Contains(body, fc.PostIssueURL) {
		t.Errorf("comment body = %q, want a Filed issues section linking the filed URL", body)
	}
	if !strings.Contains(body, "## Skipped (deduplicated)") || !strings.Contains(body, "#501") {
		t.Errorf("comment body = %q, want a Skipped (deduplicated) section naming #501", body)
	}

	if len(fc.CompleteVerdictCalls) != 1 {
		t.Fatalf("want 1 CompleteVerdict call, got %d", len(fc.CompleteVerdictCalls))
	}
}

// An all-skipped filed list renders only the skipped-deduplicated section,
// naming each title and its dedup reference, never the Filed issues heading
// (nothing was actually filed).
func TestBuildSkippedIssuesSection_AllSkipped(t *testing.T) {
	filed := []filedIntent{
		{Title: "fix(x): bug", Skipped: true, DupRef: "#123"},
		{Title: "fix(y): other bug", Skipped: true, DupRef: `this run's "fix(y): other bug"`},
	}

	got := buildSkippedIssuesSection(filed)

	if !strings.Contains(got, "## Skipped (deduplicated)") {
		t.Errorf("section = %q, want the skipped heading", got)
	}
	if !strings.Contains(got, "fix(x): bug") || !strings.Contains(got, "#123") {
		t.Errorf("section = %q, want the first title and its reference", got)
	}
	if !strings.Contains(got, "fix(y): other bug") {
		t.Errorf("section = %q, want the second title", got)
	}
	if strings.Contains(got, "## Filed issues") {
		t.Errorf("section = %q, want no Filed issues heading", got)
	}
}

// A mix of one filed and one skipped intent renders both sections, and the
// skipped entry never appears in the Filed issues list (it was never
// posted).
func TestBuildSkippedIssuesSection_MixedWithFiled(t *testing.T) {
	filed := []filedIntent{
		{Title: "fix(a): filed bug", URL: "https://github.com/owner/repo/issues/1"},
		{Title: "fix(b): dup bug", Skipped: true, DupRef: "#42"},
	}

	filedSection := buildFiledIssuesSection(filed)
	skippedSection := buildSkippedIssuesSection(filed)

	if !strings.Contains(filedSection, "fix(a): filed bug") {
		t.Errorf("filedSection = %q, want the filed title", filedSection)
	}
	if strings.Contains(filedSection, "fix(b): dup bug") {
		t.Errorf("filedSection = %q, want no skipped title", filedSection)
	}
	if !strings.Contains(skippedSection, "fix(b): dup bug") || !strings.Contains(skippedSection, "#42") {
		t.Errorf("skippedSection = %q, want the skipped title and its reference", skippedSection)
	}
}

// Both the title and the dedup reference are agent-chosen, untrusted text
// (the reference can echo an intra-run title verbatim), so a bracket in
// either renders escaped rather than breaking the Markdown bullet.
func TestBuildSkippedIssuesSection_BracketsEscaped(t *testing.T) {
	filed := []filedIntent{
		{Title: "fix(x): [bad] title", Skipped: true, DupRef: `this run's "fix(y): [bad] ref"`},
	}

	got := buildSkippedIssuesSection(filed)

	if !strings.Contains(got, `\[bad\]`) {
		t.Errorf("section = %q, want brackets escaped in both title and reference", got)
	}
	if strings.Contains(got, "[bad]") {
		t.Errorf("section = %q, want no unescaped bracket", got)
	}
}

// A multi-line title renders as a single bullet: the title is agent-chosen,
// so a heading on a later line would otherwise break out of the list and
// forge structure in the posted comment (issue #3811 review).
func TestBuildSkippedIssuesSection_MultiLineTitleTruncated(t *testing.T) {
	filed := []filedIntent{
		{Title: "fix(x): bug\n## forged heading", Skipped: true, DupRef: "#123"},
	}

	got := buildSkippedIssuesSection(filed)

	if strings.Contains(got, "## forged heading") {
		t.Errorf("section = %q, want the title truncated at its first line", got)
	}
	if !strings.Contains(got, "fix(x): bug") {
		t.Errorf("section = %q, want the title's first line kept", got)
	}
}

// A filed list with no skips renders no skipped section at all.
func TestBuildSkippedIssuesSection_NoSkips(t *testing.T) {
	filed := []filedIntent{
		{Title: "fix(a): filed bug", URL: "https://github.com/owner/repo/issues/1"},
	}

	got := buildSkippedIssuesSection(filed)

	if got != "" {
		t.Errorf("section = %q, want empty string when nothing was skipped", got)
	}
}

// The lead sentence must not claim an intra-run match was "an existing open
// issue": it wasn't, it was filed moments earlier by this same run (issue
// #3811 review).
func TestBuildSkippedIssuesSection_LeadCoversIntraRunMatch(t *testing.T) {
	filed := []filedIntent{
		{Title: "fix(y): other bug", Skipped: true, DupRef: `this run's "fix(z): peer bug"`},
	}

	got := buildSkippedIssuesSection(filed)

	if strings.Contains(got, "existing open issue") {
		t.Errorf("section = %q, want no claim of an existing open issue for an intra-run match", got)
	}
	if !strings.Contains(got, "already-filed issue") {
		t.Errorf("section = %q, want the reworded lead covering both a backlog and an intra-run match", got)
	}
}

// buildVerdictCommentSections is the call site's joiner: filed and skipped
// sections, in that order, blank-line separated, so both survive as one
// appended block.
func TestBuildVerdictCommentSections_JoinsFiledAndSkipped(t *testing.T) {
	filed := []filedIntent{
		{Title: "fix(a): filed bug", URL: "https://github.com/owner/repo/issues/1"},
		{Title: "fix(b): dup bug", Skipped: true, DupRef: "#42"},
	}

	got := buildVerdictCommentSections(filed)

	filedIdx := strings.Index(got, "## Filed issues")
	skippedIdx := strings.Index(got, "## Skipped (deduplicated)")
	if filedIdx == -1 || skippedIdx == -1 {
		t.Fatalf("sections = %q, want both headings present", got)
	}
	if filedIdx > skippedIdx {
		t.Errorf("sections = %q, want Filed issues before Skipped", got)
	}
}

// Nothing filed and nothing skipped yields an empty joined string, so the
// call site's non-empty check still skips appending altogether.
func TestBuildVerdictCommentSections_EmptyWhenNothing(t *testing.T) {
	if got := buildVerdictCommentSections(nil); got != "" {
		t.Errorf("sections = %q, want empty string", got)
	}
}
