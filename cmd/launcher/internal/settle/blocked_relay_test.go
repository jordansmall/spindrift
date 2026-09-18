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

// captureStderr returns everything fn writes to os.Stderr. It reads the pipe
// only after fn returns, so it is safe only for output that stays well under
// the pipe's 64KB buffer.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = old
	defer r.Close()
	captured, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}
	return string(captured)
}

// Pins issue #1933: without the relay, a read-only Box that reaches IF BLOCKED
// strands its work when the container exits. The harness post-driver writes the
// bundle (#2082, #2083), so o.Landing carries the branch name rather than a PR
// URL: the Box never opens a PR itself under read-only.
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
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "blocked", Note: "review never cleared"},
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

	if len(fc.RelayBundleCalls) != 1 || fc.RelayBundleCalls[0] != (forge.RelayBundleCall{OutboxDir: "/outbox/1933", Ref: branch}) {
		t.Fatalf("RelayBundleCalls = %+v, want one call with outbox=/outbox/1933 ref=%s", fc.RelayBundleCalls, branch)
	}
	if len(fc.CreateDraftPRCalls) != 1 {
		t.Fatalf("CreateDraftPRCalls = %+v, want exactly 1", fc.CreateDraftPRCalls)
	}
	want := forge.CreateDraftPRCall{Title: "feat: add widget", Body: "Adds a widget.\n\nCloses #1933", Base: "main", Head: branch}
	if fc.CreateDraftPRCalls[0] != want {
		t.Errorf("CreateDraftPRCalls[0] = %+v, want %+v", fc.CreateDraftPRCalls[0], want)
	}

	// Relaying the Box's work is additive, never a substitute for reporting the
	// issue as genuinely blocked, so the transition and comment must survive.
	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must carry agent-failed after a blocked outcome; labels=%v", iss.Labels)
	}
	var noteCalls []forge.CommentCall
	for _, call := range fc.CommentCalls {
		if call.Body == result.Resolved.Outcome.Note {
			noteCalls = append(noteCalls, call)
		}
	}
	if len(noteCalls) != 1 {
		t.Fatalf("want 1 comment posting the blocked note, got %d (all calls: %+v)", len(noteCalls), fc.CommentCalls)
	}
}

// Pins ensureClosesReference's dedup on the blocked hand-off path: when the
// box's own PR-intent body already carries a closing keyword for the issue,
// settle must not append a second one.
func TestSettle_GithubReadOnly_BlockedRelaysBundleAndCreatesDraftPR_ClosesAlreadyPresent(t *testing.T) {
	const issNum = "1933"
	const prURL = "https://github.com/owner/repo/pull/1933"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "blocked", Note: "review never cleared"},
		},
		PRIntent:      "feat: add widget\n\nAdds a widget. Closes #1933",
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
	want := forge.CreateDraftPRCall{Title: "feat: add widget", Body: "Adds a widget. Closes #1933", Base: "main", Head: branch}
	if fc.CreateDraftPRCalls[0] != want {
		t.Errorf("CreateDraftPRCalls[0] = %+v, want %+v", fc.CreateDraftPRCalls[0], want)
	}
}

// Pins ensureClosesReference's LandingRecorder short-circuit: with a
// local-shaped IssueTracker (ISSUE_TRACKER=local, CODE_FORGE=github, a valid
// combination), settle must not append a closing keyword. The local adapter
// closes issues through its own axis (ADR 0029), not GitHub's
// auto-close-on-merge convention.
func TestSettle_GithubReadOnly_BlockedRelaysBundleAndCreatesDraftPR_LocalTrackerNotInjected(t *testing.T) {
	const issNum = "1933"
	const prURL = "https://github.com/owner/repo/pull/1933"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "blocked", Note: "review never cleared"},
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

// A Box that reaches IF BLOCKED before printing a PR-intent line still gets its
// branch relayed, with no draft PR opened: there is no title or body to open one
// with.
func TestSettle_GithubReadOnly_BlockedRelaysBundleWithoutPRIntent(t *testing.T) {
	const issNum = "1933"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "blocked", Note: "push rejected"},
		},
		// PRIntentFound left false: the box's log had no PR-intent line.
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
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

// Pins issue #1946: gate.go's blocked path gated the relay on s.pr != nil,
// which is always nil for local's push-only forge, so a read-only
// CODE_FORGE=local Box never got its bundle relayed. local implements no
// DraftPRCreator, so no draft PR is attempted even though the box printed a
// PR-intent line.
func TestSettle_LocalReadOnly_BlockedRelaysBundleWithoutDraftPR(t *testing.T) {
	const issNum = "1946"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "blocked", Note: "review never cleared"},
		},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc, fc.AsLocal())
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
		if call.Body == result.Resolved.Outcome.Note {
			noteCalls = append(noteCalls, call)
		}
	}
	if len(noteCalls) != 1 {
		t.Fatalf("want 1 comment posting the blocked note, got %d (all calls: %+v)", len(noteCalls), fc.CommentCalls)
	}
}

// The read-write path never consults BundleRelay or DraftPRCreator on a blocked
// outcome, even when the Code Forge implements both and the box's log carries a
// PR-intent line: the Box already pushed and opened its own PR in-box.
func TestSettle_GithubReadWrite_BlockedUnaffectedByHostMediation(t *testing.T) {
	const issNum = "1933"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: "https://github.com/owner/repo/pull/1933", Status: "blocked", Note: "push rejected"},
		},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}

	c := baseConfig() // Config.ReadOnly defaults false
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	if len(fc.RelayBundleCalls) != 0 {
		t.Errorf("RelayBundle must not be called under read-write, got %+v", fc.RelayBundleCalls)
	}
	if len(fc.CreateDraftPRCalls) != 0 {
		t.Errorf("CreateDraftPR must not be called under read-write, got %+v", fc.CreateDraftPRCalls)
	}
}

// Mirrors TestSettle_GithubReadWrite_BlockedUnaffectedByHostMediation for the
// push-only CODE_FORGE=local forge: read-write never consults BundleRelay on a
// blocked outcome there either.
func TestSettle_LocalReadWrite_BlockedUnaffectedByHostMediation(t *testing.T) {
	const issNum = "1946"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: fc.AgentBranch(issNum), Status: "blocked", Note: "push rejected"},
		},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}

	c := baseConfig() // Config.ReadOnly defaults false
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc, fc.AsLocal())
	s.Settle(d, issNum, 0, result)

	if len(fc.RelayBundleCalls) != 0 {
		t.Errorf("RelayBundle must not be called under read-write, got %+v", fc.RelayBundleCalls)
	}
}

// A RelayBundle failure during the blocked hand-off logs and moves on. It never
// attempts CreateDraftPR, since the force-push failed and there is no branch to
// open a PR against, and never changes the recorded blocked/agent-failed
// outcome.
func TestSettle_GithubReadOnly_BlockedRelayFailureSkipsDraftPRButStaysBlocked(t *testing.T) {
	const issNum = "1933"
	branch := "agent/issue-1933"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.RelayBundleErr = errors.New("bundle missing")

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "blocked", Note: "review never cleared"},
		},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())

	stderr := captureStderr(t, func() { s.Settle(d, issNum, 0, result) })

	if len(fc.CreateDraftPRCalls) != 0 {
		t.Errorf("CreateDraftPR must not be called when the relay fails, got %+v", fc.CreateDraftPRCalls)
	}
	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must still carry agent-failed after a failed relay; labels=%v", iss.Labels)
	}
	if !strings.Contains(stderr, "could not relay blocked-hand-off bundle") {
		t.Errorf("stderr must contain the relay-failure phrase, got: %s", stderr)
	}
	if !strings.Contains(stderr, fc.RelayBundleErr.Error()) {
		t.Errorf("stderr must contain the real relay error text %q (not <nil>), got: %s", fc.RelayBundleErr.Error(), stderr)
	}
	if strings.Contains(stderr, "<nil>") {
		t.Errorf("stderr must not print <nil> in place of the real relay error, got: %s", stderr)
	}
}

// Pins issue #2096: when an empty branch range leaves nothing in the outbox,
// RelayBundle fails with forge.ErrBundleNotFound and settle logs an
// informational ".." line rather than an alarming "??" one. CreateDraftPR still
// never runs and the recorded blocked/agent-failed outcome stays untouched.
func TestSettle_GithubReadOnly_BlockedRelayAbsentBundleLogsBenign(t *testing.T) {
	const issNum = "1933"
	branch := "agent/issue-1933"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.RelayBundleErr = forge.ErrBundleNotFound

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "blocked", Note: "review never cleared"},
		},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())

	stderr := captureStderr(t, func() { s.Settle(d, issNum, 0, result) })

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

// Mirrors TestSettle_GithubReadOnly_BlockedRelayFailureSkipsDraftPRButStaysBlocked
// for the push-only CODE_FORGE=local forge: a relay failure still leaves the
// recorded blocked/agent-failed outcome alone, and local has no CreateDraftPR
// to skip.
func TestSettle_LocalReadOnly_BlockedRelayFailureStaysBlocked(t *testing.T) {
	const issNum = "1946"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.RelayBundleErr = errors.New("bundle missing")

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "blocked", Note: "review never cleared"},
		},
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc, fc.AsLocal())
	s.Settle(d, issNum, 0, result)

	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must still carry agent-failed after a failed relay; labels=%v", iss.Labels)
	}
}

// Issue #2096 under CODE_FORGE=local: an absent bundle logs an informational
// ".." line, not an alarming "??" one, and leaves the recorded
// blocked/agent-failed outcome untouched. The benign log lives at the shared
// relayBlockedWork call site, so this covers the push-only forge shape.
func TestSettle_LocalReadOnly_BlockedRelayAbsentBundleLogsBenign(t *testing.T) {
	const issNum = "1946"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.RelayBundleErr = forge.ErrBundleNotFound

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "blocked", Note: "review never cleared"},
		},
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc, fc.AsLocal())

	stderr := captureStderr(t, func() { s.Settle(d, issNum, 0, result) })

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

// A CreateDraftPR failure, such as a draft already open for this branch from an
// earlier fix pass, logs and moves on without changing the recorded
// blocked/agent-failed outcome. Settle never retries or looks up the existing
// PR.
func TestSettle_GithubReadOnly_BlockedDraftPRFailureStillReportsBlocked(t *testing.T) {
	const issNum = "1933"
	branch := "agent/issue-1933"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRErr = errors.New("gh pr create: a pull request already exists")

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "blocked", Note: "review never cleared"},
		},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())

	stderr := captureStderr(t, func() { s.Settle(d, issNum, 0, result) })

	if len(fc.RelayBundleCalls) != 1 {
		t.Errorf("RelayBundle must still run ahead of the failed CreateDraftPR, got %+v", fc.RelayBundleCalls)
	}
	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must still carry agent-failed after a failed draft-PR create; labels=%v", iss.Labels)
	}
	if !strings.Contains(stderr, "could not create draft PR for blocked hand-off") {
		t.Errorf("stderr must contain the draft-PR-failure phrase, got: %s", stderr)
	}
	if !strings.Contains(stderr, fc.CreateDraftPRErr.Error()) {
		t.Errorf("stderr must contain the real create-draft-PR error text %q (not <nil>), got: %s", fc.CreateDraftPRErr.Error(), stderr)
	}
	if strings.Contains(stderr, "<nil>") {
		t.Errorf("stderr must not print <nil> in place of the real create-draft-PR error, got: %s", stderr)
	}
}
