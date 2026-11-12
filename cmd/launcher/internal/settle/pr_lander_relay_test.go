package settle

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/retry"
)

// handoffBranch is the agent branch of issue 1.
var handoffBranch = forge.AgentBranchName(testBranchPrefix, "1")

// handoffResult is a Box outcome of the given status that carries a PR-intent line.
func handoffResult(status string) *dispatch.Result {
	return &dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "1", Landing: handoffBranch, Status: status, Note: "stuck"},
		},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}
}

// allOf runs each check that is set, in order.
func allOf(cs ...checkFn) checkFn {
	return func(t *testing.T, fc *forge.Fake, d *dispatch.Fake) {
		t.Helper()
		for _, c := range cs {
			if c != nil {
				c(t, fc, d)
			}
		}
	}
}

func relayCalls(n int) checkFn {
	return func(t *testing.T, fc *forge.Fake, _ *dispatch.Fake) {
		t.Helper()
		if len(fc.RelayBundleCalls) != n {
			t.Errorf("RelayBundle calls = %d, want %d", len(fc.RelayBundleCalls), n)
		}
	}
}

func draftPRCalls(n int) checkFn {
	return func(t *testing.T, fc *forge.Fake, _ *dispatch.Fake) {
		t.Helper()
		if len(fc.CreateDraftPRCalls) != n {
			t.Errorf("CreateDraftPR calls = %d, want %d", len(fc.CreateDraftPRCalls), n)
		}
	}
}

// handoffRig is the state a read-only hand-off row shares between its setup,
// its wiring and its checks.
type handoffRig struct {
	outbox  string
	sleeps  []time.Duration
	onSleep func() // runs on every clock sleep, after it is recorded
	stop    func() // the operator's Terminate, once Settle exists
}

// setup returns a row's Config/fake setup: a Policy of retries retries, a
// clock that records its sleeps, and an outbox; mod then tweaks the fake.
func (g *handoffRig) setup(retries int, mod func(*testing.T, *forge.Fake, *Config)) func(*testing.T, *forge.Fake, *Config) {
	return func(t *testing.T, fc *forge.Fake, c *Config) {
		g.outbox, g.sleeps, g.onSleep = t.TempDir(), nil, nil
		c.OutboxDir = func(string) string { return g.outbox }
		c.Policy = retry.Policy{Max: retries, Unit: time.Second}
		c.Clock = dispatch.Clock{Now: time.Now, Sleep: func(d time.Duration) {
			g.sleeps = append(g.sleeps, d)
			if g.onSleep != nil {
				g.onSleep()
			}
		}}
		if mod != nil {
			mod(t, fc, c)
		}
	}
}

// readOnly is setup for a read-only Box, whose host relays the bundle in and
// opens the draft PR at testPR.
func (g *handoffRig) readOnly(retries int, mod func(*testing.T, *forge.Fake, *Config)) func(*testing.T, *forge.Fake, *Config) {
	return g.setup(retries, func(t *testing.T, fc *forge.Fake, c *Config) {
		c.ReadOnly = true
		c.BaseBranch = "main"
		fc.CreateDraftPRURL = testPR
		if mod != nil {
			mod(t, fc, c)
		}
	})
}

func (g *handoffRig) captureStop(_ *forge.Fake, _ *dispatch.Fake, mark func()) { g.stop = mark }

// stopAfterRelay lands the operator's stop in the first clock sleep once the
// relay has been attempted, so it falls inside the relay's backoff and never
// in an earlier gate or confirm sleep.
func (g *handoffRig) stopAfterRelay(fc *forge.Fake) {
	g.onSleep = func() {
		if len(fc.RelayBundleCalls) > 0 {
			g.stop()
		}
	}
}

func (g *handoffRig) sleptAtLeast(n int) checkFn {
	return func(t *testing.T, _ *forge.Fake, _ *dispatch.Fake) {
		t.Helper()
		if len(g.sleeps) < n {
			t.Errorf("clock sleeps = %v, want at least %d", g.sleeps, n)
		}
	}
}

func (g *handoffRig) slept(n int) checkFn {
	return func(t *testing.T, _ *forge.Fake, _ *dispatch.Fake) {
		t.Helper()
		if len(g.sleeps) != n {
			t.Errorf("clock sleeps = %v, want %d", g.sleeps, n)
		}
	}
}

func (g *handoffRig) bundle(fate bundleFate) checkFn {
	return func(t *testing.T, _ *forge.Fake, _ *dispatch.Fake) {
		t.Helper()
		if gone := !bundleExists(t, g.outbox); gone != (fate == bundleDropped) {
			t.Errorf("bundle removed = %v, want fate %v", gone, fate)
		}
	}
}

// bundled seeds the Box's bundle in the outbox.
func (g *handoffRig) bundled(t *testing.T, _ *forge.Fake, _ *Config) {
	t.Helper()
	writeBundle(t, g.outbox)
}

func commentsLack(sub string) checkFn {
	return func(t *testing.T, fc *forge.Fake, _ *dispatch.Fake) {
		t.Helper()
		for _, c := range fc.CommentCalls {
			if strings.Contains(c.Body, sub) {
				t.Errorf("comment %q must not contain %q", c.Body, sub)
			}
		}
	}
}

// relayCases are the rows where a read-only Box's bundle is relayed in ahead
// of the PR landing: the retry budget the relay shares, the failures it never
// retries, and a stop landing in its backoff.
func relayCases() []prCase {
	g := &handoffRig{}
	parked := Settlement{Verdict: ForReason(dispatchrecord.ReasonRelayFailed), Note: "bundle relay failed; work preserved in outbox"}
	handoffBlocked := Settlement{Verdict: ForReason(dispatchrecord.ReasonHandoffBlocked)}
	boxBlocked := Settlement{Verdict: ForReason(dispatchrecord.ReasonBlocked), Note: "stuck"}
	failed := []string{"agent-failed"}
	notLive := []string{"agent-in-progress", "agent-complete"}
	errBlip := errors.New("ssh auth blip")
	retryStep := func(n, of int, err error) Step {
		return stepAt(handoffBranch, StatusRelayRetry, fmt.Sprintf("attempt=%d/%d  !! %v", n, of, err))
	}
	flipThenMerge := []string{"MarkReady:" + testPR, "Merge:" + testPR}
	recoverComment := func(err error) []string {
		return []string{"bundle relay failed: relay bundle failed: " + err.Error() + ". The Box's bundle is preserved in the outbox; run `spindrift recover 1`"}
	}
	blipRelay := func(_ *testing.T, fc *forge.Fake, _ *Config) { fc.RelayBundleErr = errBlip }
	stoppedRelay := func(_ *testing.T, fc *forge.Fake, _ *Config) {
		fc.RelayBundleErr = errBlip
		g.stopAfterRelay(fc)
	}
	scripted := make([]error, 4)
	for i := range scripted {
		scripted[i] = fmt.Errorf("relay failure %d", i+1)
	}

	cases := []prCase{
		{
			// Pins issue #4649 on the draft-PR mediation path.
			name: "a ready relay that fails transiently is retried, then the draft PR opens", shape: shapeReadOnly, noPR: true,
			wantStatuses: []Status{detailOnly, StatusRelayRetry, StatusRelayRetry, StatusVerifiedMerged},
			result:       handoffResult("ready"),
			setup:        g.readOnly(3, func(_ *testing.T, fc *forge.Fake, _ *Config) { fc.RelayBundleErrs = failThenSucceed(2) }),
			want:         merged, wantLabels: complete, wantMerged: testPR, wantCalls: flipThenMerge,
			wantSteps: []Step{retryStep(1, 3, errBlip), retryStep(2, 3, errBlip)},
			check:     allOf(relayCalls(3), draftPRCalls(1), g.sleptAtLeast(2)),
		},
		{
			// Issue #4651: the Box's bundle stays in the outbox for `spindrift recover`.
			name:         "a ready relay that fails every attempt parks failed with the last error and the bundle preserved",
			wantStatuses: []Status{detailOnly, StatusRelayRetry, StatusRelayRetry, StatusRelayRetry, StatusRelayFailed},
			shape:        shapeReadOnly, noPR: true, result: handoffResult("ready"),
			setup: g.readOnly(3, func(t *testing.T, fc *forge.Fake, c *Config) {
				g.bundled(t, fc, c)
				fc.RelayBundleErrs = scripted
			}),
			want: parked, wantPRClosed: true, wantLabels: failed, wantNoLabels: notLive, wantComments: recoverComment(scripted[3]),
			wantSteps: []Step{
				retryStep(1, 3, scripted[0]), retryStep(2, 3, scripted[1]), retryStep(3, 3, scripted[2]),
				stepAt(handoffBranch, StatusRelayFailed, "!! relay bundle failed: "+scripted[3].Error()),
			},
			check: allOf(relayCalls(4), draftPRCalls(0), g.bundle(bundleKept)),
		},
		{
			name: "a ready relay with no retry budget is attempted once", shape: shapeReadOnly, noPR: true,
			wantStatuses: []Status{detailOnly, StatusRelayFailed},
			result:       handoffResult("ready"), setup: g.readOnly(0, blipRelay),
			want: parked, wantPRClosed: true, wantLabels: failed, wantNoLabels: notLive, wantComments: recoverComment(errBlip),
			check: allOf(relayCalls(1), draftPRCalls(0), g.slept(0)),
		},
		{
			// Issue #5411: a Workflows-permission refusal parks with the cause and
			// both remedies, not the generic "retry relaying" advice a retry cannot honour.
			name: "a ready relay refused for workflow scope parks at once with the scope remedy", shape: shapeReadOnly, noPR: true,
			wantStatuses: []Status{detailOnly, StatusRelayFailed},
			result:       handoffResult("ready"),
			setup: g.readOnly(2, func(_ *testing.T, fc *forge.Fake, _ *Config) {
				fc.RelayBundleErr = fmt.Errorf("git push: %w", forge.ErrWorkflowPermissionDenied)
			}),
			want: parked, wantPRClosed: true, wantLabels: failed, wantNoLabels: notLive,
			wantComments: []string{"bundle relay failed: relay bundle failed: git push: workflow permission denied. The Box's bundle is preserved in the outbox; " + forge.WorkflowPermissionRemedy("1")},
			check:        allOf(relayCalls(1), g.slept(0), commentsLack("to retry relaying")),
		},
		{
			// A Box that wrote no bundle has nothing preserved to recover.
			name: "a ready relay whose bundle is missing stays blocked in progress without a retry", shape: shapeReadOnly, noPR: true,
			wantStatuses: []Status{detailOnly, StatusMergeBlocked},
			result:       handoffResult("ready"),
			setup: g.readOnly(2, func(_ *testing.T, fc *forge.Fake, _ *Config) {
				fc.RelayBundleErr = fmt.Errorf("outbox: %w", forge.ErrBundleNotFound)
			}),
			want: handoffBlocked, wantPRClosed: true, wantLabels: []string{"agent-in-progress"}, wantNoLabels: append([]string{"agent-complete"}, failed...),
			wantComments: []string{"merge blocked"},
			check:        allOf(relayCalls(1), g.slept(0)),
		},
		{
			// Issue #4649: the retries are bounded and the last error still reaches the log.
			name: "a blocked hand-off's relay is retried to the bound and logs the last error", shape: shapeReadOnly, noPR: true,
			wantStatuses: []Status{detailOnly, StatusBlocked, StatusRelayRetry, StatusRelayRetry, StatusRelayRetry},
			result:       handoffResult("blocked"), setup: g.readOnly(3, blipRelay),
			want: boxBlocked, wantPRClosed: true, wantLabels: failed, wantComments: []string{"stuck"},
			wantStderr:   []string{"could not relay blocked-hand-off bundle", errBlip.Error()},
			wantNoStderr: []string{"<nil>"},
			check:        allOf(relayCalls(4), draftPRCalls(0)),
		},
		{
			// A stop landing in the relay's backoff must surface as an abandon, not
			// be wrapped into a landing failure that demotes the released issue (#3523).
			name: "a stop during a ready relay's backoff abandons without opening the draft PR", shape: shapeReadOnly, noPR: true,
			wantStatuses: []Status{detailOnly, StatusRelayRetry},
			result:       handoffResult("ready"), setup: g.readOnly(3, stoppedRelay), midCall: g.captureStop,
			wantAbandoned: true, check: allOf(relayCalls(1), draftPRCalls(0)),
		},
		{
			name: "a stop during a recovered branch's relay backoff abandons without an error", shape: shapeReadOnly, noPR: true,
			wantStatuses: []Status{StatusRelayRetry},
			entry:        entryRelayed,
			setup:        g.readOnly(3, stoppedRelay), midCall: g.captureStop,
			wantAbandoned: true, check: allOf(relayCalls(1), draftPRCalls(0)),
		},
		{
			name:         "a stop during a blocked hand-off's relay backoff is logged as abandoned, not as a failure",
			wantStatuses: []Status{detailOnly, StatusBlocked, StatusRelayRetry},
			shape:        shapeReadOnly, noPR: true, result: handoffResult("blocked"),
			setup: g.readOnly(3, stoppedRelay), midCall: g.captureStop,
			// The blocked outcome is recorded ahead of the relay; the stop only withholds the blocked note.
			want: boxBlocked, wantPRClosed: true, wantLabels: failed,
			wantStderr: []string{"blocked-hand-off relay abandoned"}, wantNoStderr: []string{"could not relay", "?? #"},
			check: relayCalls(1),
		},
		{
			// Issue #4649 against #3523: the relay after a conflict-resolve must not
			// be wrapped into errLandingNeverGreen and demote the released issue.
			name: "a stop during the relay that follows a conflict-resolve abandons", shape: shapeReadOnly,
			wantStatuses: []Status{detailOnly, StatusRebaseRetry, StatusConflictResolve, StatusRelayRetry},
			maxRebase:    3, mergeErrs: []error{forge.ErrMergeConflict}, rebaseErr: forge.ErrMergeConflict,
			setup: g.setup(3, stoppedRelay), midCall: g.captureStop,
			wantAbandoned: true, wantResolves: 1, wantRebases: 1, check: relayCalls(1),
			wantCalls: []string{"MarkReady:" + testPR, "Merge:" + testPR, "MarkDraft:" + testPR},
		},
		{
			// Issue #4649: the abandon is immediate, instead of logging fix-relay-failed
			// and sleeping through the no-op check's confirm poll.
			name: "a stop during a fix pass's relay backoff abandons without the confirm pause", shape: shapeReadOnly,
			wantStatuses: []Status{detailOnly, detailOnly, StatusRelayRetry},
			maxFix:       3, checks: []forge.RollupState{red, red},
			setup: g.setup(3, func(_ *testing.T, fc *forge.Fake, c *Config) {
				fc.SetHeadCommitSHAs(testPR, []string{"sha-1", "sha-1", "sha-1", "sha-1"})
				stoppedRelay(nil, fc, c)
			}),
			midCall: g.captureStop, wantAbandoned: true, wantFixes: 1,
			// The red first poll sleeps nothing, so the lone backoff slice is the only
			// sleep; a second one is the no-op check's confirm poll.
			check: allOf(relayCalls(1), g.slept(1)),
		},
	}
	return cases
}

// blockedHandoffCases are the rows where a Box reaches IF BLOCKED: a read-only
// Box's branch is relayed in and its draft PR opened as additive work beside
// the blocked report (issue #1933), never in place of it, while a read-write
// Box pushed in-box and the host mediates nothing.
func blockedHandoffCases() []prCase {
	g := &handoffRig{}
	boxBlocked := Settlement{Verdict: ForReason(dispatchrecord.ReasonBlocked), Note: "stuck"}
	withDraftPR := Settlement{Verdict: boxBlocked.Verdict, PRURL: testPR, Note: "stuck"}
	failed := []string{"agent-failed"}
	readOnly := g.readOnly(0, nil)
	withIntent := func(intent string) *dispatch.Result {
		r := handoffResult("blocked")
		r.PRIntent, r.PRIntentFound = intent, intent != ""
		return r
	}
	bundleErr := func(err error) func(*testing.T, *forge.Fake, *Config) {
		return g.readOnly(0, func(_ *testing.T, fc *forge.Fake, _ *Config) { fc.RelayBundleErr = err })
	}
	// blocked is a read-only Box's blocked outcome reaching the hand-off.
	blocked := func(c prCase) prCase {
		c.noPR = true
		if c.shape == shapeGithub {
			c.shape = shapeGithubReadOnly
		}
		if c.result == nil {
			c.result = handoffResult("blocked")
		}
		if c.setup == nil {
			c.setup = readOnly
		}
		if c.want == (Settlement{}) {
			c.want = boxBlocked
		}
		c.wantPRClosed, c.wantLabels = true, failed
		c.wantStatuses = append([]Status{detailOnly, StatusBlocked}, c.wantStatuses...)
		return c
	}
	// unmediated is a blocked outcome of a Box that can write its tracker: the host posts nothing for it.
	unmediated := func(c prCase) prCase {
		c.noPR, c.shape = true, shapeGithubReadOnly
		c.wantPRClosed, c.wantLabels = true, failed
		c.wantStatuses = []Status{detailOnly, StatusBlocked}
		return c
	}
	const unwritableNote = "stopped after the migration; tests still red"
	unwritable := func(r outcome.Resolved) *dispatch.Result { return &dispatch.Result{Resolved: r} }
	noteOnIssue := func(t *testing.T, fc *forge.Fake, _ *dispatch.Fake) {
		t.Helper()
		for _, c := range fc.CommentCalls {
			if c.Body != "" && c.Num != "1" {
				t.Errorf("note posted on #%s, want #1", c.Num)
			}
		}
	}

	return []prCase{
		blocked(prCase{
			name:    "a blocked read-only hand-off relays the bundle and opens a draft PR that closes the issue",
			tracker: backend.GitHub,
			want:    withDraftPR,
			check:   allOf(g.relayedBundle(), openedDraftPR("feat: add widget", "Adds a widget.\n\nCloses #1")), wantComments: []string{"stuck"},
		}),
		blocked(prCase{
			name:   "a blocked hand-off's PR intent that already closes the issue gets no second reference",
			result: withIntent("feat: add widget\n\nAdds a widget. Closes #1"),
			want:   withDraftPR,
			check:  openedDraftPR("feat: add widget", "Adds a widget. Closes #1"), wantComments: []string{"stuck"},
		}),
		// A local tracker closes issues through its own axis (ADR 0029), not GitHub's auto-close on merge.
		blocked(prCase{
			name: "a blocked hand-off onto a local tracker injects no closing keyword", shape: shapeLocalReadOnly,
			want:  withDraftPR,
			check: openedDraftPR("feat: add widget", "Adds a widget."), wantComments: []string{"stuck"},
		}),
		blocked(prCase{
			name:   "a blocked hand-off without a PR intent relays the branch and opens no draft PR",
			result: withIntent(""),
			check:  allOf(g.relayedBundle(), draftPRCalls(0)), wantComments: []string{"stuck"},
		}),
		blocked(prCase{
			name:  "a blocked hand-off whose bundle is absent logs it as benign",
			setup: bundleErr(forge.ErrBundleNotFound), check: allOf(relayCalls(1), draftPRCalls(0)), wantComments: []string{"stuck"},
			wantStderr:   []string{".. #1:", "no blocked-hand-off bundle to relay"},
			wantNoStderr: []string{"could not relay blocked-hand-off bundle", "?? #1"},
		}),
		blocked(prCase{
			name: "a blocked hand-off whose draft PR create fails stays blocked", wantComments: []string{"stuck"},
			wantStatuses: []Status{StatusDraftPRFinalLookup},
			setup: g.readOnly(0, func(_ *testing.T, fc *forge.Fake, _ *Config) {
				fc.CreateDraftPRErr = errors.New("gh pr create: a pull request already exists")
			}),
			check:        relayCalls(1),
			wantStderr:   []string{"could not create draft PR for blocked hand-off", "gh pr create: a pull request already exists"},
			wantNoStderr: []string{"<nil>"},
		}),
		unmediated(prCase{
			name: "a blocked read-write box is not relayed or noted by the host", tracker: backend.GitHub,
			result: handoffResult("blocked"), want: boxBlocked,
			check: allOf(relayCalls(0), draftPRCalls(0)),
		}),
		unmediated(prCase{
			name: "a blocked read-write box on a tracker it cannot write has its note posted by the host", tracker: backend.Jira,
			result: unwritable(outcome.Resolved{Found: true, Outcome: outcome.Outcome{Issue: "1", Status: "blocked", Note: unwritableNote}}),
			want:   Settlement{Verdict: ForReason(dispatchrecord.ReasonBlocked), Note: unwritableNote}, wantComments: []string{unwritableNote},
			check: noteOnIssue,
		}),
		// The demoted-resolved path posts through the tracker directly; an unwritable tracker must see one note, not one from each path.
		unmediated(prCase{
			name: "a demoted already-resolved outcome on an unwritable tracker posts its note once", tracker: backend.Jira,
			result: unwritable(outcome.Resolved{
				Found: true, Provenance: outcome.ProvenanceSynthetic,
				Outcome:         outcome.Outcome{Issue: "1", Status: outcome.StatusBlocked, Note: "agent reported already-resolved but 2 commits exist on agent/issue-1; treating as blocked"},
				SelfReportFound: true, SelfReport: outcome.SelfReport{Status: outcome.StatusAlreadyResolved},
			}),
			want:         Settlement{Verdict: boxBlocked.Verdict, Note: "agent reported already-resolved but 2 commits exist on agent/issue-1; treating as blocked"},
			wantComments: []string{"treating as blocked"},
		}),
	}
}

// assertOutboxParked pins issues #4651 (an exhausted host-side relay) and #5144
// (a draft-PR create failure after a successful relay): the bundle is preserved
// in the outbox, so the issue moves agent-in-progress -> agent-failed with
// exactly one comment pointing at `spindrift recover`, and never the old
// merge-blocked comment.
func assertOutboxParked(t *testing.T, fc *forge.Fake, num string) {
	t.Helper()
	iss, _ := fc.Issue(num)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("an outbox park must park the issue agent-failed; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-in-progress") {
		t.Errorf("agent-in-progress must be cleared by the transition; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("an outbox park must NOT carry agent-complete (#2036); labels=%v", iss.Labels)
	}
	// postUsageComment adds one empty-bodied comment under dispatch.NewFake, so
	// count CommentCalls directly: the parking comment plus at most that one.
	var parked []string
	for _, c := range fc.CommentCalls {
		if c.Body != "" {
			parked = append(parked, c.Body)
		}
	}
	if len(parked) != 1 || len(fc.CommentCalls) > 2 {
		t.Fatalf("expected exactly one parking comment, got %d of %d calls: %+v", len(parked), len(fc.CommentCalls), fc.CommentCalls)
	}
	body := parked[0]
	if strings.Contains(body, "merge blocked") {
		t.Errorf("comment must not be the merge-blocked one: %q", body)
	}
	for _, want := range []string{"spindrift recover " + num, "outbox"} {
		if !strings.Contains(body, want) {
			t.Errorf("comment %q must contain %q", body, want)
		}
	}
}
