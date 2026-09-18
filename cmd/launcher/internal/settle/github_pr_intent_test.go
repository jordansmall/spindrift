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

// The title/body split consumes only the first "\n" and the blank line right
// after it, so newlines further into the body must survive untouched.
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

// Pins the full read-only github "ready" hand-off (issue #1919): the Box
// prints a branch name as landing= and a SPINDRIFT_PR_INTENT block instead of
// running `gh pr create`, so settle relays the branch, opens the draft PR
// itself from the parsed title/body, then watches CI and merges on green. The
// Box makes no host write at all.
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

// ensureClosesReference dedups: when the box's own PR-intent body already
// carries a GitHub-recognized closing keyword for the issue, settle must not
// append a second "Closes #<num>".
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

// ensureClosesReference short-circuits on a LandingRecorder: with
// ISSUE_TRACKER=local and CODE_FORGE=github, a valid real combination, the
// local adapter closes issues through its own axis (ADR 0029), so settle must
// not append a "Closes #<num>".
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

// The genuinely-nothing-to-hand-off case: issue #2447 made RelayBundle always
// run whether or not a PR-intent line is present, so blocking now needs both a
// missing PR-intent line and a failed relay. The hand-off is left visibly
// not-done (#2046): the issue stays agent-in-progress, not the agent-complete
// an operator reads as merged (#2036), and is never demoted to agent-failed.
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

// The issue #2447 fallback: a status=ready Box with no usable PR-intent line
// but a successful relay still gets its branch handed off. settle reconstructs
// a title/body from the branch's own commits (forge.BundleCommitSubjects)
// instead of blocking a hand-off with nothing actually wrong with it, then
// merges exactly as the PR-intent-found path does.
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

// Nothing else in this file asserts against fc.CommitSubjectsCalls on a
// success path, so a swap of s.cfg.BaseBranch and the agent branch, or a wrong
// outbox dir, would still pass every other test here (issue #2447).
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

// A box-authored commit subject shaped like a GitHub closing keyword for some
// other issue must not survive verbatim into the bulleted commit list, since
// GitHub's PR-body scanner would auto-close that unrelated issue on merge
// (issue #2447 follow-up). The one real "Closes #<num>" for the issue actually
// being landed must still be present and untouched.
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

// AC5 of issue #2447: an operator reading only the GitHub issue, not the
// launcher's stdout log, must still be able to tell a reconstructed hand-off
// from a normal one, so hostMediateDraftPR posts a comment on the issue itself
// alongside the PR body's own "Reconstructed host-side" note.
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

// When reconstruction succeeds but the CreateDraftPR that follows fails,
// hostMediateDraftPR blocks through the same blockHandoff path a relay failure
// takes (issue #2447 follow-up). Only the merge-blocked comment may be posted:
// the reconstructed text never reached any PR, so the reconstructed-hand-off
// comment must not fire.
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

// When CreateDraftPR adopts a pre-existing PR (issue #2407's retry path, so
// created=false) instead of creating a fresh one, the reconstructed title/body
// never reached that PR, so neither the stdout log nor an issue comment may
// claim the hand-off was reconstructed (issue #2447 follow-up). The call still
// carries the reconstructed text and the hand-off still merges normally.
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

// The normal, PR-intent-found path must not get the reconstructed-hand-off
// comment new to issue #2447.
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

// The reconstructed path's analogue of the local-tracker case above: a
// local-shaped IssueTracker gets no "Closes #<num>" appended either.
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

// PR-intent is missing and the relay succeeds, but reconstruction itself fails
// (CommitSubjects errors), so the hand-off must still block rather than open
// an empty-titled PR.
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
	// Pin the exact body, not just a substring, so a regression re-introducing
	// a stutter of ErrNoPRIntent's own message (mediation.go's Open) is caught
	// here.
	const wantBody = "merge blocked: no usable PR-intent line found in the box's log: reconstructing from the relayed branch's commits also failed: commit subjects: git log failed"
	if got := blockedCalls[0].Body; got != wantBody {
		t.Errorf("merge-blocked comment body = %q, want exactly %q", got, wantBody)
	}
	if strings.Contains(blockedCalls[0].Body, "found: no usable PR-intent line found") {
		t.Errorf("merge-blocked comment body stutters ErrNoPRIntent's message twice: %q", blockedCalls[0].Body)
	}
}

// CommitSubjects succeeds but returns zero subjects, which is a distinct
// branch of reconstructPRText from a CommitSubjects error: the hand-off must
// still block rather than open an empty-titled PR.
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

// Embeds interface types, not the concrete githubReadOnlyForge, because Go's
// method promotion follows the static field type: CommitSubjects stays
// unpromoted even though the underlying Fake-backed value implements it. A
// type assertion to forge.BundleCommitSubjects therefore fails, which is the
// branch of reconstructPRText this exercises.
type readOnlyForgeWithoutCommitSubjects struct {
	forge.CodeForge
	forge.PRForge
	forge.BundleRelay
	forge.DraftPRCreator
}

// A reachable gap: nothing guarantees that an implementor of BundleRelay and
// DraftPRCreator also implements the newer, independently optional
// forge.BundleCommitSubjects. The hand-off must block, and CommitSubjects must
// never be called, because the type assertion fails first.
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

// A missing or malformed bundle blocks the hand-off before any draft PR is
// attempted: RelayBundle must run, and fail, ahead of CreateDraftPR.
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

// The read-write path (Config.ReadOnly false) never consults BundleRelay or
// DraftPRCreator even when the Code Forge implements them: the Box already
// opened its own PR in-box, so o.Landing is a real URL to watch CI on.
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

// Issue #1949's fix for a confirmed live exploit: a prompt-injected read-only
// Box can print landing=main, or any other ref, on its outcome line. settle
// must never trust that as the relay destination or the draft-PR head. Both
// come from the Code Forge's own canonical AgentBranch for the issue.
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

// The "merged" analogue of #1949's fix (issue #1955): a prompt-injected Box
// can print landing=main on a status=merged outcome line too, so verifyMerged
// must resolve the PR to check from the Code Forge's own canonical
// AgentBranch, exactly like the "ready" arm above.
func TestSettle_GithubReadOnly_MergedStatus_HostileLandingIgnored_UsesAgentBranch(t *testing.T) {
	const issNum = "1955"
	const hostileLanding = "main"
	const prURL = "https://github.com/owner/repo/pull/1955"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	agentBranch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress", "agent-complete"}})
	// The PR is registered only against the host-derived agent branch, so a
	// settle that resolved it via o.Landing would find no PR here.
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
	// Positive proof verifyMerged ran: without it a refactor that skipped
	// verifyMerged entirely would still satisfy the negative label assertions
	// above, a vacuous pass.
	if want := "landing=" + prURL + "  status=verified-merged"; !strings.Contains(out, want) {
		t.Fatalf("verifyMerged must confirm the host-derived PR merged; want %q in output; got: %q", want, out)
	}
	if bad := "landing=" + hostileLanding + "  status=verified-merged"; strings.Contains(out, bad) {
		t.Fatalf("verifyMerged must never verify against the hostile landing=%s; got: %q", hostileLanding, out)
	}
}
