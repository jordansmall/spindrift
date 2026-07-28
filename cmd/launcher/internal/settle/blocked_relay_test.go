package settle

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
)

// TestSettle_GithubReadOnly_BlockedRelaysBundleAndCreatesDraftPR asserts issue
// #1933's fix: a read-only Box that reaches IF BLOCKED has its finished branch
// bundled to the outbox by the harness post-driver (issue #2082; the agent no
// longer writes the bundle itself since #2083 retired the if-blocked-push-
// outbox.md bundle-write step) and emits a SPINDRIFT_PR_INTENT line (the
// if-blocked-pr-outbox.md fragment) — without this, that work is silently
// stranded when the container exits, since nothing previously relayed it on the
// "blocked" branch of Settle. o.Landing carries the branch name (not a PR URL,
// mirroring the "ready" path) since the Box never opens a PR itself under
// read-only.
func TestSettle_GithubReadOnly_BlockedRelaysBundleAndCreatesDraftPR(t *testing.T) {
	const issNum = "1933"
	const prURL = "https://github.com/owner/repo/pull/1933"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
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

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
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

// TestSettle_LocalReadOnly_BlockedRelaysBundleWithoutDraftPR asserts issue
// #1946's fix: a read-only CODE_FORGE=local Box that reaches IF BLOCKED gets
// its outbox bundle relayed too, not just the PR-shaped github/jira case
// #1933 originally covered -- gate.go's blocked-path condition gated the
// relay on s.pr != nil, which is always nil for local's push-only forge, so
// the relay never ran for it. local doesn't implement DraftPRCreator, so no
// draft PR gets attempted even though the box printed a PR-intent line.
func TestSettle_LocalReadOnly_BlockedRelaysBundleWithoutDraftPR(t *testing.T) {
	const issNum = "1946"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

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
	s := New(c, fc, fc.AsLocal())
	s.Settle(d, issNum, 0, result)

	if len(fc.RelayBundleCalls) != 1 || fc.RelayBundleCalls[0] != (forge.RelayBundleCall{OutboxDir: "/outbox/1946", Ref: branch}) {
		t.Fatalf("RelayBundleCalls = %+v, want one call with outbox=/outbox/1946 ref=%s", fc.RelayBundleCalls, branch)
	}
	if len(fc.CreateDraftPRCalls) != 0 {
		t.Errorf("CreateDraftPR must not be called; local doesn't implement DraftPRCreator, got %+v", fc.CreateDraftPRCalls)
	}

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

// TestSettle_LocalReadWrite_BlockedUnaffectedByHostMediation asserts the
// read-write path (Config.ReadOnly false) never consults BundleRelay on a
// blocked outcome under CODE_FORGE=local either -- the Box already pushed
// (or tried to) in-box under read-write, so there is nothing here for
// settle to relay. Mirrors
// TestSettle_GithubReadWrite_BlockedUnaffectedByHostMediation for the
// push-only forge shape.
func TestSettle_LocalReadWrite_BlockedUnaffectedByHostMediation(t *testing.T) {
	const issNum = "1946"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success:       true,
		OutcomeFound:  true,
		Outcome:       outcome.Outcome{Issue: issNum, Landing: fc.AgentBranch(issNum), Status: "blocked", Note: "push rejected"},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}

	c := baseConfig() // Config.ReadOnly defaults false
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := New(c, fc, fc.AsLocal())
	s.Settle(d, issNum, 0, result)

	if len(fc.RelayBundleCalls) != 0 {
		t.Errorf("RelayBundle must not be called under read-write, got %+v", fc.RelayBundleCalls)
	}
}

// TestSettle_GithubReadOnly_BlockedRelayFailureSkipsDraftPRButStaysBlocked
// asserts a RelayBundle failure during the blocked hand-off logs and moves
// on, never attempting CreateDraftPR (a real force-push failed, so a branch
// that isn't there is nothing to open a PR against) and never changing the
// blocked/agent-failed outcome the caller already recorded.
func TestSettle_GithubReadOnly_BlockedRelayFailureSkipsDraftPRButStaysBlocked(t *testing.T) {
	const issNum = "1933"
	branch := "agent/issue-1933"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.RelayBundleErr = errors.New("bundle missing")

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

	if len(fc.CreateDraftPRCalls) != 0 {
		t.Errorf("CreateDraftPR must not be called when the relay fails, got %+v", fc.CreateDraftPRCalls)
	}
	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must still carry agent-failed after a failed relay; labels=%v", iss.Labels)
	}
}

// TestSettle_GithubReadOnly_BlockedRelayAbsentBundleLogsBenign asserts issue
// #2096's fix: when RelayBundle fails with forge.ErrBundleNotFound during
// the blocked hand-off -- an empty branch range left nothing in the outbox
// to relay -- settle logs an informational ".." line, not the alarming "??
// ... could not relay ..." one. CreateDraftPR still never runs (no branch to
// open a PR against) and the blocked/agent-failed outcome the caller
// already recorded stays untouched, mirroring
// TestSettle_GithubReadOnly_BlockedRelayFailureSkipsDraftPRButStaysBlocked
// for the benign case.
func TestSettle_GithubReadOnly_BlockedRelayAbsentBundleLogsBenign(t *testing.T) {
	const issNum = "1933"
	branch := "agent/issue-1933"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.RelayBundleErr = forge.ErrBundleNotFound

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

	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	s.Settle(d, issNum, 0, result)
	w.Close()
	os.Stderr = old
	captured, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}
	stderr := string(captured)

	if strings.Contains(stderr, "could not relay blocked-hand-off bundle") {
		t.Errorf("stderr must not contain the alarming relay-failure phrase, got: %s", stderr)
	}
	if strings.Contains(stderr, "?? #"+issNum) {
		t.Errorf("stderr must not contain a ?? warning line for #%s, got: %s", issNum, stderr)
	}
	if !strings.Contains(stderr, ".. #"+issNum+":") {
		t.Errorf("stderr must contain an informational .. line for #%s, got: %s", issNum, stderr)
	}
	if !strings.Contains(stderr, "no blocked-hand-off bundle to relay") {
		t.Errorf("stderr must contain the benign no-bundle-to-relay phrase, got: %s", stderr)
	}

	if len(fc.CreateDraftPRCalls) != 0 {
		t.Errorf("CreateDraftPR must not be called when there is no bundle to relay, got %+v", fc.CreateDraftPRCalls)
	}
	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must still carry agent-failed after an absent-bundle relay; labels=%v", iss.Labels)
	}
}

// TestSettle_LocalReadOnly_BlockedRelayFailureStaysBlocked asserts a
// RelayBundle failure during the blocked hand-off logs and moves on under
// CODE_FORGE=local too, never changing the blocked/agent-failed outcome the
// caller already recorded. Mirrors
// TestSettle_GithubReadOnly_BlockedRelayFailureSkipsDraftPRButStaysBlocked
// for the push-only forge shape (local has no CreateDraftPR to skip).
func TestSettle_LocalReadOnly_BlockedRelayFailureStaysBlocked(t *testing.T) {
	const issNum = "1946"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.RelayBundleErr = errors.New("bundle missing")

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: issNum, Landing: branch, Status: "blocked", Note: "review never cleared"},
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := New(c, fc, fc.AsLocal())
	s.Settle(d, issNum, 0, result)

	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must still carry agent-failed after a failed relay; labels=%v", iss.Labels)
	}
}

// TestSettle_LocalReadOnly_BlockedRelayAbsentBundleLogsBenign asserts issue
// #2096's fix under CODE_FORGE=local: when RelayBundle fails with
// forge.ErrBundleNotFound during the blocked hand-off -- an empty branch
// range left nothing in the outbox to relay -- settle logs an informational
// ".." line, not the alarming "?? ... could not relay ..." one, and the
// blocked/agent-failed outcome the caller already recorded stays untouched.
// The benign log lives at the shared relayBlockedWork call site, so this
// mirrors TestSettle_GithubReadOnly_BlockedRelayAbsentBundleLogsBenign for
// the push-only forge shape (local has no CreateDraftPR to skip).
func TestSettle_LocalReadOnly_BlockedRelayAbsentBundleLogsBenign(t *testing.T) {
	const issNum = "1946"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.RelayBundleErr = forge.ErrBundleNotFound

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: issNum, Landing: branch, Status: "blocked", Note: "review never cleared"},
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := New(c, fc, fc.AsLocal())

	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	s.Settle(d, issNum, 0, result)
	w.Close()
	os.Stderr = old
	captured, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}
	stderr := string(captured)

	if strings.Contains(stderr, "could not relay blocked-hand-off bundle") {
		t.Errorf("stderr must not contain the alarming relay-failure phrase, got: %s", stderr)
	}
	if strings.Contains(stderr, "?? #"+issNum) {
		t.Errorf("stderr must not contain a ?? warning line for #%s, got: %s", issNum, stderr)
	}
	if !strings.Contains(stderr, ".. #"+issNum+":") {
		t.Errorf("stderr must contain an informational .. line for #%s, got: %s", issNum, stderr)
	}
	if !strings.Contains(stderr, "no blocked-hand-off bundle to relay") {
		t.Errorf("stderr must contain the benign no-bundle-to-relay phrase, got: %s", stderr)
	}

	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must still carry agent-failed after an absent-bundle relay; labels=%v", iss.Labels)
	}
}

// TestSettle_GithubReadOnly_BlockedDraftPRFailureStillReportsBlocked asserts
// a CreateDraftPR failure (e.g. a draft already exists for this branch from
// an earlier fix pass) logs and moves on without changing the blocked/
// agent-failed outcome the caller already recorded — settle never retries or
// looks up the existing PR itself.
func TestSettle_GithubReadOnly_BlockedDraftPRFailureStillReportsBlocked(t *testing.T) {
	const issNum = "1933"
	branch := "agent/issue-1933"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRErr = errors.New("gh pr create: a pull request already exists")

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

	if len(fc.RelayBundleCalls) != 1 {
		t.Errorf("RelayBundle must still run ahead of the failed CreateDraftPR, got %+v", fc.RelayBundleCalls)
	}
	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must still carry agent-failed after a failed draft-PR create; labels=%v", iss.Labels)
	}
}
