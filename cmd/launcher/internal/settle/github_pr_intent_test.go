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

// TestParsePRIntent_PreservesInternalNewlinesInBody verifies a body
// spanning several lines (a real PR description, not a one-liner) survives
// parsePRIntent's title/body split intact — the split only consumes the
// first "\n" (title) and the blank-line separator immediately after, never
// touching newlines further into the body.
func TestParsePRIntent_PreservesInternalNewlinesInBody(t *testing.T) {
	body := "First paragraph of the summary.\n\nSecond paragraph with more detail.\n- a bullet\n- another bullet"
	result := dispatch.Result{
		PRIntentFound: true,
		PRIntent:      "feat: add widget\n\n" + body,
	}
	title, gotBody, ok := parsePRIntent(result)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if title != "feat: add widget" {
		t.Errorf("title: got %q, want %q", title, "feat: add widget")
	}
	if gotBody != body {
		t.Errorf("body: got %q, want %q", gotBody, body)
	}
}

// TestSettle_GithubReadOnly_ReadyRelaysThenCreatesDraftPRThenMerges asserts
// the full read-only github "ready" hand-off (issue #1919): the Box's
// outcome line carries the branch name (not a PR URL, since it never opened
// one) as landing=, and a SPINDRIFT_PR_INTENT block instead of an in-box `gh
// pr create`. settle relays the finished branch via the Code Forge's
// forge.BundleRelay hook, opens the draft PR itself via forge.DraftPRCreator
// from the parsed title/body, then watches CI and merges on green exactly as
// the read-write path does — proving the Box made no host write at all.
func TestSettle_GithubReadOnly_ReadyRelaysThenCreatesDraftPRThenMerges(t *testing.T) {
	const issNum = "1919"
	const prURL = "https://github.com/owner/repo/pull/1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "ready", Note: "ok"},
		},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	if len(fc.RelayBundleCalls) != 1 || fc.RelayBundleCalls[0] != (forge.RelayBundleCall{OutboxDir: "/outbox/1919", Ref: branch}) {
		t.Fatalf("RelayBundleCalls = %+v, want one call with outbox=/outbox/1919 ref=%s", fc.RelayBundleCalls, branch)
	}
	if len(fc.CreateDraftPRCalls) != 1 {
		t.Fatalf("CreateDraftPRCalls = %+v, want exactly 1", fc.CreateDraftPRCalls)
	}
	want := forge.CreateDraftPRCall{Title: "feat: add widget", Body: "Adds a widget.\n\nCloses #1919", Base: "main", Head: branch}
	if fc.CreateDraftPRCalls[0] != want {
		t.Errorf("CreateDraftPRCalls[0] = %+v, want %+v", fc.CreateDraftPRCalls[0], want)
	}
	if fc.Merged != prURL {
		t.Errorf("expected Merge(%q) to have run; fc.Merged=%q", prURL, fc.Merged)
	}
	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must carry agent-complete after a merged read-only landing; labels=%v", iss.Labels)
	}
}

// TestSettle_GithubReadOnly_ReadyRelaysThenCreatesDraftPRThenMerges_ClosesAlreadyPresent
// asserts ensureClosesReference's dedup: when the box's own PR-intent body
// already carries a GitHub-recognized closing keyword referencing the issue,
// settle must not append a second "Closes #<num>".
func TestSettle_GithubReadOnly_ReadyRelaysThenCreatesDraftPRThenMerges_ClosesAlreadyPresent(t *testing.T) {
	const issNum = "1919"
	const prURL = "https://github.com/owner/repo/pull/1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "ready", Note: "ok"},
		},
		PRIntent:      "feat: add widget\n\nAdds a widget. Closes #1919",
		PRIntentFound: true,
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	if len(fc.CreateDraftPRCalls) != 1 {
		t.Fatalf("CreateDraftPRCalls = %+v, want exactly 1", fc.CreateDraftPRCalls)
	}
	want := forge.CreateDraftPRCall{Title: "feat: add widget", Body: "Adds a widget. Closes #1919", Base: "main", Head: branch}
	if fc.CreateDraftPRCalls[0] != want {
		t.Errorf("CreateDraftPRCalls[0] = %+v, want %+v", fc.CreateDraftPRCalls[0], want)
	}
}

// TestSettle_GithubReadOnly_ReadyRelaysThenCreatesDraftPRThenMerges_LocalTrackerNotInjected
// asserts ensureClosesReference's LandingRecorder short-circuit: when the
// IssueTracker is local-shaped (ISSUE_TRACKER=local, CODE_FORGE=github — a
// valid real combination), settle must not append a "Closes #<num>" since
// the local adapter closes issues through its own axis (ADR 0029), never
// GitHub's auto-close-on-merge convention.
func TestSettle_GithubReadOnly_ReadyRelaysThenCreatesDraftPRThenMerges_LocalTrackerNotInjected(t *testing.T) {
	const issNum = "1919"
	const prURL = "https://github.com/owner/repo/pull/1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "ready", Note: "ok"},
		},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsLocalShaped(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	if len(fc.CreateDraftPRCalls) != 1 {
		t.Fatalf("CreateDraftPRCalls = %+v, want exactly 1", fc.CreateDraftPRCalls)
	}
	want := forge.CreateDraftPRCall{Title: "feat: add widget", Body: "Adds a widget.", Base: "main", Head: branch}
	if fc.CreateDraftPRCalls[0] != want {
		t.Errorf("CreateDraftPRCalls[0] = %+v, want %+v", fc.CreateDraftPRCalls[0], want)
	}
}

// TestSettle_GithubReadOnly_MissingPRIntentAndRelayFailureBlocksNotFails
// asserts a status=ready Box that left no PR-intent line AND whose relay
// itself fails still blocks the hand-off before any draft PR is created —
// no draft PR, CI never watched. Since issue #2447, RelayBundle is always
// attempted regardless of PR-intent presence (a Box can finish real,
// mergeable work and simply fail to print its last line), so this is the
// genuinely-nothing-to-hand-off case: PR-intent missing AND the relay of
// the branch itself fails. The nudge-exhausted hand-off is left visibly
// not-done (#2046): the issue stays agent-in-progress rather than being
// marked agent-complete, which reads as merged/green to an operator (the
// exact #2036 confusion), yet is never demoted to agent-failed either.
func TestSettle_GithubReadOnly_MissingPRIntentAndRelayFailureBlocksNotFails(t *testing.T) {
	const issNum = "1919"
	branch := "agent/issue-1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.RelayBundleErr = errors.New("bundle missing")

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "ready", Note: "ok"},
		},
		// PRIntentFound left false: the box's log had no PR-intent line.
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	if len(fc.RelayBundleCalls) != 1 {
		t.Errorf("RelayBundle must be attempted even with no PR-intent line (issue #2447), got %+v", fc.RelayBundleCalls)
	}
	if len(fc.CreateDraftPRCalls) != 0 {
		t.Errorf("CreateDraftPR must not be called with no PR-intent line, got %+v", fc.CreateDraftPRCalls)
	}
	if fc.Merged != "" {
		t.Errorf("Merge must not be called when no PR was ever opened; fc.Merged=%q", fc.Merged)
	}
	iss, _ := fc.Issue(issNum)
	if containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("a nudge-exhausted blocked hand-off must NOT carry agent-complete — it reads as merged/done to an operator (#2046, the #2036 confusion); labels=%v", iss.Labels)
	}
	if !containsLabel(iss.Labels, "agent-in-progress") {
		t.Errorf("a nudge-exhausted blocked hand-off is left in-progress, visibly not-done; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must NOT carry agent-failed after a blocked hand-off; labels=%v", iss.Labels)
	}
	var blockedCalls []forge.CommentCall
	for _, c := range fc.CommentCalls {
		if strings.Contains(c.Body, "merge blocked") {
			blockedCalls = append(blockedCalls, c)
		}
	}
	if len(blockedCalls) != 1 {
		t.Fatalf("expected exactly one merge-blocked comment, got %d: %+v", len(blockedCalls), fc.CommentCalls)
	}
}

// TestSettle_GithubReadOnly_MissingPRIntentReconstructsFromCommits asserts
// the new fallback behavior (issue #2447): a status=ready Box that left no
// usable PR-intent line but whose relay succeeds gets its branch handed off
// anyway — settle reconstructs a title/body from the relayed branch's own
// commits (forge.BundleCommitSubjects) and opens the draft PR from that,
// rather than blocking a hand-off with nothing actually wrong with it. The
// full merge lifecycle proceeds exactly as the normal PR-intent-found path
// does once the draft PR is open.
func TestSettle_GithubReadOnly_MissingPRIntentReconstructsFromCommits(t *testing.T) {
	const issNum = "1919"
	const prURL = "https://github.com/owner/repo/pull/1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.CommitSubjectsResult = []string{"feat: add widget", "fix: typo"}

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "ready", Note: "ok"},
		},
		// PRIntentFound left false: the box's log had no PR-intent line.
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	if len(fc.RelayBundleCalls) != 1 {
		t.Fatalf("RelayBundleCalls = %+v, want exactly 1", fc.RelayBundleCalls)
	}
	if len(fc.CreateDraftPRCalls) != 1 {
		t.Fatalf("CreateDraftPRCalls = %+v, want exactly 1", fc.CreateDraftPRCalls)
	}
	call := fc.CreateDraftPRCalls[0]
	if call.Title != "feat: add widget" {
		t.Errorf("Title = %q, want first commit subject %q", call.Title, "feat: add widget")
	}
	if !strings.Contains(call.Body, "Reconstructed host-side") {
		t.Errorf("Body = %q, want it to contain the reconstructed-host-side explanation", call.Body)
	}
	if !strings.Contains(call.Body, "- feat: add widget") || !strings.Contains(call.Body, "- fix: typo") {
		t.Errorf("Body = %q, want it to bullet both commit subjects", call.Body)
	}
	if !strings.HasSuffix(strings.TrimRight(call.Body, "\n"), "Closes #1919") {
		t.Errorf("Body = %q, want it to end with Closes #1919", call.Body)
	}
	if fc.Merged != prURL {
		t.Errorf("expected Merge(%q) to have run; fc.Merged=%q", prURL, fc.Merged)
	}
	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must carry agent-complete after a merged reconstructed hand-off; labels=%v", iss.Labels)
	}
}

// TestSettle_GithubReadOnly_MissingPRIntentReconstructsFromCommits_CallsCommitSubjectsWithOutboxBaseAndBranch
// asserts reconstructPRText's CommitSubjects call is wired correctly (issue
// #2447): nothing else in this file asserts against fc.CommitSubjectsCalls
// on a success path, so a swap of s.cfg.BaseBranch and the agent branch (or a
// wrong outbox dir) would still pass every other test here. Pins the single
// recorded call's OutboxDir/Base/Ref to exactly the outbox dir for this
// issue, the configured base branch (never the head branch), and the agent
// branch for this issue (never swapped with base).
func TestSettle_GithubReadOnly_MissingPRIntentReconstructsFromCommits_CallsCommitSubjectsWithOutboxBaseAndBranch(t *testing.T) {
	const issNum = "1919"
	const prURL = "https://github.com/owner/repo/pull/1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.CommitSubjectsResult = []string{"feat: add widget", "fix: typo"}

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "ready", Note: "ok"},
		},
		// PRIntentFound left false: the box's log had no PR-intent line.
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	if len(fc.CommitSubjectsCalls) != 1 {
		t.Fatalf("CommitSubjectsCalls = %+v, want exactly 1", fc.CommitSubjectsCalls)
	}
	want := forge.CommitSubjectsCall{OutboxDir: "/outbox/1919", Base: "main", Ref: branch}
	if fc.CommitSubjectsCalls[0] != want {
		t.Errorf("CommitSubjectsCalls[0] = %+v, want %+v", fc.CommitSubjectsCalls[0], want)
	}
}

// TestSettle_GithubReadOnly_MissingPRIntentReconstructsFromCommits_DefusesInjectedClosingKeyword
// asserts the fix for the closing-keyword injection hazard reconstructPRText
// otherwise carries (issue #2447 follow-up): a box-authored commit subject
// that happens to be shaped like a GitHub closing keyword referencing some
// OTHER issue must not survive verbatim into the bulleted commit list —
// GitHub's own PR-body scanner would auto-close that unrelated issue on
// merge, a hand-off the host never intended. The one real "Closes #<num>"
// line ensureClosesReference appends for the issue actually being landed
// must still be present and untouched.
func TestSettle_GithubReadOnly_MissingPRIntentReconstructsFromCommits_DefusesInjectedClosingKeyword(t *testing.T) {
	const issNum = "1919"
	const otherNum = "999"
	const prURL = "https://github.com/owner/repo/pull/1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.CommitSubjectsResult = []string{"feat: add widget", "fix: closes #" + otherNum}

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "ready", Note: "ok"},
		},
		// PRIntentFound left false: the box's log had no PR-intent line.
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	if len(fc.CreateDraftPRCalls) != 1 {
		t.Fatalf("CreateDraftPRCalls = %+v, want exactly 1", fc.CreateDraftPRCalls)
	}
	body := fc.CreateDraftPRCalls[0].Body
	if hasClosingReference(body, otherNum) {
		t.Errorf("Body = %q, must not carry a live closing reference to unrelated issue #%s", body, otherNum)
	}
	if !strings.Contains(body, otherNum) || !strings.Contains(body, "closes") {
		t.Errorf("Body = %q, want the defused subject to still be visually recognizable (contain %q and \"closes\")", body, otherNum)
	}
	if !hasClosingReference(body, issNum) {
		t.Errorf("Body = %q, want a live Closes reference to the landed issue #%s", body, issNum)
	}
}

// TestSettle_GithubReadOnly_MissingPRIntentReconstructsFromCommits_PostsIssueComment
// asserts AC5 of issue #2447: a reconstructed hand-off must be distinguishable
// from a normal one by an operator reading only the GitHub issue (not the
// launcher's own stdout log) — so hostMediateDraftPR must post a comment on
// the issue itself, alongside the PR body's own "Reconstructed host-side"
// note, explaining that the box's own hand-off carried no usable PR-intent
// line and that the PR was derived host-side from the branch's commits.
func TestSettle_GithubReadOnly_MissingPRIntentReconstructsFromCommits_PostsIssueComment(t *testing.T) {
	const issNum = "1919"
	const prURL = "https://github.com/owner/repo/pull/1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.CommitSubjectsResult = []string{"feat: add widget", "fix: typo"}

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "ready", Note: "ok"},
		},
		// PRIntentFound left false: the box's log had no PR-intent line.
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	var reconstructedCalls []forge.CommentCall
	for _, c := range fc.CommentCalls {
		if strings.Contains(c.Body, "reconstructed") {
			reconstructedCalls = append(reconstructedCalls, c)
		}
	}
	if len(reconstructedCalls) != 1 {
		t.Fatalf("expected exactly one reconstructed-hand-off comment, got %d: %+v", len(reconstructedCalls), fc.CommentCalls)
	}
}

// TestSettle_GithubReadOnly_MissingPRIntentReconstructsButCreateDraftPRFailsBlocksNoReconstructedComment
// asserts that when PR-intent is missing and reconstruction itself succeeds
// (relay succeeds, CommitSubjects returns subjects), but the CreateDraftPR
// call that follows fails, hostMediateDraftPR blocks the hand-off — via the
// same blockHandoff path a genuine relay failure takes — before ever
// reaching the "reconstructed && created" comment-posting block a few lines
// below the CreateDraftPR call (issue #2447 follow-up). Critically, only the
// merge-blocked comment must be posted: the reconstruction succeeding must
// never itself cause the reconstructed-hand-off comment to fire when the
// draft PR create that was supposed to consume that reconstructed text never
// actually succeeded.
func TestSettle_GithubReadOnly_MissingPRIntentReconstructsButCreateDraftPRFailsBlocksNoReconstructedComment(t *testing.T) {
	const issNum = "1919"
	branch := "agent/issue-1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CommitSubjectsResult = []string{"feat: add widget", "fix: typo"}
	fc.CreateDraftPRErr = errors.New("create draft PR: 500")

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "ready", Note: "ok"},
		},
		// PRIntentFound left false: the box's log had no PR-intent line.
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	if len(fc.CreateDraftPRCalls) != 1 {
		t.Fatalf("CreateDraftPRCalls = %+v, want exactly 1 (reconstruction must have succeeded and CreateDraftPR must have been attempted)", fc.CreateDraftPRCalls)
	}
	if fc.Merged != "" {
		t.Errorf("Merge must not be called when CreateDraftPR itself failed; fc.Merged=%q", fc.Merged)
	}
	iss, _ := fc.Issue(issNum)
	if containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("a blocked draft-PR-create must NOT carry agent-complete — it reads as merged/done (#2046); labels=%v", iss.Labels)
	}
	if !containsLabel(iss.Labels, "agent-in-progress") {
		t.Errorf("a blocked draft-PR-create is left in-progress, visibly not-done; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must NOT carry agent-failed after a blocked draft-PR-create; labels=%v", iss.Labels)
	}
	var blockedCalls []forge.CommentCall
	for _, c := range fc.CommentCalls {
		if strings.Contains(c.Body, "merge blocked") {
			blockedCalls = append(blockedCalls, c)
		}
	}
	if len(blockedCalls) != 1 {
		t.Fatalf("expected exactly one merge-blocked comment, got %d: %+v", len(blockedCalls), fc.CommentCalls)
	}
	for _, c := range fc.CommentCalls {
		if strings.Contains(c.Body, "reconstructed") {
			t.Errorf("a failed CreateDraftPR must not get a reconstructed-hand-off comment (the reconstructed text was never applied to any PR), got %+v", fc.CommentCalls)
		}
	}
}

// TestSettle_GithubReadOnly_MissingPRIntentReconstructsButAdoptsExistingPR_NoReconstructedComment
// asserts the fix for the bug this test is named after (issue #2447 follow-
// up): when PR-intent is missing and reconstruction succeeds, but
// CreateDraftPR *adopts* a pre-existing PR (issue #2407's retry path, so
// created=false) instead of creating a fresh one, the reconstructed
// title/body were never actually applied to that PR — hostMediateDraftPR
// must not then claim, via either the stdout log or an issue comment, that
// the hand-off was reconstructed. The call itself still happens (and still
// carries the reconstructed title/body — nothing here skips *building*
// them), and the overall hand-off still succeeds normally (the PR still
// gets watched and merged); only the reconstructed-hand-off signaling is
// suppressed.
func TestSettle_GithubReadOnly_MissingPRIntentReconstructsButAdoptsExistingPR_NoReconstructedComment(t *testing.T) {
	const issNum = "1919"
	const prURL = "https://github.com/owner/repo/pull/1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRAdoptHead = branch
	fc.CreateDraftPRAdoptedURL = prURL
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.CommitSubjectsResult = []string{"feat: add widget", "fix: typo"}

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "ready", Note: "ok"},
		},
		// PRIntentFound left false: the box's log had no PR-intent line.
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	if len(fc.CreateDraftPRCalls) != 1 {
		t.Fatalf("CreateDraftPRCalls = %+v, want exactly 1", fc.CreateDraftPRCalls)
	}
	call := fc.CreateDraftPRCalls[0]
	if call.Title != "feat: add widget" {
		t.Errorf("Title = %q, want first commit subject %q (reconstruction must still run even though the PR is adopted, not created)", call.Title, "feat: add widget")
	}

	for _, c := range fc.CommentCalls {
		if strings.Contains(c.Body, "reconstructed") {
			t.Errorf("adopting a pre-existing PR must not get a reconstructed-hand-off comment (the reconstructed text was never applied to it), got %+v", fc.CommentCalls)
		}
	}

	if fc.Merged != prURL {
		t.Errorf("expected Merge(%q) to have run; the hand-off itself must still succeed normally even though no reconstructed-hand-off comment is posted; fc.Merged=%q", prURL, fc.Merged)
	}
	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must carry agent-complete after a merged hand-off; labels=%v", iss.Labels)
	}
}

// TestSettle_GithubReadOnly_ReadyRelaysThenCreatesDraftPRThenMerges_NoReconstructedComment
// asserts the normal, PR-intent-found path must NOT get the reconstructed-
// hand-off comment new to issue #2447 — only the reconstructed path should.
func TestSettle_GithubReadOnly_ReadyRelaysThenCreatesDraftPRThenMerges_NoReconstructedComment(t *testing.T) {
	const issNum = "1919"
	const prURL = "https://github.com/owner/repo/pull/1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "ready", Note: "ok"},
		},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	for _, c := range fc.CommentCalls {
		if strings.Contains(c.Body, "reconstructed") {
			t.Errorf("normal PR-intent-found hand-off must not get a reconstructed-hand-off comment, got %+v", fc.CommentCalls)
		}
	}
}

// TestSettle_GithubReadOnly_MissingPRIntentReconstructsFromCommits_LocalTrackerNotInjected
// mirrors TestSettle_GithubReadOnly_ReadyRelaysThenCreatesDraftPRThenMerges_LocalTrackerNotInjected
// for the reconstructed path: when the IssueTracker is local-shaped, the
// reconstructed body must not get a "Closes #<num>" appended either.
func TestSettle_GithubReadOnly_MissingPRIntentReconstructsFromCommits_LocalTrackerNotInjected(t *testing.T) {
	const issNum = "1919"
	const prURL = "https://github.com/owner/repo/pull/1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.CommitSubjectsResult = []string{"feat: add widget", "fix: typo"}

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "ready", Note: "ok"},
		},
		// PRIntentFound left false: the box's log had no PR-intent line.
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsLocalShaped(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	if len(fc.CreateDraftPRCalls) != 1 {
		t.Fatalf("CreateDraftPRCalls = %+v, want exactly 1", fc.CreateDraftPRCalls)
	}
	call := fc.CreateDraftPRCalls[0]
	if strings.Contains(call.Body, "Closes #") {
		t.Errorf("Body = %q, must not carry a Closes reference for a local-shaped tracker", call.Body)
	}
}

// TestSettle_GithubReadOnly_MissingPRIntentAndReconstructionFailsBlocksNotFails
// asserts that when PR-intent is missing, relay succeeds, but reconstruction
// itself fails (e.g. CommitSubjects errors), the hand-off still safely
// blocks rather than opening an empty-titled PR — proving the "genuinely
// nothing to hand off" posture degrades correctly even after a successful
// relay.
func TestSettle_GithubReadOnly_MissingPRIntentAndReconstructionFailsBlocksNotFails(t *testing.T) {
	const issNum = "1919"
	branch := "agent/issue-1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CommitSubjectsErr = errors.New("commit subjects: git log failed")

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "ready", Note: "ok"},
		},
		// PRIntentFound left false: the box's log had no PR-intent line.
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	if len(fc.RelayBundleCalls) != 1 {
		t.Errorf("RelayBundle must have been attempted, got %+v", fc.RelayBundleCalls)
	}
	if len(fc.CreateDraftPRCalls) != 0 {
		t.Errorf("CreateDraftPR must not be called when reconstruction fails, got %+v", fc.CreateDraftPRCalls)
	}
	iss, _ := fc.Issue(issNum)
	if containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("a blocked reconstruction must NOT carry agent-complete; labels=%v", iss.Labels)
	}
	if !containsLabel(iss.Labels, "agent-in-progress") {
		t.Errorf("a blocked reconstruction is left in-progress, visibly not-done; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must NOT carry agent-failed after a blocked reconstruction; labels=%v", iss.Labels)
	}
	var blockedCalls []forge.CommentCall
	for _, c := range fc.CommentCalls {
		if strings.Contains(c.Body, "merge blocked") {
			blockedCalls = append(blockedCalls, c)
		}
	}
	if len(blockedCalls) != 1 {
		t.Fatalf("expected exactly one merge-blocked comment, got %d: %+v", len(blockedCalls), fc.CommentCalls)
	}
	// Pin the exact comment body (not just a substring) so a regression
	// re-introducing a stutter of ErrNoPRIntent's own message (mediation.go's
	// Open) is caught here. Composed from blockHandoff's "merge blocked: %v"
	// format wrapping Open's actual FallbackReconstruct-fails-too error:
	// ErrNoPRIntent wrapped together with reconstructPRText's CommitSubjects
	// failure.
	const wantBody = "merge blocked: no usable PR-intent line found in the box's log: reconstructing from the relayed branch's commits also failed: commit subjects: git log failed"
	if got := blockedCalls[0].Body; got != wantBody {
		t.Errorf("merge-blocked comment body = %q, want exactly %q", got, wantBody)
	}
	if strings.Contains(blockedCalls[0].Body, "found: no usable PR-intent line found") {
		t.Errorf("merge-blocked comment body stutters ErrNoPRIntent's message twice: %q", blockedCalls[0].Body)
	}
}

// TestSettle_GithubReadOnly_MissingPRIntentAndZeroCommitSubjectsBlocksNotFails
// asserts that when PR-intent is missing, relay succeeds, but the relayed
// branch carries zero commits to reconstruct from (CommitSubjects succeeds
// with an empty/nil result), the hand-off still safely blocks rather than
// opening an empty-titled PR — the "nothing to reconstruct from" branch of
// reconstructPRText, distinct from a CommitSubjects error.
func TestSettle_GithubReadOnly_MissingPRIntentAndZeroCommitSubjectsBlocksNotFails(t *testing.T) {
	const issNum = "1919"
	branch := "agent/issue-1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	// CommitSubjectsResult left nil (its zero value) and CommitSubjectsErr
	// left nil: CommitSubjects succeeds but returns zero subjects.

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "ready", Note: "ok"},
		},
		// PRIntentFound left false: the box's log had no PR-intent line.
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	if len(fc.RelayBundleCalls) != 1 {
		t.Errorf("RelayBundle must have been attempted, got %+v", fc.RelayBundleCalls)
	}
	if len(fc.CreateDraftPRCalls) != 0 {
		t.Errorf("CreateDraftPR must not be called when there are zero commits to reconstruct from, got %+v", fc.CreateDraftPRCalls)
	}
	iss, _ := fc.Issue(issNum)
	if containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("a blocked reconstruction must NOT carry agent-complete; labels=%v", iss.Labels)
	}
	if !containsLabel(iss.Labels, "agent-in-progress") {
		t.Errorf("a blocked reconstruction is left in-progress, visibly not-done; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must NOT carry agent-failed after a blocked reconstruction; labels=%v", iss.Labels)
	}
	var blockedCalls []forge.CommentCall
	for _, c := range fc.CommentCalls {
		if strings.Contains(c.Body, "merge blocked") {
			blockedCalls = append(blockedCalls, c)
		}
	}
	if len(blockedCalls) != 1 {
		t.Fatalf("expected exactly one merge-blocked comment, got %d: %+v", len(blockedCalls), fc.CommentCalls)
	}
}

// readOnlyForgeWithoutCommitSubjects wraps a github-read-only Fake in a
// struct that embeds only forge.CodeForge, forge.PRForge, forge.BundleRelay,
// and forge.DraftPRCreator — deliberately not forge.BundleCommitSubjects.
// Embedding interface types (not the concrete githubReadOnlyForge) means
// Go's method-set promotion only picks up exactly those four interfaces'
// methods: even though the underlying *Fake-backed value also happens to
// implement CommitSubjects, that method is not promoted onto this wrapper
// type, since promotion follows the static field type, not the dynamic
// value. A type assertion to forge.BundleCommitSubjects on a value of this
// type therefore correctly fails, exercising the "Code Forge doesn't
// implement forge.BundleCommitSubjects at all" branch of reconstructPRText.
type readOnlyForgeWithoutCommitSubjects struct {
	forge.CodeForge
	forge.PRForge
	forge.BundleRelay
	forge.DraftPRCreator
}

// TestSettle_GithubReadOnly_CodeForgeLacksCommitSubjectsBlocksNotFails
// asserts that when PR-intent is missing, relay succeeds, but the Code Forge
// itself doesn't implement forge.BundleCommitSubjects at all (a real,
// reachable gap: unlike BundleRelay/DraftPRCreator, nothing guarantees every
// implementor of those two also implements this newer, independently
// optional capability), the hand-off still safely blocks. Also asserts
// CommitSubjects itself is never called — the type assertion fails first.
func TestSettle_GithubReadOnly_CodeForgeLacksCommitSubjectsBlocksNotFails(t *testing.T) {
	const issNum = "1919"
	branch := "agent/issue-1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	readOnly := fc.AsGithubReadOnly()
	wrapper := readOnlyForgeWithoutCommitSubjects{
		CodeForge:      readOnly,
		PRForge:        readOnly.(forge.PRForge),
		BundleRelay:    readOnly.(forge.BundleRelay),
		DraftPRCreator: readOnly.(forge.DraftPRCreator),
	}
	if _, ok := any(wrapper).(forge.BundleCommitSubjects); ok {
		t.Fatal("readOnlyForgeWithoutCommitSubjects must not satisfy forge.BundleCommitSubjects")
	}

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "ready", Note: "ok"},
		},
		// PRIntentFound left false: the box's log had no PR-intent line.
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), wrapper)
	s.Settle(d, issNum, 0, result)

	if len(fc.RelayBundleCalls) != 1 {
		t.Errorf("RelayBundle must have been attempted, got %+v", fc.RelayBundleCalls)
	}
	if len(fc.CreateDraftPRCalls) != 0 {
		t.Errorf("CreateDraftPR must not be called when the Code Forge lacks forge.BundleCommitSubjects, got %+v", fc.CreateDraftPRCalls)
	}
	if len(fc.CommitSubjectsCalls) != 0 {
		t.Errorf("CommitSubjects must never be called when the type assertion to forge.BundleCommitSubjects itself fails, got %+v", fc.CommitSubjectsCalls)
	}
	iss, _ := fc.Issue(issNum)
	if containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("a blocked reconstruction must NOT carry agent-complete; labels=%v", iss.Labels)
	}
	if !containsLabel(iss.Labels, "agent-in-progress") {
		t.Errorf("a blocked reconstruction is left in-progress, visibly not-done; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must NOT carry agent-failed after a blocked reconstruction; labels=%v", iss.Labels)
	}
	var blockedCalls []forge.CommentCall
	for _, c := range fc.CommentCalls {
		if strings.Contains(c.Body, "merge blocked") {
			blockedCalls = append(blockedCalls, c)
		}
	}
	if len(blockedCalls) != 1 {
		t.Fatalf("expected exactly one merge-blocked comment, got %d: %+v", len(blockedCalls), fc.CommentCalls)
	}
}

// TestSettle_GithubReadOnly_RelayFailureBlocksBeforeCreatingPR asserts a
// missing/malformed bundle blocks the hand-off before any draft PR is
// attempted — RelayBundle must run, and fail, ahead of CreateDraftPR.
func TestSettle_GithubReadOnly_RelayFailureBlocksBeforeCreatingPR(t *testing.T) {
	const issNum = "1919"
	branch := "agent/issue-1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.RelayBundleErr = errors.New("bundle missing")

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "ready", Note: "ok"},
		},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	if len(fc.CreateDraftPRCalls) != 0 {
		t.Errorf("CreateDraftPR must not be called when the relay fails, got %+v", fc.CreateDraftPRCalls)
	}
	iss, _ := fc.Issue(issNum)
	if containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("a blocked relay must NOT carry agent-complete — it reads as merged/done (#2046); labels=%v", iss.Labels)
	}
	if !containsLabel(iss.Labels, "agent-in-progress") {
		t.Errorf("a blocked relay is left in-progress, visibly not-done; labels=%v", iss.Labels)
	}
}

// TestSettle_GithubReadWrite_UnaffectedByHostMediation asserts the
// read-write path (Config.ReadOnly false) never consults BundleRelay or
// DraftPRCreator even when the Code Forge happens to implement them — the
// Box already opened its own PR in-box, so o.Landing is already a real URL
// settle should watch CI on directly.
func TestSettle_GithubReadWrite_UnaffectedByHostMediation(t *testing.T) {
	const issNum = "1919"
	const prURL = "https://github.com/owner/repo/pull/1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "ready", Note: "ok"},
		},
	}

	c := baseConfig() // Config.ReadOnly defaults false
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	if len(fc.RelayBundleCalls) != 0 {
		t.Errorf("RelayBundle must not be called under read-write, got %+v", fc.RelayBundleCalls)
	}
	if len(fc.CreateDraftPRCalls) != 0 {
		t.Errorf("CreateDraftPR must not be called under read-write, got %+v", fc.CreateDraftPRCalls)
	}
	if fc.Merged != prURL {
		t.Errorf("expected Merge(%q) to have run against the box's own PR; fc.Merged=%q", prURL, fc.Merged)
	}
}

// TestSettle_GithubReadOnly_HostileLandingIgnored_UsesAgentBranch asserts
// issue #1949's fix for a confirmed live exploit: a prompt-injected read-only
// Box can print landing=main (or any other ref) on its outcome line. settle
// must never trust that value as the relay/force-push destination or the
// draft-PR head — both are derived host-side from the Code Forge's own
// canonical AgentBranch for the issue, regardless of what landing= says.
func TestSettle_GithubReadOnly_HostileLandingIgnored_UsesAgentBranch(t *testing.T) {
	const issNum = "1919"
	const hostileLanding = "main"
	const prURL = "https://github.com/owner/repo/pull/1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	agentBranch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: hostileLanding, Status: "ready", Note: "ok"},
		},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	if len(fc.RelayBundleCalls) != 1 || fc.RelayBundleCalls[0].Ref != agentBranch {
		t.Fatalf("RelayBundleCalls = %+v, want one call with ref=%s (never the hostile landing=%s)", fc.RelayBundleCalls, agentBranch, hostileLanding)
	}
	if len(fc.CreateDraftPRCalls) != 1 || fc.CreateDraftPRCalls[0].Head != agentBranch {
		t.Fatalf("CreateDraftPRCalls = %+v, want one call with head=%s (never the hostile landing=%s)", fc.CreateDraftPRCalls, agentBranch, hostileLanding)
	}
}

// TestSettle_GithubReadOnly_MergedStatus_HostileLandingIgnored_UsesAgentBranch
// asserts the "merged" analogue of #1949's fix (issue #1955): a
// prompt-injected read-only Box can print landing=main (or any other ref) on
// a status=merged outcome line too. verifyMerged must never trust that value
// as the PR to check for MERGED state — it has to be resolved host-side from
// the Code Forge's own canonical AgentBranch for the issue, exactly like the
// "ready" arm above.
func TestSettle_GithubReadOnly_MergedStatus_HostileLandingIgnored_UsesAgentBranch(t *testing.T) {
	const issNum = "1955"
	const hostileLanding = "main"
	const prURL = "https://github.com/owner/repo/pull/1955"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	agentBranch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress", "agent-complete"}})
	// The real PR is registered only against the host-derived agent branch,
	// never against the hostile landing="main" — if settle ever resolves the
	// PR to check via o.Landing instead, it finds no such PR here.
	fc.SetPR(agentBranch, forge.PR{URL: prURL})
	fc.SetPRState(prURL, forge.PRMerged)

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: hostileLanding, Status: "merged", Note: "ok"},
		},
	}

	c := baseConfig()
	c.ReadOnly = true
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	out := testutil.CaptureStdout(t, func() {
		s.Settle(d, issNum, 0, result)
	})

	iss, _ := fc.Issue(issNum)
	if containsLabel(iss.Labels, "agent-failed") {
		t.Fatalf("hostile landing=%s on a genuinely-merged status=merged outcome must not demote to agent-failed; verifyMerged must resolve the host-derived agent branch (%s), not landing=. labels=%v", hostileLanding, agentBranch, iss.Labels)
	}
	if !containsLabel(iss.Labels, "agent-in-progress") {
		t.Fatalf("issue must remain agent-in-progress after a successful host-derived merge verification; labels=%v", iss.Labels)
	}
	// Positive proof verifyMerged actually ran and confirmed MERGED against the
	// host-derived PR — without it a future refactor that skipped verifyMerged
	// entirely would still satisfy the negative label assertions above, a
	// vacuous pass. The verified-merged line must name the real PR URL, never
	// the hostile landing="main".
	if want := "landing=" + prURL + "  status=verified-merged"; !strings.Contains(out, want) {
		t.Fatalf("verifyMerged must confirm the host-derived PR merged; want %q in output; got: %q", want, out)
	}
	if bad := "landing=" + hostileLanding + "  status=verified-merged"; strings.Contains(out, bad) {
		t.Fatalf("verifyMerged must never verify against the hostile landing=%s; got: %q", hostileLanding, out)
	}
}
