package butler

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/ledger/ledgertest"
	"spindrift.dev/launcher/internal/outcome"
)

// intentsDispatcher returns a ready Box whose run files the given raw
// issue-intent JSON lines.
func intentsDispatcher(intents ...string) *dispatch.Fake {
	d := dispatch.NewFake()
	d.RunResult = dispatch.Succeeded(dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "butler-bugs", Status: outcome.StatusReady, Note: "swept"},
		},
		IssueIntentsFound: true,
		IssueIntents:      intents,
	})
	return d
}

func skipLogIntent(title, class, concurrence string, files ...string) string {
	terms := make([]string, len(files))
	for i, f := range files {
		terms[i] = fmt.Sprintf("%q", f+":Foo")
	}
	return fmt.Sprintf(`{"title":%q,"body":"repro","dedupTerms":[%s],"class":%q,"concurrence":%q}`,
		title, strings.Join(terms, ","), class, concurrence)
}

// sweepLog runs one "bugs" sweep and returns what settle printed.
func sweepLog(t *testing.T, policy Policy, tree fakeTree, d *dispatch.Fake, pf *fakePatchForge, fc *forge.Fake) string {
	t.Helper()
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	newBox := func(c dispatch.Chore) dispatch.Dispatcher { return d }
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, policy, func() time.Time { return now })
	if pf != nil {
		r = r.WithPatchForge(pf, &fakePatchGate{})
	}
	var err error
	logs := captureStdout(t, func() {
		_, err = r.Sweep([]string{"bugs"})
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	return logs
}

func skipLogPolicy() Policy {
	policy := testRunPolicy(noRunEvery, "bugs")
	policy.Chores = withClasses(policy.Chores, "bugs", "docs-drift")
	policy.PromotionMaxFiles = 3
	policy.Budgets.MaxPromotionsPerDay = 1
	return policy
}

func TestSettleRun_LogsWhyAFindingWasNotPromoted(t *testing.T) {
	cases := []struct {
		name    string
		intents []string
		want    string
	}{
		{
			name:    "class not allow-listed",
			intents: []string{skipLogIntent("f", "other", "agreed", "a.go")},
			want:    "class not allow-listed",
		},
		{
			name:    "file count outside host limit",
			intents: []string{skipLogIntent("f", "docs-drift", "agreed", "a.go", "b.go", "c.go", "d.go")},
			want:    "file count outside host limit",
		},
		{
			name:    "no reviewer concurrence",
			intents: []string{skipLogIntent("f", "docs-drift", "", "a.go")},
			want:    "no reviewer concurrence",
		},
		{
			name: "no room",
			intents: []string{
				skipLogIntent("first", "docs-drift", "agreed", "a.go"),
				skipLogIntent("second", "docs-drift", "agreed", "b.go"),
			},
			want: "no room",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := forge.NewFake()
			fc.PostIssueURL = "https://example.com/issues/1"
			logs := sweepLog(t, skipLogPolicy(), fakeTree{head: "headsha", files: []string{"a.go"}}, intentsDispatcher(tc.intents...), nil, fc)
			if n := strings.Count(logs, "status=promote-skipped"); n != 1 {
				t.Fatalf("promote-skipped lines = %d, want 1; log = %q", n, logs)
			}
			if !strings.Contains(logs, "status=promote-skipped  note="+tc.want+"\n") {
				t.Errorf("log = %q, want promote-skipped with note=%s", logs, tc.want)
			}
			if strings.Contains(logs, "status=promotion-off") {
				t.Errorf("log = %q, want no promotion-off line with promotion on", logs)
			}
		})
	}
}

func TestSettleRun_PromotionOffLogsOneSweepLevelLine(t *testing.T) {
	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/1"
	policy := skipLogPolicy()
	policy.Budgets.MaxPromotionsPerDay = 0
	d := intentsDispatcher(
		skipLogIntent("first", "docs-drift", "agreed", "a.go"),
		skipLogIntent("second", "docs-drift", "agreed", "b.go"),
	)
	logs := sweepLog(t, policy, fakeTree{head: "headsha", files: []string{"a.go"}}, d, nil, fc)
	if n := strings.Count(logs, "status=promotion-off"); n != 1 {
		t.Errorf("promotion-off lines = %d, want 1; log = %q", n, logs)
	}
	if !strings.Contains(logs, "status=promotion-off  note="+promotionOffNote+"\n") {
		t.Errorf("log = %q, want the promotion-off note", logs)
	}
	if strings.Contains(logs, "status=promote-skipped") {
		t.Errorf("log = %q, want no per-finding promote-skipped with promotion off", logs)
	}
}

func TestSettleRun_PromotedFindingLogsNoSkip(t *testing.T) {
	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/1"
	d := intentsDispatcher(skipLogIntent("f", "docs-drift", "agreed", "a.go"))
	logs := sweepLog(t, skipLogPolicy(), fakeTree{head: "headsha", files: []string{"a.go"}}, d, nil, fc)
	if strings.Contains(logs, "status=promote-skipped") || strings.Contains(logs, "status=promotion-off") {
		t.Errorf("log = %q, want no skip line for a promoted finding", logs)
	}
}

func TestSettleRun_PatchedFindingLogsNoPromoteSkip(t *testing.T) {
	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9501"
	tree := fakeTree{head: "headsha", files: []string{"a.go"}, commitPatch: PatchCommit{Dir: "/repo", Ref: "refs/butler/patch"}}
	pf := &fakePatchForge{prefix: "agent/issue-", IssueLabeler: fc, draftURL: "https://example.com/pull/2"}
	logs := sweepLog(t, patchTestPolicy(), tree, patchableDispatcher("docs-drift"), pf, fc)
	if len(pf.draftCalls) != 1 {
		t.Fatalf("draftCalls = %v, want the patch to land", pf.draftCalls)
	}
	if strings.Contains(logs, "status=promote-skipped") {
		t.Errorf("log = %q, want no promote-skipped for a patched finding", logs)
	}
}

// A patch candidate whose landing fails falls back to promotion; when that
// also refuses, the skip is logged like any other. The patch class is not on
// the promotion allow-list, so the fallback fails the class gate.
func TestSettleRun_PatchLandingFailedFallbackSkipLogsReason(t *testing.T) {
	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9502"
	fc.SetIssue(forge.Issue{Number: "9502"})
	tree := fakeTree{head: "headsha", files: []string{"a.go"}, commitPatch: PatchCommit{Dir: "/repo", Ref: "refs/butler/patch"}}
	pf := &fakePatchForge{prefix: "agent/issue-", IssueLabeler: fc, draftErr: errors.New("boom")}
	policy := patchTestPolicy()
	policy.Chores = withClasses(policy.Chores, "bugs", "other")
	logs := sweepLog(t, policy, tree, patchableDispatcher("docs-drift"), pf, fc)
	if !strings.Contains(logs, "status=patch-failed") {
		t.Fatalf("log = %q, want the patch landing to fail", logs)
	}
	if !strings.Contains(logs, "status=promote-skipped  note=class not allow-listed\n") {
		t.Errorf("log = %q, want promote-skipped from the failed fallback", logs)
	}
}

// With promotion off, the sweep-level line reports only a sweep that filed
// something: a finding that fails to post leaves nothing to report.
func TestSettleRun_PromotionOffLogsNothingWhenNoFindingFiled(t *testing.T) {
	fc := forge.NewFake()
	fc.PostIssueErr = errors.New("boom")
	policy := skipLogPolicy()
	policy.Budgets.MaxPromotionsPerDay = 0
	d := intentsDispatcher(skipLogIntent("f", "docs-drift", "agreed", "a.go"))
	logs := sweepLog(t, policy, fakeTree{head: "headsha", files: []string{"a.go"}}, d, nil, fc)
	if strings.Contains(logs, "status=promotion-off") {
		t.Errorf("log = %q, want no promotion-off line when nothing filed", logs)
	}
}

// A patch whose diff no longer applies is re-decided as a plain finding; when
// promotion then refuses it, the skip is logged once like any other.
func TestSettleRun_PatchApplyFailedSkipLogsReason(t *testing.T) {
	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9503"
	tree := fakeTree{head: "headsha", files: []string{"a.go"}, commitPatchErr: errors.New("does not apply")}
	pf := &fakePatchForge{prefix: "agent/issue-", IssueLabeler: fc}
	policy := patchTestPolicy()
	policy.Chores = withClasses(policy.Chores, "bugs", "other")
	logs := sweepLog(t, policy, tree, patchableDispatcher("docs-drift"), pf, fc)
	if !strings.Contains(logs, "status=patch-apply-failed") {
		t.Fatalf("log = %q, want the patch apply to fail", logs)
	}
	if n := strings.Count(logs, "status=promote-skipped"); n != 1 {
		t.Fatalf("promote-skipped lines = %d, want 1; log = %q", n, logs)
	}
	if !strings.Contains(logs, "status=promote-skipped  note=class not allow-listed\n") {
		t.Errorf("log = %q, want promote-skipped with note=class not allow-listed", logs)
	}
}
