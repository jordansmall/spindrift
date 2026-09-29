package butler

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/ledger/ledgertest"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/testutil"
	"spindrift.dev/launcher/internal/usage"
)

// claimButlerChore reads chore's current tip and appends a Claimed state on
// top of it, the same handoff a real run's claim step performs before it
// ever dispatches a Box.
func claimButlerChore(t *testing.T, backend ledger.Backend, choreName string, start time.Time) ledger.Tip {
	t.Helper()
	tip, err := backend.Read(choreName)
	if err != nil {
		t.Fatalf("Read(%q): %v", choreName, err)
	}
	claim, err := ledger.Claim(backend, choreName, tip, ledger.ClaimedBy{Host: "worker", Start: start})
	if err != nil {
		t.Fatalf("Claim(%q): %v", choreName, err)
	}
	return claim
}

func readyResult(intents ...string) dispatch.Result {
	r := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "butler-bugs", Status: outcome.StatusReady, Note: "swept"},
		},
	}
	if len(intents) > 0 {
		r.IssueIntentsFound = true
		r.IssueIntents = intents
	}
	return r
}

// (a) Filing: two intents file as two agent-butler-finding issues, never
// ready-for-agent, each body naming the Chore and the files its own dedup
// terms cover.
func TestSettleRun_FilesFindingsWithProvenanceLabelAndBacklink(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim := claimButlerChore(t, backend, "bugs", start)

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/501"

	scope := chore.Scope{Head: "headsha", NextCursor: "cursor2"}
	now := start.Add(time.Minute)
	s := newSettleRun(fc.AsIssueFiler(), backend, "bugs", claim, scope, func() time.Time { return now }, 0, promotion{}, dayRoom{})

	result := readyResult(
		`{"title":"bug in a.go","body":"repro","dedupTerms":["a.go:Foo"]}`,
		`{"title":"bug in b and c","body":"repro2","dedupTerms":["b/c.go:Bar","b/c.go:Baz"]}`,
	)

	s.settle(dispatch.NewFake(), result)

	if len(fc.PostIssueCalls) != 2 {
		t.Fatalf("want 2 PostIssue calls, got %d: %+v", len(fc.PostIssueCalls), fc.PostIssueCalls)
	}
	for _, call := range fc.PostIssueCalls {
		if len(call.Labels) != 1 || call.Labels[0] != "agent-butler-finding" {
			t.Errorf("PostIssue labels = %v, want [%s]", call.Labels, "agent-butler-finding")
		}
	}
	body0 := fc.PostIssueCalls[0].Body
	if !strings.Contains(body0, "butler's `bugs` Chore") {
		t.Errorf("body = %q, want the Chore named", body0)
	}
	if !strings.Contains(body0, "Files: `a.go`.") {
		t.Errorf("body = %q, want a.go named", body0)
	}
	body1 := fc.PostIssueCalls[1].Body
	if !strings.Contains(body1, "Files: `b/c.go`.") {
		t.Errorf("body = %q, want b/c.go named exactly once despite two dedup terms sharing it", body1)
	}
}

// (b) Done commit contents: after a clean run, the Chore's tip is a Done
// state carrying scope.Head/NextCursor, the filed URLs, and the
// Dispatcher's cumulative usage; settle's own return also reports the run
// landed, with the same counts.
func TestSettleRun_DoneCommitContents(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim := claimButlerChore(t, backend, "bugs", start)

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/501"

	scope := chore.Scope{Head: "headsha", NextCursor: "cursor2"}
	now := start.Add(time.Minute)
	s := newSettleRun(fc.AsIssueFiler(), backend, "bugs", claim, scope, func() time.Time { return now }, 0, promotion{}, dayRoom{})

	result := readyResult(
		`{"title":"first finding","body":"repro","dedupTerms":["a.go:Foo"]}`,
		`{"title":"second finding","body":"repro2","dedupTerms":["b.go:Bar"]}`,
	)

	d := dispatch.NewFake()
	d.CumulativeUsageResult = usage.Usage{InputTokens: 10, OutputTokens: 20, TotalCostUSD: 1.5, NumTurns: 3}

	got := s.settle(d, result)
	if !got.done || got.filed != 2 || got.promoted != 0 || got.dropped != 0 {
		t.Errorf("settled = %+v, want done=true filed=2 promoted=0 dropped=0", got)
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if tip.State.Phase != ledger.Done {
		t.Errorf("Phase = %q, want %q", tip.State.Phase, ledger.Done)
	}
	if tip.State.LastSwept != scope.Head {
		t.Errorf("LastSwept = %q, want %q", tip.State.LastSwept, scope.Head)
	}
	if tip.State.Cursor != scope.NextCursor {
		t.Errorf("Cursor = %q, want %q", tip.State.Cursor, scope.NextCursor)
	}
	wantFiled := []string{fc.PostIssueURL, fc.PostIssueURL}
	if !slices.Equal(tip.State.Filed, wantFiled) {
		t.Fatalf("Filed = %v, want %v", tip.State.Filed, wantFiled)
	}
	if tip.State.Usage != d.CumulativeUsageResult {
		t.Errorf("Usage = %+v, want %+v", tip.State.Usage, d.CumulativeUsageResult)
	}
	if tip.State.ClaimedBy != nil {
		t.Errorf("ClaimedBy = %+v, want nil on a Done state", tip.State.ClaimedBy)
	}
}

// TestSettleRun_ReportsChoreSettledNotIssue pins issue #3878: a butler run's
// terminal report.Record must carry the Chore, not num (the
// dispatchkey.Chore-shaped "butler-bugs"), since the report wire's Issue
// field means a tracker issue and a Chore is not one.
func TestSettleRun_ReportsChoreSettledNotIssue(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)

	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim := claimButlerChore(t, backend, "bugs", start)

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/501"

	scope := chore.Scope{Head: "headsha", NextCursor: "cursor2"}
	now := start.Add(time.Minute)
	s := newSettleRun(fc.AsIssueFiler(), backend, "bugs", claim, scope, func() time.Time { return now }, 0, promotion{}, dayRoom{})

	s.settle(dispatch.NewFake(), readyResult())

	recs := readRecords()
	if len(recs) != 1 {
		t.Fatalf("records: got %d, want 1: %+v", len(recs), recs)
	}
	if recs[0].Event != "settled" || recs[0].Key != dispatchkey.Chore("bugs") {
		t.Errorf("record = %+v, want event=settled chore=bugs issue=\"\"", recs[0])
	}
}

// (c) A partial filing failure still writes a done commit, recording only
// the intent that actually filed -- not the one whose PostIssue call failed
// -- so the next run doesn't refile the success and can still re-find the
// failure's own dedup keys via the backlog.
func TestSettleRun_PartialFilingFailure_RecordsOnlyWhatFiled(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim := claimButlerChore(t, backend, "bugs", start)

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/501"
	fc.PostIssueErrForTitle = map[string]error{"second finding": errors.New("create failed")}

	scope := chore.Scope{Head: "headsha", NextCursor: "cursor2"}
	now := start.Add(time.Minute)
	s := newSettleRun(fc.AsIssueFiler(), backend, "bugs", claim, scope, func() time.Time { return now }, 0, promotion{}, dayRoom{})

	result := readyResult(
		`{"title":"first finding","body":"repro","dedupTerms":["a.go:Foo"]}`,
		`{"title":"second finding","body":"repro2","dedupTerms":["b.go:Bar"]}`,
	)

	s.settle(dispatch.NewFake(), result)

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if tip.State.Phase != ledger.Done {
		t.Fatalf("Phase = %q, want %q", tip.State.Phase, ledger.Done)
	}
	if len(tip.State.Filed) != 1 || tip.State.Filed[0] != fc.PostIssueURL {
		t.Errorf("Filed = %v, want exactly [%s]", tip.State.Filed, fc.PostIssueURL)
	}
}

// (c2) A cap below the well-formed intent count files only the first
// maxFindingsPerSweep of them and records the overflow in the done commit's
// Dropped field and settle's own return.
func TestSettleRun_CapDropsOverflowAndRecordsDropped(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim := claimButlerChore(t, backend, "bugs", start)

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/501"

	scope := chore.Scope{Head: "headsha", NextCursor: "cursor2"}
	now := start.Add(time.Minute)
	s := newSettleRun(fc.AsIssueFiler(), backend, "bugs", claim, scope, func() time.Time { return now }, 2, promotion{}, dayRoom{})

	result := readyResult(
		`{"title":"first finding","body":"repro","dedupTerms":["a.go:Foo"]}`,
		`{"title":"second finding","body":"repro2","dedupTerms":["b.go:Bar"]}`,
		`{"title":"third finding","body":"repro3","dedupTerms":["c.go:Baz"]}`,
	)

	got := s.settle(dispatch.NewFake(), result)
	if got.dropped != 1 || got.filed != 2 {
		t.Errorf("settled = %+v, want filed=2 dropped=1", got)
	}

	if len(fc.PostIssueCalls) != 2 {
		t.Fatalf("want 2 PostIssue calls (capped), got %d: %+v", len(fc.PostIssueCalls), fc.PostIssueCalls)
	}
	for _, call := range fc.PostIssueCalls {
		if call.Title == "third finding" {
			t.Errorf("PostIssue called for %q, want it dropped by the cap", call.Title)
		}
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if tip.State.Dropped != 1 {
		t.Errorf("Dropped = %d, want 1", tip.State.Dropped)
	}
	if len(tip.State.Filed) != 2 {
		t.Errorf("Filed = %v, want 2 entries", tip.State.Filed)
	}
}

// (c3) A cap of 0 (the default) is no cap at all: every well-formed intent
// files and Dropped stays 0.
func TestSettleRun_ZeroCapFilesEverything(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim := claimButlerChore(t, backend, "bugs", start)

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/501"

	scope := chore.Scope{Head: "headsha", NextCursor: "cursor2"}
	now := start.Add(time.Minute)
	s := newSettleRun(fc.AsIssueFiler(), backend, "bugs", claim, scope, func() time.Time { return now }, 0, promotion{}, dayRoom{})

	result := readyResult(
		`{"title":"first finding","body":"repro","dedupTerms":["a.go:Foo"]}`,
		`{"title":"second finding","body":"repro2","dedupTerms":["b.go:Bar"]}`,
	)

	s.settle(dispatch.NewFake(), result)

	if len(fc.PostIssueCalls) != 2 {
		t.Fatalf("want 2 PostIssue calls, got %d", len(fc.PostIssueCalls))
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if tip.State.Dropped != 0 {
		t.Errorf("Dropped = %d, want 0", tip.State.Dropped)
	}
}

// (c4) A malformed payload never counts toward the cap.
func TestSettleRun_MalformedPayloadDoesNotCountTowardCap(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim := claimButlerChore(t, backend, "bugs", start)

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/501"

	scope := chore.Scope{Head: "headsha", NextCursor: "cursor2"}
	now := start.Add(time.Minute)
	s := newSettleRun(fc.AsIssueFiler(), backend, "bugs", claim, scope, func() time.Time { return now }, 1, promotion{}, dayRoom{})

	result := readyResult(
		`{"title":"first finding","body":"repro","dedupTerms":["a.go:Foo"]}`,
		`not valid json`,
		`{"title":"second finding","body":"repro2","dedupTerms":["b.go:Bar"]}`,
	)

	s.settle(dispatch.NewFake(), result)

	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("want 1 PostIssue call (cap=1, malformed uncounted), got %d: %+v", len(fc.PostIssueCalls), fc.PostIssueCalls)
	}
	if fc.PostIssueCalls[0].Title != "first finding" {
		t.Errorf("PostIssue title = %q, want %q", fc.PostIssueCalls[0].Title, "first finding")
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if tip.State.Dropped != 1 {
		t.Errorf("Dropped = %d, want 1 (second finding dropped by the cap)", tip.State.Dropped)
	}
}

// (d) A crashed run -- no outcome line at all, or a non-ready status like
// blocked -- files nothing and leaves the claim exactly where it was: no
// done commit, lastSwept/cursor unadvanced, and settle reports it never
// landed.
func TestSettleRun_CrashedRun_LeavesClaimUnadvanced(t *testing.T) {
	cases := []struct {
		name   string
		result dispatch.Result
	}{
		{
			name:   "no outcome line",
			result: dispatch.Result{Success: false},
		},
		{
			name: "blocked status",
			result: dispatch.Result{
				Success: true,
				Resolved: outcome.Resolved{
					Found:   true,
					Outcome: outcome.Outcome{Issue: "butler-bugs", Status: outcome.StatusBlocked, Note: "needs a human"},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
			start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

			firstClaim, err := ledger.Claim(backend, "bugs", ledger.Tip{}, ledger.ClaimedBy{Host: "worker", Start: start})
			if err != nil {
				t.Fatalf("initial Claim: %v", err)
			}
			if _, err := ledger.Finish(backend, "bugs", firstClaim, ledger.State{LastSwept: "prevhead", Cursor: "prevcursor"}, start.Add(time.Minute)); err != nil {
				t.Fatalf("initial Finish: %v", err)
			}

			claim := claimButlerChore(t, backend, "bugs", start.Add(2*time.Minute))

			fc := forge.NewFake()
			scope := chore.Scope{Head: "newhead", NextCursor: "newcursor"}
			now := start.Add(3 * time.Minute)
			s := newSettleRun(fc.AsIssueFiler(), backend, "bugs", claim, scope, func() time.Time { return now }, 0, promotion{}, dayRoom{})

			got := s.settle(dispatch.NewFake(), tc.result)
			if got.done {
				t.Errorf("settled = %+v, want done=false", got)
			}

			if len(fc.PostIssueCalls) != 0 {
				t.Errorf("PostIssueCalls = %+v, want none", fc.PostIssueCalls)
			}

			tip, err := backend.Read("bugs")
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if tip.Commit != claim.Commit {
				t.Fatalf("tip.Commit = %s, want it unchanged at the claim commit %s", tip.Commit, claim.Commit)
			}
			if tip.State.Phase != ledger.Claimed {
				t.Errorf("Phase = %q, want %q", tip.State.Phase, ledger.Claimed)
			}
			if tip.State.LastSwept != "prevhead" || tip.State.Cursor != "prevcursor" {
				t.Errorf("LastSwept/Cursor = %q/%q, want prevhead/prevcursor (scope's newhead/newcursor must not land)", tip.State.LastSwept, tip.State.Cursor)
			}
		})
	}
}

// (e) A crashed run -- no ready outcome line at all -- still warns about any
// rejected signal lines the result channel carried, since settle.LogRejectedSignals
// now runs as settle's first statement, ahead of both crash guards (issue
// #3990). Before this fix the warning lived inside settle.FileButlerFindings,
// reachable only once a run had already cleared the guards below, so a
// crashed run's rejected lines were dropped with no trace at all.
func TestSettleRun_CrashedRun_StillWarnsAboutRejectedSignals(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim := claimButlerChore(t, backend, "bugs", start)

	fc := forge.NewFake()
	scope := chore.Scope{Head: "headsha", NextCursor: "cursor2"}
	now := start.Add(time.Minute)
	s := newSettleRun(fc.AsIssueFiler(), backend, "bugs", claim, scope, func() time.Time { return now }, 0, promotion{}, dayRoom{})

	result := dispatch.Result{
		Success:              false,
		IssueIntentsRejected: outcome.Rejections{NonceMismatch: 1},
	}

	stderr := testutil.CaptureStderr(t, func() {
		got := s.settle(dispatch.NewFake(), result)
		if got.done {
			t.Errorf("settled = %+v, want done=false", got)
		}
	})

	want := "#butler-bugs: 1 nonce-mismatched issue-intent line(s) rejected"
	if !strings.Contains(stderr, want) {
		t.Errorf("stderr must warn about the rejected issue-intent line even on a crashed run; want substring %q, got: %q", want, stderr)
	}
}

// labelsForFinding returns the labels PostIssue was called with for the
// finding titled title, or nil if no such call happened.
func labelsForFinding(fc *forge.Fake, title string) []string {
	for _, call := range fc.PostIssueCalls {
		if call.Title == title {
			return call.Labels
		}
	}
	return nil
}

// (e) All four gates pass: the finding files carrying both labels, a body
// naming the class and quoting the reviewer's own concurrence, and the
// Ledger done commit records the URL in Promoted (issue #3880).
func TestSettleRun_Promotion_AllGatesPass(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim := claimButlerChore(t, backend, "bugs", start)

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/701"

	scope := chore.Scope{Head: "headsha", NextCursor: "cursor2"}
	now := start.Add(time.Minute)
	policy := promotion{enabled: true, classes: []string{"error-handling"}, maxFiles: 2, label: "ready-for-agent"}
	room := dayRoom{perDay: 1, chores: []string{"bugs"}, zone: time.UTC}
	s := newSettleRun(fc.AsIssueFiler(), backend, "bugs", claim, scope, func() time.Time { return now }, 0, policy, room)

	result := readyResult(`{"title":"promotable bug","body":"repro","dedupTerms":["a.go:Foo"],"class":"error-handling","concurrence":"looks like a real bug, agreed"}`)
	got := s.settle(dispatch.NewFake(), result)
	if got.promoted != 1 {
		t.Errorf("settled = %+v, want promoted=1", got)
	}

	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("want 1 PostIssue call, got %d", len(fc.PostIssueCalls))
	}
	call := fc.PostIssueCalls[0]
	if !slices.Contains(call.Labels, "agent-butler-finding") || !slices.Contains(call.Labels, "ready-for-agent") {
		t.Errorf("labels = %v, want both agent-butler-finding and ready-for-agent", call.Labels)
	}
	if !strings.Contains(call.Body, "Auto-promoted") || !strings.Contains(call.Body, "error-handling") || !strings.Contains(call.Body, "looks like a real bug, agreed") {
		t.Errorf("body = %q, want an auto-promoted note naming the class and quoting the concurrence", call.Body)
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !slices.Equal(tip.State.Promoted, []string{fc.PostIssueURL}) {
		t.Errorf("Promoted = %v, want [%s]", tip.State.Promoted, fc.PostIssueURL)
	}
}

// (e2) A Consumer-configured non-default work label (LABEL=agent-go) is what
// a promoted finding actually carries, not a hardcoded "ready-for-agent"
// (issue #3880).
func TestSettleRun_Promotion_CustomLabel(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim := claimButlerChore(t, backend, "bugs", start)

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/705"

	scope := chore.Scope{Head: "headsha", NextCursor: "cursor2"}
	now := start.Add(time.Minute)
	policy := promotion{enabled: true, classes: []string{"error-handling"}, maxFiles: 2, label: "agent-go"}
	room := dayRoom{perDay: 1, chores: []string{"bugs"}, zone: time.UTC}
	s := newSettleRun(fc.AsIssueFiler(), backend, "bugs", claim, scope, func() time.Time { return now }, 0, policy, room)

	result := readyResult(`{"title":"promotable bug","body":"repro","dedupTerms":["a.go:Foo"],"class":"error-handling","concurrence":"looks like a real bug, agreed"}`)
	s.settle(dispatch.NewFake(), result)

	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("want 1 PostIssue call, got %d", len(fc.PostIssueCalls))
	}
	call := fc.PostIssueCalls[0]
	if !slices.Contains(call.Labels, "agent-go") || slices.Contains(call.Labels, "ready-for-agent") {
		t.Errorf("labels = %v, want agent-go and not the ready-for-agent default", call.Labels)
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !slices.Equal(tip.State.Promoted, []string{fc.PostIssueURL}) {
		t.Errorf("Promoted = %v, want [%s]", tip.State.Promoted, fc.PostIssueURL)
	}
}

// (f) Any single gate failing alone files the finding unlabelled.
func TestSettleRun_Promotion_AnyGateFailingFilesUnlabelled(t *testing.T) {
	base := promotion{enabled: true, classes: []string{"error-handling"}, maxFiles: 2, label: "ready-for-agent"}
	baseRoom := dayRoom{perDay: 1, chores: []string{"bugs"}, zone: time.UTC}

	cases := []struct {
		name    string
		policy  promotion
		room    dayRoom
		payload string
	}{
		{
			"class not on the allow-list",
			base,
			baseRoom,
			`{"title":"f1","body":"b","dedupTerms":["a.go:X"],"class":"dead-code","concurrence":"agreed"}`,
		},
		{
			"empty class",
			base,
			baseRoom,
			`{"title":"f2","body":"b","dedupTerms":["a.go:X"],"class":"","concurrence":"agreed"}`,
		},
		{
			"files exceed the host limit despite an allow-listed class",
			func() promotion { p := base; p.maxFiles = 1; return p }(),
			baseRoom,
			`{"title":"f3","body":"b","dedupTerms":["a.go:X","b.go:Y"],"class":"error-handling","concurrence":"agreed"}`,
		},
		{
			"zero files",
			base,
			baseRoom,
			`{"title":"f4","body":"b","dedupTerms":[],"class":"error-handling","concurrence":"agreed"}`,
		},
		{
			// Validate rejects a whitespace-only Concurrence outright (issue
			// #3992); the decide table test covers concurred()'s trim alone.
			"empty concurrence",
			base,
			baseRoom,
			`{"title":"f5","body":"b","dedupTerms":["a.go:X"],"class":"error-handling","concurrence":""}`,
		},
		{
			"maxPromotionsPerDay defaults to zero room",
			base,
			dayRoom{perDay: 0, chores: []string{"bugs"}, zone: time.UTC},
			`{"title":"f7","body":"b","dedupTerms":["a.go:X"],"class":"error-handling","concurrence":"agreed"}`,
		},
		{
			"promotion off",
			func() promotion { p := base; p.enabled = false; return p }(),
			baseRoom,
			`{"title":"f8","body":"b","dedupTerms":["a.go:X"],"class":"error-handling","concurrence":"agreed"}`,
		},
		{
			"empty Label fails closed despite every other gate passing",
			func() promotion { p := base; p.label = ""; p.enabled = false; return p }(),
			baseRoom,
			`{"title":"f9","body":"b","dedupTerms":["a.go:X"],"class":"error-handling","concurrence":"agreed"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
			start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			claim := claimButlerChore(t, backend, "bugs", start)

			fc := forge.NewFake()
			fc.PostIssueURL = "https://example.com/issues/702"

			scope := chore.Scope{Head: "headsha", NextCursor: "cursor2"}
			now := start.Add(time.Minute)
			s := newSettleRun(fc.AsIssueFiler(), backend, "bugs", claim, scope, func() time.Time { return now }, 0, tc.policy, tc.room)

			result := readyResult(tc.payload)
			s.settle(dispatch.NewFake(), result)

			if len(fc.PostIssueCalls) != 1 {
				t.Fatalf("want 1 PostIssue call, got %d: %+v", len(fc.PostIssueCalls), fc.PostIssueCalls)
			}
			labels := fc.PostIssueCalls[0].Labels
			if len(labels) != 1 || labels[0] != "agent-butler-finding" {
				t.Errorf("labels = %v, want exactly [agent-butler-finding]", labels)
			}

			tip, err := backend.Read("bugs")
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if len(tip.State.Promoted) != 0 {
				t.Errorf("Promoted = %v, want none", tip.State.Promoted)
			}
		})
	}
}

// (g) Room of 1 with two eligible findings: only the first promotes.
func TestSettleRun_Promotion_RoomOfOnePromotesOnlyFirst(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim := claimButlerChore(t, backend, "bugs", start)

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/703"

	scope := chore.Scope{Head: "headsha", NextCursor: "cursor2"}
	now := start.Add(time.Minute)
	policy := promotion{enabled: true, classes: []string{"error-handling"}, maxFiles: 1, label: "ready-for-agent"}
	room := dayRoom{perDay: 1, chores: []string{"bugs"}, zone: time.UTC}
	s := newSettleRun(fc.AsIssueFiler(), backend, "bugs", claim, scope, func() time.Time { return now }, 0, policy, room)

	result := readyResult(
		`{"title":"first eligible","body":"b1","dedupTerms":["a.go:X"],"class":"error-handling","concurrence":"agreed one"}`,
		`{"title":"second eligible","body":"b2","dedupTerms":["b.go:Y"],"class":"error-handling","concurrence":"agreed two"}`,
	)
	s.settle(dispatch.NewFake(), result)

	if got := labelsForFinding(fc, "first eligible"); !slices.Contains(got, "ready-for-agent") {
		t.Errorf("first eligible labels = %v, want ready-for-agent", got)
	}
	if got := labelsForFinding(fc, "second eligible"); slices.Contains(got, "ready-for-agent") {
		t.Errorf("second eligible labels = %v, want no ready-for-agent (room spent)", got)
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(tip.State.Promoted) != 1 {
		t.Errorf("Promoted = %v, want exactly 1 entry", tip.State.Promoted)
	}
}

// (g2) A failed post must not spend the room: the second finding still
// promotes.
func TestSettleRun_Promotion_FailedPostReturnsRoomToLaterFinding(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim := claimButlerChore(t, backend, "bugs", start)

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/706"
	fc.PostIssueErrForTitle = map[string]error{"first eligible": errors.New("create failed")}

	scope := chore.Scope{Head: "headsha", NextCursor: "cursor2"}
	now := start.Add(time.Minute)
	policy := promotion{enabled: true, classes: []string{"error-handling"}, maxFiles: 1, label: "ready-for-agent"}
	room := dayRoom{perDay: 1, chores: []string{"bugs"}, zone: time.UTC}
	s := newSettleRun(fc.AsIssueFiler(), backend, "bugs", claim, scope, func() time.Time { return now }, 0, policy, room)

	result := readyResult(
		`{"title":"first eligible","body":"b1","dedupTerms":["a.go:X"],"class":"error-handling","concurrence":"agreed one"}`,
		`{"title":"second eligible","body":"b2","dedupTerms":["b.go:Y"],"class":"error-handling","concurrence":"agreed two"}`,
	)
	s.settle(dispatch.NewFake(), result)

	if got := labelsForFinding(fc, "second eligible"); !slices.Contains(got, "ready-for-agent") {
		t.Errorf("second eligible labels = %v, want ready-for-agent (the first's failed post must not spend the room)", got)
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !slices.Equal(tip.State.Promoted, []string{fc.PostIssueURL}) {
		t.Errorf("Promoted = %v, want exactly [%s] (only the second, successfully filed finding)", tip.State.Promoted, fc.PostIssueURL)
	}
}

// (h) The Box cannot widen its own promotion.
func TestSettleRun_Promotion_PayloadCannotWidenPolicy(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim := claimButlerChore(t, backend, "bugs", start)

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/704"

	scope := chore.Scope{Head: "headsha", NextCursor: "cursor2"}
	now := start.Add(time.Minute)
	policy := promotion{enabled: true, classes: []string{"error-handling"}, maxFiles: 1, label: "ready-for-agent"}
	room := dayRoom{perDay: 5, chores: []string{"bugs"}, zone: time.UTC}
	s := newSettleRun(fc.AsIssueFiler(), backend, "bugs", claim, scope, func() time.Time { return now }, 0, policy, room)

	result := readyResult(
		`{"title":"sneaky payload","body":"b","dedupTerms":["a.go:X"],"class":"dead-code","concurrence":"agreed","classes":["dead-code"],"maxFiles":999,"labels":["ready-for-agent"]}`,
	)
	s.settle(dispatch.NewFake(), result)

	labels := labelsForFinding(fc, "sneaky payload")
	if len(labels) != 1 || labels[0] != "agent-butler-finding" {
		t.Errorf("labels = %v, want exactly [agent-butler-finding] -- the payload's extra keys must not widen the policy", labels)
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(tip.State.Promoted) != 0 {
		t.Errorf("Promoted = %v, want none", tip.State.Promoted)
	}
}

// (i0) A multi-line Concurrence renders as one line in the promotion note.
func TestSettleRun_Promotion_ConcurrenceCollapsedToOneLine(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim := claimButlerChore(t, backend, "bugs", start)

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/707"

	scope := chore.Scope{Head: "headsha", NextCursor: "cursor2"}
	now := start.Add(time.Minute)
	policy := promotion{enabled: true, classes: []string{"error-handling"}, maxFiles: 1, label: "ready-for-agent"}
	room := dayRoom{perDay: 1, chores: []string{"bugs"}, zone: time.UTC}
	s := newSettleRun(fc.AsIssueFiler(), backend, "bugs", claim, scope, func() time.Time { return now }, 0, policy, room)

	result := readyResult(`{"title":"multi-line concurrence","body":"b","dedupTerms":["a.go:X"],"class":"error-handling","concurrence":"agreed,\n\nbut also:\n- one\n- two"}`)
	s.settle(dispatch.NewFake(), result)

	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("want 1 PostIssue call, got %d", len(fc.PostIssueCalls))
	}
	body := fc.PostIssueCalls[0].Body
	i := strings.Index(body, "**Auto-promoted**")
	if i < 0 {
		t.Fatalf("body = %q, want an Auto-promoted note", body)
	}
	rest := body[i:]
	wantNote := "**Auto-promoted** to `ready-for-agent` by the butler: class `error-handling` is on the `bugs` Chore's allow-list, it touches 1 file(s) (host limit 1), and the in-Box reviewer agreed: `agreed, but also: - one - two`"
	if !strings.HasPrefix(rest, wantNote+"\n\n") {
		t.Errorf("note region = %q, want it to start with the one-line note %q", rest, wantNote)
	}
}

// (i) promotion.decide in isolation: promotion off, an unlisted class, the
// file-count gate (both directions -- zero files and over the limit),
// missing/blank reviewer concurrence, no room left, and the promote case
// itself, asserting the labels it hands back (issue #3993). No case here
// hands decide a room closure -- room is a plain int argument now.
func TestPromotion_Decide(t *testing.T) {
	base := promotion{enabled: true, classes: []string{"error-handling"}, maxFiles: 2, label: "ready-for-agent"}
	baseFinding := settle.Finding{Class: "error-handling", DedupTerms: []string{"a.go:X"}, Concurrence: "agreed"}

	cases := []struct {
		name    string
		policy  promotion
		finding settle.Finding
		room    int
		want    decisionKind
	}{
		{
			"promotion off",
			func() promotion { p := base; p.enabled = false; return p }(),
			baseFinding,
			1,
			skip,
		},
		{
			"class not allow-listed",
			base,
			func() settle.Finding { f := baseFinding; f.Class = "dead-code"; return f }(),
			1,
			skip,
		},
		{
			"empty class",
			base,
			func() settle.Finding { f := baseFinding; f.Class = ""; return f }(),
			1,
			skip,
		},
		{
			"file limit exceeded",
			base,
			func() settle.Finding {
				f := baseFinding
				f.DedupTerms = []string{"a.go:X", "b.go:Y", "c.go:Z"}
				return f
			}(),
			1,
			skip,
		},
		{
			"zero files",
			base,
			func() settle.Finding { f := baseFinding; f.DedupTerms = nil; return f }(),
			1,
			skip,
		},
		{
			"missing concurrence",
			base,
			func() settle.Finding { f := baseFinding; f.Concurrence = ""; return f }(),
			1,
			skip,
		},
		{
			"whitespace-only concurrence",
			base,
			func() settle.Finding { f := baseFinding; f.Concurrence = "   \n\t "; return f }(),
			1,
			skip,
		},
		{
			"no room",
			base,
			baseFinding,
			0,
			skip,
		},
		{
			"promote",
			base,
			baseFinding,
			1,
			promote,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.policy.decide(tc.finding, tc.room)
			if got.kind != tc.want {
				t.Fatalf("decide().kind = %v, want %v (reason %q)", got.kind, tc.want, got.reason)
			}
			if tc.want == promote {
				if !slices.Equal(got.labels, []string{tc.policy.label}) {
					t.Errorf("decide().labels = %v, want [%s]", got.labels, tc.policy.label)
				}
			} else if got.labels != nil {
				t.Errorf("decide().labels = %v, want nil on skip", got.labels)
			}
		})
	}
}

// TestNewPromotion_Enabled pins newPromotion's "any one of the three unset
// reads as off" rule from its own doc comment: a positive per-day budget, a
// configured label, and at least one allow-listed class must all hold, or
// enabled is false regardless of the others.
func TestNewPromotion_Enabled(t *testing.T) {
	cases := []struct {
		name    string
		classes []string
		perDay  int
		label   string
		want    bool
	}{
		{"all set", []string{"error-handling"}, 1, "ready-for-agent", true},
		{"perDay zero", []string{"error-handling"}, 0, "ready-for-agent", false},
		{"label empty", []string{"error-handling"}, 1, "", false},
		{"classes empty", nil, 1, "ready-for-agent", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := newPromotion(tc.classes, 2, tc.perDay, tc.label)
			if got.enabled != tc.want {
				t.Errorf("newPromotion(...).enabled = %v, want %v", got.enabled, tc.want)
			}
		})
	}
}

// (i2) promotionNote renders raw Box-supplied markdown/HTML in Concurrence as
// literal code-span text (issue #3928), exercised through a real settle run
// so parseIssueIntent's sanitization actually runs before promotionNote sees
// it -- promotionNote itself only collapses to one line.
func TestPromotionNote_ConcurrenceMarkdownNeutralized(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"mention", "@octocat", "@octocat"},
		{"emphasis", "**bold** *it* _u_", "**bold** *it* _u_"},
		{"link", "[x](https://evil.example)", "[x](https://evil.example)"},
		{"bare url", "https://evil.example", "https://evil.example"},
		{"inline html", "<a href=x>y</a>", "<a href=x>y</a>"},
		{"backtick span-close attempt", "ok` @octocat `", "ok' @octocat '"},
		{"leading/trailing space trimmed", "  padded  ", "padded"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
			start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			claim := claimButlerChore(t, backend, "bugs", start)

			fc := forge.NewFake()
			fc.PostIssueURL = "https://example.com/issues/713"

			scope := chore.Scope{Head: "headsha", NextCursor: "cursor2"}
			now := start.Add(time.Minute)
			policy := promotion{enabled: true, classes: []string{"cls"}, maxFiles: 1, label: "ready-for-agent"}
			room := dayRoom{perDay: 1, chores: []string{"bugs"}, zone: time.UTC}
			s := newSettleRun(fc.AsIssueFiler(), backend, "bugs", claim, scope, func() time.Time { return now }, 0, policy, room)

			payload := `{"title":"t","body":"b","dedupTerms":["a.go:X"],"class":"cls","concurrence":` + jsonQuote(c.raw) + `}`
			result := readyResult(payload)
			s.settle(dispatch.NewFake(), result)

			if len(fc.PostIssueCalls) != 1 {
				t.Fatalf("want 1 PostIssue call, got %d", len(fc.PostIssueCalls))
			}
			body := fc.PostIssueCalls[0].Body
			wantFragment := "agreed: `" + c.want + "`"
			if !strings.Contains(body, wantFragment) {
				t.Errorf("body = %q, want it to contain %q", body, wantFragment)
			}
		})
	}
}

// doneAppendErr wraps a ledger.Backend and fails any Done-phase Append with a
// plain (non-CAS) error, simulating a push failure after a reservation
// already landed cleanly (issue #3926).
type doneAppendErr struct {
	ledger.Backend
	err error
}

func (w doneAppendErr) Append(choreName, old string, s ledger.State, at time.Time) (string, error) {
	if s.Phase == ledger.Done {
		return "", w.err
	}
	return w.Backend.Append(choreName, old, s, at)
}

// takeoverOnDone wraps a ledger.Backend and, on any Done-phase Append, first
// lands a rival Claim on the current tip before delegating -- so the done
// write's own compare-and-swap loses to a real takeover that happened after
// this run's reservation, rather than a synthetic error (issue #3926).
type takeoverOnDone struct {
	ledger.Backend
}

func (w takeoverOnDone) Append(choreName, old string, s ledger.State, at time.Time) (string, error) {
	if s.Phase == ledger.Done {
		tip, err := w.Backend.Read(choreName)
		if err != nil {
			return "", err
		}
		if _, err := ledger.Claim(w.Backend, choreName, tip, ledger.ClaimedBy{Host: "rival", Start: at}); err != nil {
			return "", err
		}
	}
	return w.Backend.Append(choreName, old, s, at)
}

// (j) The reservation Reserve writes before filing survives a non-CAS Finish
// failure: the promotion is already posted and labelled, so DayTotals must
// still count it even though the done commit never landed (issue #3926).
func TestSettleRun_Promotion_ReserveCountsDespiteDoneAppendFailure(t *testing.T) {
	inner := ledger.Local{Repo: ledgertest.NewRepo(t)}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim := claimButlerChore(t, inner, "bugs", start)

	backend := doneAppendErr{Backend: inner, err: errors.New("push failed")}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/710"

	scope := chore.Scope{Head: "headsha", NextCursor: "cursor2"}
	now := start.Add(time.Minute)
	policy := promotion{enabled: true, classes: []string{"error-handling"}, maxFiles: 1, label: "ready-for-agent"}
	room := dayRoom{perDay: 1, chores: []string{"bugs"}, zone: time.UTC}
	s := newSettleRun(fc.AsIssueFiler(), backend, "bugs", claim, scope, func() time.Time { return now }, 0, policy, room)

	result := readyResult(`{"title":"promotable","body":"b","dedupTerms":["a.go:X"],"class":"error-handling","concurrence":"agreed"}`)
	got := s.settle(dispatch.NewFake(), result)
	if got.done {
		t.Errorf("settled = %+v, want done=false (the push failed)", got)
	}

	if len(fc.PostIssueCalls) != 1 || !slices.Contains(fc.PostIssueCalls[0].Labels, "ready-for-agent") {
		t.Fatalf("PostIssueCalls = %+v, want one call carrying ready-for-agent", fc.PostIssueCalls)
	}

	totals, err := ledger.DayTotals(inner, "bugs", now)
	if err != nil {
		t.Fatalf("DayTotals: %v", err)
	}
	if totals.Promoted != 1 {
		t.Errorf("Promoted = %d, want 1 (the reservation, despite the lost done commit)", totals.Promoted)
	}
}

// (k) A rival takeover lands between this run's reservation and its done
// write: the done commit's own CAS legitimately loses, but the reservation
// still stands in the chain and must still count (issue #3926).
func TestSettleRun_Promotion_ReserveCountsDespiteTakeoverBeforeDone(t *testing.T) {
	inner := ledger.Local{Repo: ledgertest.NewRepo(t)}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim := claimButlerChore(t, inner, "bugs", start)

	backend := takeoverOnDone{Backend: inner}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/711"

	scope := chore.Scope{Head: "headsha", NextCursor: "cursor2"}
	now := start.Add(time.Minute)
	policy := promotion{enabled: true, classes: []string{"error-handling"}, maxFiles: 1, label: "ready-for-agent"}
	room := dayRoom{perDay: 1, chores: []string{"bugs"}, zone: time.UTC}
	s := newSettleRun(fc.AsIssueFiler(), backend, "bugs", claim, scope, func() time.Time { return now }, 0, policy, room)

	result := readyResult(`{"title":"promotable","body":"b","dedupTerms":["a.go:X"],"class":"error-handling","concurrence":"agreed"}`)
	s.settle(dispatch.NewFake(), result)

	if len(fc.PostIssueCalls) != 1 || !slices.Contains(fc.PostIssueCalls[0].Labels, "ready-for-agent") {
		t.Fatalf("PostIssueCalls = %+v, want one call carrying ready-for-agent", fc.PostIssueCalls)
	}

	totals, err := ledger.DayTotals(inner, "bugs", now)
	if err != nil {
		t.Fatalf("DayTotals: %v", err)
	}
	if totals.Promoted != 1 {
		t.Errorf("Promoted = %d, want 1 (the reservation, despite the takeover before done)", totals.Promoted)
	}
}

// (l) A takeover before Settle even starts: the claim this run holds is
// already stale by the time Reserve runs, so Reserve itself loses its CAS.
// The run must not promote (issue #3926).
func TestSettleRun_Promotion_StaleClaimReserveFailsNeverPromotes(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	staleClaim := claimButlerChore(t, backend, "bugs", start)
	claimButlerChore(t, backend, "bugs", start.Add(30*time.Minute))

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/712"

	scope := chore.Scope{Head: "headsha", NextCursor: "cursor2"}
	now := start.Add(time.Hour)
	policy := promotion{enabled: true, classes: []string{"error-handling"}, maxFiles: 1, label: "ready-for-agent"}
	room := dayRoom{perDay: 1, chores: []string{"bugs"}, zone: time.UTC}
	s := newSettleRun(fc.AsIssueFiler(), backend, "bugs", staleClaim, scope, func() time.Time { return now }, 0, policy, room)

	result := readyResult(`{"title":"promotable","body":"b","dedupTerms":["a.go:X"],"class":"error-handling","concurrence":"agreed"}`)
	got := s.settle(dispatch.NewFake(), result)
	if got.done {
		t.Errorf("settled = %+v, want done=false (the stale claim's own Finish must also lose)", got)
	}

	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("want 1 PostIssue call, got %d", len(fc.PostIssueCalls))
	}
	if labels := fc.PostIssueCalls[0].Labels; slices.Contains(labels, "ready-for-agent") {
		t.Errorf("labels = %v, want no ready-for-agent (Reserve lost its CAS)", labels)
	}

	totals, err := ledger.DayTotals(backend, "bugs", now)
	if err != nil {
		t.Fatalf("DayTotals: %v", err)
	}
	if totals.Promoted != 0 {
		t.Errorf("Promoted = %d, want 0 (Reserve never landed)", totals.Promoted)
	}
}

// (m) The ordinary success path reserves then finishes on top of the
// reservation: DayTotals must count the promotion exactly once.
func TestSettleRun_Promotion_SuccessNoDoubleCount(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim := claimButlerChore(t, backend, "bugs", start)

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/713"

	scope := chore.Scope{Head: "headsha", NextCursor: "cursor2"}
	now := start.Add(time.Minute)
	policy := promotion{enabled: true, classes: []string{"error-handling"}, maxFiles: 1, label: "ready-for-agent"}
	room := dayRoom{perDay: 1, chores: []string{"bugs"}, zone: time.UTC}
	s := newSettleRun(fc.AsIssueFiler(), backend, "bugs", claim, scope, func() time.Time { return now }, 0, policy, room)

	result := readyResult(`{"title":"promotable","body":"b","dedupTerms":["a.go:X"],"class":"error-handling","concurrence":"agreed"}`)
	got := s.settle(dispatch.NewFake(), result)
	if !got.done || got.promoted != 1 {
		t.Errorf("settled = %+v, want done=true promoted=1", got)
	}

	totals, err := ledger.DayTotals(backend, "bugs", now)
	if err != nil {
		t.Fatalf("DayTotals: %v", err)
	}
	if totals.Promoted != 1 {
		t.Errorf("Promoted = %d, want exactly 1 (not double-counted from the reservation beneath the done commit)", totals.Promoted)
	}
}

// (n) Nothing eligible in the sweep -- no reservation commit lands at all,
// only the claim and the done commit.
func TestSettleRun_Promotion_NoReservationCommitWhenNothingEligible(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim := claimButlerChore(t, backend, "bugs", start)

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/714"

	scope := chore.Scope{Head: "headsha", NextCursor: "cursor2"}
	now := start.Add(time.Minute)
	policy := promotion{enabled: true, classes: []string{"error-handling"}, maxFiles: 1, label: "ready-for-agent"}
	room := dayRoom{perDay: 0, chores: []string{"bugs"}, zone: time.UTC}
	s := newSettleRun(fc.AsIssueFiler(), backend, "bugs", claim, scope, func() time.Time { return now }, 0, policy, room)

	result := readyResult(`{"title":"promotable","body":"b","dedupTerms":["a.go:X"],"class":"error-handling","concurrence":"agreed"}`)
	s.settle(dispatch.NewFake(), result)

	entries, err := backend.History("bugs", start.Add(-time.Hour))
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("History = %d entries, want exactly 2 (claim, done -- no reservation)", len(entries))
	}
}

// jsonQuote renders s as a JSON string literal. Go's strconv.Quote escaping
// happens to match JSON's for the plain ASCII these test cases use.
func jsonQuote(s string) string {
	return strconv.Quote(s)
}
