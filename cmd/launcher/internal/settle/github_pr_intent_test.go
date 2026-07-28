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
		Success:       true,
		OutcomeFound:  true,
		Outcome:       outcome.Outcome{Issue: issNum, Landing: branch, Status: "ready", Note: "ok"},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := New(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	if len(fc.RelayBundleCalls) != 1 || fc.RelayBundleCalls[0] != (forge.RelayBundleCall{OutboxDir: "/outbox/1919", Ref: branch}) {
		t.Fatalf("RelayBundleCalls = %+v, want one call with outbox=/outbox/1919 ref=%s", fc.RelayBundleCalls, branch)
	}
	if len(fc.CreateDraftPRCalls) != 1 {
		t.Fatalf("CreateDraftPRCalls = %+v, want exactly 1", fc.CreateDraftPRCalls)
	}
	want := forge.CreateDraftPRCall{Title: "feat: add widget", Body: "Adds a widget.", Base: "main", Head: branch}
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

// TestSettle_GithubReadOnly_MissingPRIntentBlocksNotFails asserts a
// status=ready Box that left no PR-intent line blocks the hand-off before
// any real host write — no bundle relay (a real force-push to origin), no
// draft PR created, CI never watched. The nudge-exhausted hand-off is left
// visibly not-done (#2046): the issue stays agent-in-progress rather than
// being marked agent-complete, which reads as merged/green to an operator
// (the exact #2036 confusion), yet is never demoted to agent-failed either.
// Parsing the PR-intent line first, ahead of the relay, matters here:
// relaying is a genuine side effect against the remote, and a box that left
// no PR-intent has nothing worth relaying a branch for.
func TestSettle_GithubReadOnly_MissingPRIntentBlocksNotFails(t *testing.T) {
	const issNum = "1919"
	branch := "agent/issue-1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: issNum, Landing: branch, Status: "ready", Note: "ok"},
		// PRIntentFound left false: the box's log had no PR-intent line.
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := New(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	if len(fc.RelayBundleCalls) != 0 {
		t.Errorf("RelayBundle must not be called with no PR-intent line, got %+v", fc.RelayBundleCalls)
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
		Success:       true,
		OutcomeFound:  true,
		Outcome:       outcome.Outcome{Issue: issNum, Landing: branch, Status: "ready", Note: "ok"},
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
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: issNum, Landing: prURL, Status: "ready", Note: "ok"},
	}

	c := baseConfig() // Config.ReadOnly defaults false
	s := New(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
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
		Success:       true,
		OutcomeFound:  true,
		Outcome:       outcome.Outcome{Issue: issNum, Landing: hostileLanding, Status: "ready", Note: "ok"},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := New(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
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
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: issNum, Landing: hostileLanding, Status: "merged", Note: "ok"},
	}

	c := baseConfig()
	c.ReadOnly = true
	s := New(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
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
