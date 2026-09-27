package settle

import (
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/butler"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/testutil"
	"spindrift.dev/launcher/internal/usage"
)

// newButlerBareRepo builds an empty bare repo for a ledger.Local backend,
// same exec-git convention as ledger/local_test.go's newBareRepo.
func newButlerBareRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")

	dir := t.TempDir()
	bare := filepath.Join(dir, "repo.git")
	if out, err := exec.Command("git", "init", "--bare", "-q", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", bare, "config", "gc.auto", "0").CombinedOutput(); err != nil {
		t.Fatalf("git config gc.auto: %v: %s", err, out)
	}
	return bare
}

// claimButlerChore reads chore's current tip and appends a Claimed state on
// top of it, the same handoff a real run's claim step performs before it
// ever dispatches a Box.
func claimButlerChore(t *testing.T, backend ledger.Backend, chore string, start time.Time) ledger.Tip {
	t.Helper()
	tip, err := backend.Read(chore)
	if err != nil {
		t.Fatalf("Read(%q): %v", chore, err)
	}
	claim, err := ledger.Claim(backend, chore, tip, ledger.ClaimedBy{Host: "worker", Start: start})
	if err != nil {
		t.Fatalf("Claim(%q): %v", chore, err)
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
func TestButlerSettle_FilesFindingsWithProvenanceLabelAndBacklink(t *testing.T) {
	backend := ledger.Local{Repo: newButlerBareRepo(t)}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim := claimButlerChore(t, backend, "bugs", start)

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/501"

	scope := butler.Scope{Head: "headsha", NextCursor: "cursor2"}
	now := start.Add(time.Minute)
	s := NewButlerSettle(fc.AsIssueFiler(), backend, "bugs", claim, scope, func() time.Time { return now }, 0)

	result := readyResult(
		`{"title":"bug in a.go","body":"repro","dedupTerms":["a.go:Foo"]}`,
		`{"title":"bug in b and c","body":"repro2","dedupTerms":["b/c.go:Bar","b/c.go:Baz"]}`,
	)

	s.Settle(dispatch.NewFake(), "butler-bugs", 0, result)

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
// Dispatcher's cumulative usage.
func TestButlerSettle_DoneCommitContents(t *testing.T) {
	backend := ledger.Local{Repo: newButlerBareRepo(t)}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim := claimButlerChore(t, backend, "bugs", start)

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/501"

	scope := butler.Scope{Head: "headsha", NextCursor: "cursor2"}
	now := start.Add(time.Minute)
	s := NewButlerSettle(fc.AsIssueFiler(), backend, "bugs", claim, scope, func() time.Time { return now }, 0)

	result := readyResult(
		`{"title":"first finding","body":"repro","dedupTerms":["a.go:Foo"]}`,
		`{"title":"second finding","body":"repro2","dedupTerms":["b.go:Bar"]}`,
	)

	d := dispatch.NewFake()
	d.CumulativeUsageResult = usage.Usage{InputTokens: 10, OutputTokens: 20, TotalCostUSD: 1.5, NumTurns: 3}

	s.Settle(d, "butler-bugs", 0, result)

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

// TestButlerSettle_ReportsChoreSettledNotIssue pins issue #3878: a butler
// run's terminal report.Record must carry the Chore, not num (the
// dispatch.ChoreKey-shaped "butler-bugs"), since the report wire's Issue
// field means a tracker issue and a Chore is not one.
func TestButlerSettle_ReportsChoreSettledNotIssue(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)

	backend := ledger.Local{Repo: newButlerBareRepo(t)}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim := claimButlerChore(t, backend, "bugs", start)

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/501"

	scope := butler.Scope{Head: "headsha", NextCursor: "cursor2"}
	now := start.Add(time.Minute)
	s := NewButlerSettle(fc.AsIssueFiler(), backend, "bugs", claim, scope, func() time.Time { return now }, 0)

	s.Settle(dispatch.NewFake(), "butler-bugs", 0, readyResult())

	recs := readRecords()
	if len(recs) != 1 {
		t.Fatalf("records: got %d, want 1: %+v", len(recs), recs)
	}
	if recs[0].Event != "settled" || recs[0].Chore != "bugs" || recs[0].Issue != "" {
		t.Errorf("record = %+v, want event=settled chore=bugs issue=\"\"", recs[0])
	}
}

// (c) A partial filing failure still writes a done commit, recording only
// the intent that actually filed -- not the one whose PostIssue call failed
// -- so the next run doesn't refile the success and can still re-find the
// failure's own dedup keys via the backlog.
func TestButlerSettle_PartialFilingFailure_RecordsOnlyWhatFiled(t *testing.T) {
	backend := ledger.Local{Repo: newButlerBareRepo(t)}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim := claimButlerChore(t, backend, "bugs", start)

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/501"
	fc.PostIssueErrForTitle = map[string]error{"second finding": errors.New("create failed")}

	scope := butler.Scope{Head: "headsha", NextCursor: "cursor2"}
	now := start.Add(time.Minute)
	s := NewButlerSettle(fc.AsIssueFiler(), backend, "bugs", claim, scope, func() time.Time { return now }, 0)

	result := readyResult(
		`{"title":"first finding","body":"repro","dedupTerms":["a.go:Foo"]}`,
		`{"title":"second finding","body":"repro2","dedupTerms":["b.go:Bar"]}`,
	)

	s.Settle(dispatch.NewFake(), "butler-bugs", 0, result)

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
// Dropped field and the settled note.
func TestButlerSettle_CapDropsOverflowAndRecordsDropped(t *testing.T) {
	backend := ledger.Local{Repo: newButlerBareRepo(t)}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim := claimButlerChore(t, backend, "bugs", start)

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/501"

	scope := butler.Scope{Head: "headsha", NextCursor: "cursor2"}
	now := start.Add(time.Minute)
	s := NewButlerSettle(fc.AsIssueFiler(), backend, "bugs", claim, scope, func() time.Time { return now }, 2)

	result := readyResult(
		`{"title":"first finding","body":"repro","dedupTerms":["a.go:Foo"]}`,
		`{"title":"second finding","body":"repro2","dedupTerms":["b.go:Bar"]}`,
		`{"title":"third finding","body":"repro3","dedupTerms":["c.go:Baz"]}`,
	)

	s.Settle(dispatch.NewFake(), "butler-bugs", 0, result)

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
func TestButlerSettle_ZeroCapFilesEverything(t *testing.T) {
	backend := ledger.Local{Repo: newButlerBareRepo(t)}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim := claimButlerChore(t, backend, "bugs", start)

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/501"

	scope := butler.Scope{Head: "headsha", NextCursor: "cursor2"}
	now := start.Add(time.Minute)
	s := NewButlerSettle(fc.AsIssueFiler(), backend, "bugs", claim, scope, func() time.Time { return now }, 0)

	result := readyResult(
		`{"title":"first finding","body":"repro","dedupTerms":["a.go:Foo"]}`,
		`{"title":"second finding","body":"repro2","dedupTerms":["b.go:Bar"]}`,
	)

	s.Settle(dispatch.NewFake(), "butler-bugs", 0, result)

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

// (c4) A malformed payload never counts toward the cap -- it is skipped by
// fileIssueIntentsDetailedFunc's own malformed check regardless of where it
// falls, and the cap still admits exactly maxFindingsPerSweep well-formed
// intents around it.
func TestButlerSettle_MalformedPayloadDoesNotCountTowardCap(t *testing.T) {
	backend := ledger.Local{Repo: newButlerBareRepo(t)}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim := claimButlerChore(t, backend, "bugs", start)

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/501"

	scope := butler.Scope{Head: "headsha", NextCursor: "cursor2"}
	now := start.Add(time.Minute)
	s := NewButlerSettle(fc.AsIssueFiler(), backend, "bugs", claim, scope, func() time.Time { return now }, 1)

	result := readyResult(
		`{"title":"first finding","body":"repro","dedupTerms":["a.go:Foo"]}`,
		`not valid json`,
		`{"title":"second finding","body":"repro2","dedupTerms":["b.go:Bar"]}`,
	)

	s.Settle(dispatch.NewFake(), "butler-bugs", 0, result)

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
// done commit, lastSwept/cursor unadvanced.
func TestButlerSettle_CrashedRun_LeavesClaimUnadvanced(t *testing.T) {
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
			backend := ledger.Local{Repo: newButlerBareRepo(t)}
			start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

			// A prior done run establishes a non-empty lastSwept/cursor so
			// this case can prove the claim carries them forward untouched,
			// not merely that both stayed at their zero value.
			firstClaim, err := ledger.Claim(backend, "bugs", ledger.Tip{}, ledger.ClaimedBy{Host: "worker", Start: start})
			if err != nil {
				t.Fatalf("initial Claim: %v", err)
			}
			if _, err := ledger.Finish(backend, "bugs", firstClaim, ledger.State{LastSwept: "prevhead", Cursor: "prevcursor"}, start.Add(time.Minute)); err != nil {
				t.Fatalf("initial Finish: %v", err)
			}

			claim := claimButlerChore(t, backend, "bugs", start.Add(2*time.Minute))

			fc := forge.NewFake()
			scope := butler.Scope{Head: "newhead", NextCursor: "newcursor"}
			now := start.Add(3 * time.Minute)
			s := NewButlerSettle(fc.AsIssueFiler(), backend, "bugs", claim, scope, func() time.Time { return now }, 0)

			s.Settle(dispatch.NewFake(), "butler-bugs", 0, tc.result)

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
