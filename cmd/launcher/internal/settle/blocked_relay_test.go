package settle

import (
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
)

// TestSettle_GithubReadOnly_BlockedRelaysBundleAndCreatesDraftPR asserts issue
// #1933's fix: a read-only Box that reaches IF BLOCKED already wrote its
// finished branch to the outbox and a SPINDRIFT_PR_INTENT line (the new
// if-blocked-push-outbox.md/if-blocked-pr-outbox.md fragments) — without this,
// that work is silently stranded when the container exits, since nothing
// previously relayed it on the "blocked" branch of Settle. o.Landing carries
// the branch name (not a PR URL, mirroring the "ready" path) since the Box
// never opens a PR itself under read-only.
func TestSettle_GithubReadOnly_BlockedRelaysBundleAndCreatesDraftPR(t *testing.T) {
	const issNum = "1933"
	branch := "agent/issue-1933"
	const prURL = "https://github.com/owner/repo/pull/1933"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success:       true,
		OutcomeFound:  true,
		Outcome:       outcome.Outcome{Issue: issNum, Landing: branch, Status: "blocked", Note: "review never cleared"},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := New(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	if len(fc.RelayBundleCalls) != 1 || fc.RelayBundleCalls[0] != (forge.RelayBundleCall{OutboxDir: "/outbox/1933", Ref: branch}) {
		t.Fatalf("RelayBundleCalls = %+v, want one call with outbox=/outbox/1933 ref=%s", fc.RelayBundleCalls, branch)
	}
	if len(fc.CreateDraftPRCalls) != 1 {
		t.Fatalf("CreateDraftPRCalls = %+v, want exactly 1", fc.CreateDraftPRCalls)
	}
	want := forge.CreateDraftPRCall{Title: "feat: add widget", Body: "Adds a widget.", Base: "main", Head: branch}
	if fc.CreateDraftPRCalls[0] != want {
		t.Errorf("CreateDraftPRCalls[0] = %+v, want %+v", fc.CreateDraftPRCalls[0], want)
	}

	// The blocked transition/comment this settle already made must survive
	// unchanged -- relaying the Box's work is additive, never a substitute
	// for reporting the issue as genuinely blocked (never agent-complete).
	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must carry agent-failed after a blocked outcome; labels=%v", iss.Labels)
	}
	var noteCalls []forge.CommentCall
	for _, call := range fc.CommentCalls {
		if call.Body == result.Outcome.Note {
			noteCalls = append(noteCalls, call)
		}
	}
	if len(noteCalls) != 1 {
		t.Fatalf("want 1 comment posting the blocked note, got %d (all calls: %+v)", len(noteCalls), fc.CommentCalls)
	}
}

// TestSettle_GithubReadOnly_BlockedRelaysBundleWithoutPRIntent asserts a Box
// that reaches IF BLOCKED before ever printing a PR-intent line (e.g. review
// never cleared, so it never reached the OPEN A PULL REQUEST section) still
// gets its branch relayed -- just with no draft PR opened, since there is no
// title/body to open one with.
func TestSettle_GithubReadOnly_BlockedRelaysBundleWithoutPRIntent(t *testing.T) {
	const issNum = "1933"
	branch := "agent/issue-1933"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: issNum, Landing: branch, Status: "blocked", Note: "push rejected"},
		// PRIntentFound left false: the box's log had no PR-intent line.
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := New(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	if len(fc.RelayBundleCalls) != 1 || fc.RelayBundleCalls[0] != (forge.RelayBundleCall{OutboxDir: "/outbox/1933", Ref: branch}) {
		t.Fatalf("RelayBundleCalls = %+v, want one call with outbox=/outbox/1933 ref=%s", fc.RelayBundleCalls, branch)
	}
	if len(fc.CreateDraftPRCalls) != 0 {
		t.Errorf("CreateDraftPR must not be called with no PR-intent line, got %+v", fc.CreateDraftPRCalls)
	}
	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must still carry agent-failed; labels=%v", iss.Labels)
	}
}

// TestSettle_GithubReadWrite_BlockedUnaffectedByHostMediation asserts the
// read-write path (Config.ReadOnly false) never consults BundleRelay or
// DraftPRCreator on a blocked outcome, even when the Code Forge happens to
// implement them and the box's log carries a PR-intent line -- the Box
// already pushed (or tried to) and opened its own PR in-box under
// read-write, so there is nothing here for settle to relay.
func TestSettle_GithubReadWrite_BlockedUnaffectedByHostMediation(t *testing.T) {
	const issNum = "1933"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success:       true,
		OutcomeFound:  true,
		Outcome:       outcome.Outcome{Issue: issNum, Landing: "https://github.com/owner/repo/pull/1933", Status: "blocked", Note: "push rejected"},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}

	c := baseConfig() // Config.ReadOnly defaults false
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := New(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	if len(fc.RelayBundleCalls) != 0 {
		t.Errorf("RelayBundle must not be called under read-write, got %+v", fc.RelayBundleCalls)
	}
	if len(fc.CreateDraftPRCalls) != 0 {
		t.Errorf("CreateDraftPR must not be called under read-write, got %+v", fc.CreateDraftPRCalls)
	}
}
